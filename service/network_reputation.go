package service

import (
	"net"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

const networkReputationInterval = 10 * time.Minute

// networkReputation is the periodically recomputed account-farm verdict. Both
// maps are replaced wholesale and never mutated, so readers need no lock.
type networkReputation struct {
	flaggedNetworks map[string]struct{}
	shadowBanned    map[int]struct{}
}

var currentNetworkReputation atomic.Pointer[networkReputation]

// StartNetworkReputation keeps the verdict fresh on EVERY instance rather than
// only the master: it gates the relay path, so a slave holding an empty map would
// happily serve the farms the master is refusing.
func StartNetworkReputation() {
	go func() {
		refreshNetworkReputation()
		ticker := time.NewTicker(networkReputationInterval)
		defer ticker.Stop()
		for range ticker.C {
			refreshNetworkReputation()
		}
	}()
}

// registrationNetworkKey groups an address the way a farm cannot cheaply escape:
// /24 for IPv4, /48 for IPv6. The per-IP cap (REGISTER_IP_MAX_ACCOUNTS) only ever
// sees a single address, so rotating within one rented allocation defeats it while
// staying under the limit on every individual IP.
func registrationNetworkKey(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}
	if v4 := parsed.To4(); v4 != nil {
		return v4.Mask(net.CIDRMask(24, 32)).String() + "/24"
	}
	return parsed.Mask(net.CIDRMask(48, 128)).String() + "/48"
}

func refreshNetworkReputation() {
	setting := operation_setting.GetQuotaSetting()
	minAccounts := setting.FreeAbuseNetworkMinAccounts
	if minAccounts <= 0 {
		currentNetworkReputation.Store(&networkReputation{})
		return
	}
	windowDays := setting.FreeAbuseNetworkWindowDays
	if windowDays <= 0 {
		windowDays = 14
	}
	rows, err := model.RegistrationProvenanceSince(time.Now().AddDate(0, 0, -windowDays).Unix())
	if err != nil {
		// Keep the previous verdict: dropping it would silently un-ban every farm
		// account until the next tick succeeds.
		common.SysError("failed to refresh network reputation: " + err.Error())
		return
	}

	accounts := make(map[string]int)
	identities := make(map[string]int)
	for i := range rows {
		key := registrationNetworkKey(rows[i].RegisterIp)
		if key == "" {
			continue
		}
		accounts[key]++
		if rows[i].HasIdentity() {
			identities[key]++
		}
	}

	flaggedNetworks := make(map[string]struct{})
	for key, total := range accounts {
		if total < minAccounts {
			continue
		}
		// A network where a real share of accounts bound a third-party identity is
		// a shared address (household, campus, carrier NAT), not a farm.
		if identities[key]*100 >= total*setting.FreeAbuseNetworkMaxIdentityPct {
			continue
		}
		flaggedNetworks[key] = struct{}{}
	}

	shadowBanned := make(map[int]struct{})
	for i := range rows {
		if rows[i].HasIdentity() || rows[i].UsedQuota > 0 {
			continue
		}
		if _, ok := flaggedNetworks[registrationNetworkKey(rows[i].RegisterIp)]; ok {
			shadowBanned[rows[i].Id] = struct{}{}
		}
	}

	currentNetworkReputation.Store(&networkReputation{
		flaggedNetworks: flaggedNetworks,
		shadowBanned:    shadowBanned,
	})
}

// RegistrationNetworkFlagged reports whether new accounts from this address would
// join an allocation that bulk-registers and almost never binds an identity.
func RegistrationNetworkFlagged(ip string) bool {
	verdict := currentNetworkReputation.Load()
	if verdict == nil || len(verdict.flaggedNetworks) == 0 {
		return false
	}
	key := registrationNetworkKey(ip)
	if key == "" {
		return false
	}
	_, ok := verdict.flaggedNetworks[key]
	return ok
}

// FreeModelsShadowBanned reports whether this account came out of an account farm:
// created on a flagged network, with no third-party identity and no spend. The
// verdict lives only in memory and is never written to the account, so an operator
// sees individual requests fail like any throttled free user instead of watching a
// stockpile go dark at one timestamp.
func FreeModelsShadowBanned(userId int) bool {
	if userId <= 0 {
		return false
	}
	verdict := currentNetworkReputation.Load()
	if verdict == nil {
		return false
	}
	_, ok := verdict.shadowBanned[userId]
	return ok
}
