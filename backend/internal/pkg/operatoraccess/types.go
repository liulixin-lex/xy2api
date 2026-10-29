package operatoraccess

import (
	"encoding/json"
	"errors"
	"sort"
)

type Module string

const (
	ModuleDashboard       Module = "dashboard"
	ModuleOps             Module = "ops"
	ModuleUsers           Module = "users"
	ModuleGroups          Module = "groups"
	ModuleChannels        Module = "channels"
	ModuleAccounts        Module = "accounts"
	ModuleShareScheduling Module = "share_scheduling"
	ModuleAnnouncements   Module = "announcements"
	ModuleSupportTickets  Module = "support_tickets"
	ModuleProxies         Module = "proxies"
	ModuleSecurityAudit   Module = "security_audit"
	ModuleRedeemCodes     Module = "redeem_codes"
	ModulePromoCodes      Module = "promo_codes"
	ModuleAffiliates      Module = "affiliates"
	ModuleOrders          Module = "orders"
	ModuleUsage           Module = "usage"
	ModuleAuditLogs       Module = "audit_logs"
	ModuleSubscriptions   Module = "subscriptions"
)

// Modules lists the available permission modules in UI order.
var Modules = []Module{ModuleDashboard, ModuleOps, ModuleUsers, ModuleGroups, ModuleChannels,
	ModuleAccounts, ModuleShareScheduling, ModuleAnnouncements, ModuleSupportTickets, ModuleProxies, ModuleSecurityAudit,
	ModuleRedeemCodes, ModulePromoCodes, ModuleAffiliates, ModuleOrders, ModuleUsage,
	ModuleAuditLogs, ModuleSubscriptions}

type Level uint8

const (
	LevelNone Level = iota
	LevelView
	LevelManage
)

func ParseLevel(s string) (Level, bool) {
	switch s {
	case "view":
		return LevelView, true
	case "manage":
		return LevelManage, true
	}
	return LevelNone, false
}
func (l Level) String() string {
	switch l {
	case LevelView:
		return "view"
	case LevelManage:
		return "manage"
	}
	return ""
}

type Flag string

const (
	FlagUsersBalanceAdjust      Flag = "users.balance_adjust"
	FlagOrdersRefund            Flag = "orders.refund"
	FlagRedeemCodesIssue        Flag = "redeem_codes.issue"
	FlagDataExport              Flag = "data.export"
	FlagAccountsUpstreamAddress Flag = "accounts.upstream_address"
)

var Flags = []Flag{FlagUsersBalanceAdjust, FlagOrdersRefund, FlagRedeemCodesIssue, FlagDataExport, FlagAccountsUpstreamAddress}

// Permissions is the decoded admin_operator_permissions.permissions value.
type Permissions struct {
	Modules map[Module]Level
	Flags   map[Flag]bool
}

func (p Permissions) Can(m Module, l Level) bool { return l != LevelNone && p.Modules[m] >= l }
func (p Permissions) Has(f Flag) bool            { return p.Flags[f] }

// Wire is the JSON shape of DEV_PLAN 5.3. Flags are sorted ascending.
type Wire struct {
	Modules map[string]string `json:"modules"`
	Flags   []string          `json:"flags"`
}

// DecodeWire parses and validates a PUT body or a stored row. Unknown top-level
// keys, unknown module ids, values other than view|manage, and unknown flag ids
// return ErrInvalid (mapped to 400 ADMIN_PERMISSION_INVALID). Duplicate flags
// collapse. null modules or flags mean empty.
func DecodeWire(raw []byte) (Permissions, error) {
	empty := Permissions{Modules: map[Module]Level{}, Flags: map[Flag]bool{}}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return empty, ErrInvalid
	}
	for k := range obj {
		if k != "modules" && k != "flags" {
			return empty, ErrInvalid
		}
	}
	var w Wire
	if json.Unmarshal(raw, &w) != nil {
		return empty, ErrInvalid
	}
	p := Permissions{Modules: map[Module]Level{}, Flags: map[Flag]bool{}}
	for m, v := range w.Modules {
		known := false
		for _, id := range Modules {
			if string(id) == m {
				known = true
				break
			}
		}
		l, ok := ParseLevel(v)
		if !known || !ok {
			return empty, ErrInvalid
		}
		p.Modules[Module(m)] = l
	}
	for _, v := range w.Flags {
		known := false
		for _, id := range Flags {
			if string(id) == v {
				known = true
				break
			}
		}
		if !known {
			return empty, ErrInvalid
		}
		p.Flags[Flag(v)] = true
	}
	return p, nil
}
func (p Permissions) Wire() Wire {
	w := Wire{Modules: map[string]string{}, Flags: []string{}}
	for _, m := range Modules {
		if l := p.Modules[m]; l == LevelView || l == LevelManage {
			w.Modules[string(m)] = l.String()
		}
	}
	for _, f := range Flags {
		if p.Has(f) {
			w.Flags = append(w.Flags, string(f))
		}
	}
	sort.Strings(w.Flags)
	return w
}

var ErrInvalid = errors.New("operatoraccess: invalid permissions")
