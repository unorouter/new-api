package controller

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/bytedance/gopkg/util/gopool"
)

// An IPN is a single HTTP delivery: a 401 on our side, a redeploy, or an
// outage at theirs, and the order stays pending while the money has arrived.
// Polling the payment back from NowPayments makes settlement depend on the
// order record alone. The minimum age leaves the normal IPN path its head
// start; the maximum age matches how long NowPayments keeps a payment.
const (
	nowPaymentsReconcileTick   = 5 * time.Minute
	nowPaymentsReconcileMinAge = 10 * time.Minute
	nowPaymentsReconcileMaxAge = 7 * 24 * time.Hour
	nowPaymentsReconcileBatch  = 200
)

var (
	nowPaymentsReconcileOnce    sync.Once
	nowPaymentsReconcileRunning atomic.Bool
)

func StartNowPaymentsReconcileTask() {
	nowPaymentsReconcileOnce.Do(func() {
		if !common.IsMasterNode {
			return
		}
		gopool.Go(func() {
			logger.LogInfo(context.Background(), fmt.Sprintf("NowPayments reconcile task started: tick=%s min_age=%s max_age=%s", nowPaymentsReconcileTick, nowPaymentsReconcileMinAge, nowPaymentsReconcileMaxAge))
			ticker := time.NewTicker(nowPaymentsReconcileTick)
			defer ticker.Stop()
			runNowPaymentsReconcileOnce()
			for range ticker.C {
				runNowPaymentsReconcileOnce()
			}
		})
	})
}

func runNowPaymentsReconcileOnce() {
	if !nowPaymentsReconcileRunning.CompareAndSwap(false, true) {
		return
	}
	defer nowPaymentsReconcileRunning.Store(false)
	if !setting.NowPaymentsEnabled || setting.NowPaymentsApiKey == "" {
		return
	}
	ctx := context.Background()
	now := time.Now()
	orders, err := model.GetPendingTopUpsWithProviderPaymentId(
		model.PaymentProviderNowPayments,
		now.Add(-nowPaymentsReconcileMaxAge).Unix(),
		now.Add(-nowPaymentsReconcileMinAge).Unix(),
		nowPaymentsReconcileBatch,
	)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("NowPayments reconcile failed to list pending orders error=%q", err.Error()))
		return
	}
	settled := 0
	for _, order := range orders {
		event, err := fetchNowPaymentsPayment(order.ProviderPaymentId)
		if err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("NowPayments reconcile lookup failed trade_no=%s payment_id=%s error=%q", order.TradeNo, order.ProviderPaymentId, err.Error()))
			continue
		}
		if event.OrderId == "" {
			event.OrderId = order.TradeNo
		}
		if event.OrderId != order.TradeNo {
			logger.LogWarn(ctx, fmt.Sprintf("NowPayments reconcile payment belongs to another order trade_no=%s payment_id=%s returned_order_id=%s", order.TradeNo, order.ProviderPaymentId, event.OrderId))
			continue
		}
		switch event.PaymentStatus {
		case "finished", "partially_paid", "failed", "expired", "refunded":
			settleNowPaymentsEvent(ctx, event, "source=reconcile")
			settled++
		default:
			// Still in flight or never paid: the IPN or the next tick decides.
			// Keep the paid figure current so a stalled order stays telling.
			if event.ActuallyPaid > 0 {
				if err := model.SetTopUpPaidAmount(order.TradeNo, event.ActuallyPaid); err != nil {
					logger.LogError(ctx, fmt.Sprintf("NowPayments reconcile failed to record paid amount trade_no=%s actually_paid=%v error=%q", order.TradeNo, event.ActuallyPaid, err.Error()))
				}
			}
		}
	}
	if settled > 0 {
		logger.LogInfo(ctx, fmt.Sprintf("NowPayments reconcile settled %d of %d pending orders", settled, len(orders)))
	}
}
