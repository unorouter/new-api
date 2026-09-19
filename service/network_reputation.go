package service

import (
	"fmt"
	"net"
	"strings"
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
	burstMin := setting.FreeAbuseBurstMinAccounts
	domainMin := setting.FreeAbuseUsernameDomainMinAccounts
	if minAccounts <= 0 && burstMin <= 0 && domainMin <= 0 {
		currentNetworkReputation.Store(&networkReputation{})
		return
	}
	windowDays := setting.FreeAbuseNetworkWindowDays
	if windowDays <= 0 {
		windowDays = 14
	}
	burstDays := setting.FreeAbuseBurstWindowDays
	if burstDays <= 0 {
		burstDays = 90
	}
	now := time.Now()
	networkSince := now.AddDate(0, 0, -windowDays).Unix()
	fetchSince := networkSince
	if (burstMin > 0 || domainMin > 0) && burstDays > windowDays {
		fetchSince = now.AddDate(0, 0, -burstDays).Unix()
	}
	rows, err := model.RegistrationProvenanceSince(fetchSince)
	if err != nil {
		// Keep the previous verdict: dropping it would silently un-ban every farm
		// account until the next tick succeeds.
		common.SysError("failed to refresh network reputation: " + err.Error())
		return
	}

	accounts := make(map[string]int)
	identities := make(map[string]int)
	// Identity-free registrations per wall-clock minute, across every network. A
	// farm that rents one residential exit per account never puts ten accounts in
	// one /24, but it still has to create them in a burst: 347 accounts in 25
	// minutes from 279 different /16s on 2026-09-16, against a baseline that never
	// reached eight password-only signups in a minute outside farm days.
	burstMinutes := make(map[int64]int)
	// Accounts whose username is an address at a domain nobody real uses: the
	// farms mint <random>@<their domain> and leave the email field empty. Public
	// mail providers are excluded because thousands of people type their real
	// address as a username.
	domainAllow := usernameDomainAllowlist(setting.FreeAbuseUsernameDomainAllowlist)
	domainAccounts := make(map[string]int)
	domainIdentities := make(map[string]int)
	for i := range rows {
		if !rows[i].HasIdentity() && burstMin > 0 {
			burstMinutes[rows[i].CreatedAt/60]++
		}
		if domainMin > 0 {
			if domain := usernameDomain(rows[i].Username, rows[i].Email, domainAllow); domain != "" {
				domainAccounts[domain]++
				if rows[i].HasIdentity() {
					domainIdentities[domain]++
				}
			}
		}
		if minAccounts <= 0 || rows[i].CreatedAt <= networkSince {
			continue
		}
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

	flaggedDomains := make(map[string]struct{})
	for domain, total := range domainAccounts {
		if total < domainMin {
			continue
		}
		if domainIdentities[domain]*100 >= total*setting.FreeAbuseNetworkMaxIdentityPct {
			continue
		}
		flaggedDomains[domain] = struct{}{}
	}

	shadowBanned := make(map[int]struct{})
	burstBanned := 0
	domainBanned := 0
	for i := range rows {
		if rows[i].HasIdentity() || rows[i].UsedQuota > 0 {
			continue
		}
		if len(flaggedDomains) > 0 {
			if _, ok := flaggedDomains[usernameDomain(rows[i].Username, rows[i].Email, domainAllow)]; ok {
				shadowBanned[rows[i].Id] = struct{}{}
				domainBanned++
				continue
			}
		}
		// A typed email is not identity, but the farms never bother with one (1% of
		// farm-minute accounts against 40% of ordinary signups), so it separates
		// the person who happened to register in a farm minute from the farm.
		if burstMin > 0 && rows[i].Email == "" && burstMinutes[rows[i].CreatedAt/60] >= burstMin {
			shadowBanned[rows[i].Id] = struct{}{}
			burstBanned++
			continue
		}
		if rows[i].CreatedAt <= networkSince {
			continue
		}
		if _, ok := flaggedNetworks[registrationNetworkKey(rows[i].RegisterIp)]; ok {
			shadowBanned[rows[i].Id] = struct{}{}
		}
	}

	prev := currentNetworkReputation.Load()
	if prev == nil || len(prev.shadowBanned) != len(shadowBanned) {
		common.SysLog(fmt.Sprintf("network reputation: %d flagged networks, %d flagged username domains, %d shadow-banned accounts (%d from registration bursts, %d from username domains)",
			len(flaggedNetworks), len(flaggedDomains), len(shadowBanned), burstBanned, domainBanned))
	}
	currentNetworkReputation.Store(&networkReputation{
		flaggedNetworks: flaggedNetworks,
		shadowBanned:    shadowBanned,
	})
}

func usernameDomainAllowlist(raw string) map[string]struct{} {
	allow := make(map[string]struct{})
	for _, item := range strings.Split(raw, ",") {
		if item = strings.ToLower(strings.TrimSpace(item)); item != "" {
			allow[item] = struct{}{}
		}
	}
	return allow
}

// usernameDomain returns the domain of an address-shaped username on an account
// that typed no email, or "" when the username is not an address or the domain is
// a public mail provider.
func usernameDomain(username, email string, allow map[string]struct{}) string {
	if email != "" {
		return ""
	}
	at := strings.LastIndex(username, "@")
	if at <= 0 || at == len(username)-1 {
		return ""
	}
	domain := strings.ToLower(username[at+1:])
	if !strings.Contains(domain, ".") || strings.ContainsAny(domain, " \t/\\") {
		return ""
	}
	if _, ok := allow[domain]; ok {
		return ""
	}
	return domain
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
	if verdict := currentNetworkReputation.Load(); verdict != nil {
		if _, ok := verdict.shadowBanned[userId]; ok {
			return true
		}
	}
	return cooccurrenceShadowBanned(userId)
}
