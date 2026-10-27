package service

import (
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// StartConsentPruner runs on the master node only, once a day.
func StartConsentPruner() {
	if !common.IsMasterNode {
		return
	}
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for ; true; <-ticker.C {
			if n, err := model.PruneGuestOwnPurposeObjections(time.Now()); err != nil {
				common.SysError("guest objection prune failed: " + err.Error())
			} else if n > 0 {
				common.SysLog(fmt.Sprintf("pruned %d guest objections past the guest cookie lifetime", n))
			}
			if n, err := model.PruneProviderConsents(time.Now()); err != nil {
				common.SysError("provider consent prune failed: " + err.Error())
			} else if n > 0 {
				common.SysLog(fmt.Sprintf("pruned %d provider consent records past the proof period", n))
			}
			if n, err := model.PruneTermsAcceptances(time.Now()); err != nil {
				common.SysError("terms acceptance prune failed: " + err.Error())
			} else if n > 0 {
				common.SysLog(fmt.Sprintf("pruned %d terms acceptance records past the proof period", n))
			}
			n, err := model.PruneFreeConsents(time.Now())
			if err != nil {
				common.SysError("archived model consent prune failed: " + err.Error())
				continue
			}
			if n > 0 {
				common.SysLog(fmt.Sprintf("pruned %d archived model consent records past the proof period", n))
			}
		}
	}()
}
