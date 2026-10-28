package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
)

type AdminOperatorPermissionRepository interface {
	Get(ctx context.Context, userID int64) (raw []byte, found bool, err error)
	Upsert(ctx context.Context, userID int64, raw []byte, updatedBy int64) error
}
type OperatorAccessQueryRepository interface {
	GroupHasProtectedMembers(ctx context.Context, groupID, operatorID int64) (bool, error)
	ProxyIDsUsedByAccounts(ctx context.Context, proxyIDs []int64) ([]int64, error)
}

type OperatorAccessService struct {
	repo                 AdminOperatorPermissionRepository
	userRepo             UserRepository
	apiKeyRepo           APIKeyRepository
	subscriptionRepo     UserSubscriptionRepository
	userGroupRateRepo    UserGroupRateRepository
	redeemRepo           RedeemCodeRepository
	proxyRepo            ProxyRepository
	adminService         AdminService
	paymentService       *PaymentService
	channelService       *ChannelService
	monitorService       *ChannelMonitorService
	templateService      *ChannelMonitorRequestTemplateService
	moderationService    *ContentModerationService
	opsService           *OpsService
	settingService       *SettingService
	paymentConfigService *PaymentConfigService
	auditLogService      *AuditLogService
	queryRepo            OperatorAccessQueryRepository
	invalidRows          sync.Map // log each malformed stored user row once
}

func NewOperatorAccessService(repo AdminOperatorPermissionRepository, userRepo UserRepository, apiKeyRepo APIKeyRepository, subscriptionRepo UserSubscriptionRepository, userGroupRateRepo UserGroupRateRepository, redeemRepo RedeemCodeRepository, proxyRepo ProxyRepository, adminService AdminService, paymentService *PaymentService, channelService *ChannelService, monitorService *ChannelMonitorService, templateService *ChannelMonitorRequestTemplateService, moderationService *ContentModerationService, opsService *OpsService, settingService *SettingService, paymentConfigService *PaymentConfigService, auditLogService *AuditLogService, queryRepo OperatorAccessQueryRepository) *OperatorAccessService {
	s := &OperatorAccessService{repo: repo, userRepo: userRepo, apiKeyRepo: apiKeyRepo, subscriptionRepo: subscriptionRepo, userGroupRateRepo: userGroupRateRepo, redeemRepo: redeemRepo, proxyRepo: proxyRepo, adminService: adminService, paymentService: paymentService, channelService: channelService, monitorService: monitorService, templateService: templateService, moderationService: moderationService, opsService: opsService, settingService: settingService, paymentConfigService: paymentConfigService, auditLogService: auditLogService, queryRepo: queryRepo}
	if impl, ok := adminService.(*adminServiceImpl); ok {
		impl.requireActor = true
	}
	return s
}

func operatorUnavailable() error {
	return infraerrors.New(http.StatusServiceUnavailable, "OPERATOR_ACCESS_UNAVAILABLE", "operator access unavailable")
}

func operatorLoadError(err, notFound error) error {
	if err == nil {
		return notFound
	}
	if infraerrors.Code(err) == http.StatusNotFound {
		return err
	}
	return operatorUnavailable()
}

func (s *OperatorAccessService) permissions(ctx context.Context, userID int64) (operatoraccess.Permissions, error) {
	empty := operatoraccess.Permissions{}
	if s == nil || s.repo == nil {
		return empty, operatorUnavailable()
	}
	raw, found, err := s.repo.Get(ctx, userID)
	if err != nil {
		return empty, operatorUnavailable()
	}
	if !found {
		return empty, nil
	}
	p, err := operatoraccess.DecodeWire(raw)
	if err != nil {
		if _, logged := s.invalidRows.LoadOrStore(userID, true); !logged {
			slog.Warn("operator_access.invalid_row", "user_id", userID)
		}
		return empty, nil
	}
	return p, nil
}
func (s *OperatorAccessService) LoadScope(ctx context.Context, user *User) (*operatoraccess.Scope, error) {
	if user == nil || user.Role != RoleOperator {
		return nil, nil
	}
	p, err := s.permissions(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	id := user.ID
	return &operatoraccess.Scope{UserID: id, Perms: p, Guard: s, Reload: func(ctx context.Context) (operatoraccess.Permissions, bool, error) {
		if s.userRepo == nil {
			return operatoraccess.Permissions{}, false, operatorUnavailable()
		}
		fresh, err := s.userRepo.GetByID(ctx, id)
		if errors.Is(err, ErrUserNotFound) {
			return operatoraccess.Permissions{}, false, nil
		}
		if err != nil {
			return operatoraccess.Permissions{}, false, operatorUnavailable()
		}
		if fresh == nil || fresh.DeletedAt != nil || fresh.Role != RoleOperator || !fresh.IsActive() {
			return operatoraccess.Permissions{}, false, nil
		}
		p, err := s.permissions(ctx, id)
		return p, err == nil, err
	}}, nil
}
func (s *OperatorAccessService) permissionUser(ctx context.Context, id int64) error {
	if s == nil || s.userRepo == nil {
		return operatorUnavailable()
	}
	u, err := s.userRepo.GetByID(ctx, id)
	if err != nil {
		return operatorLoadError(err, ErrUserNotFound)
	}
	if u == nil || u.DeletedAt != nil {
		return ErrUserNotFound
	}
	return nil
}
func (s *OperatorAccessService) GetPermissions(ctx context.Context, userID int64) (operatoraccess.Wire, error) {
	if err := s.permissionUser(ctx, userID); err != nil {
		return operatoraccess.Wire{}, err
	}
	p, err := s.permissions(ctx, userID)
	return p.Wire(), err
}
func (s *OperatorAccessService) PutPermissions(ctx context.Context, actorID, userID int64, raw []byte) (operatoraccess.Wire, error) {
	p, err := operatoraccess.DecodeWire(raw)
	if err != nil {
		return operatoraccess.Wire{}, infraerrors.New(http.StatusBadRequest, "ADMIN_PERMISSION_INVALID", "invalid operator permissions")
	}
	if err = s.permissionUser(ctx, userID); err != nil {
		return operatoraccess.Wire{}, err
	}
	before, err := s.permissions(ctx, userID)
	if err != nil {
		return operatoraccess.Wire{}, err
	}
	after := p.Wire()
	canonical, err := json.Marshal(after)
	if err != nil {
		return operatoraccess.Wire{}, operatorUnavailable()
	}
	if err = s.repo.Upsert(ctx, userID, canonical, actorID); err != nil {
		return operatoraccess.Wire{}, operatorUnavailable()
	}
	diff, _ := json.Marshal(struct {
		UserID int64               `json:"user_id"`
		Before operatoraccess.Wire `json:"before"`
		After  operatoraccess.Wire `json:"after"`
	}{userID, before.Wire(), after})
	s.auditLogService.Record(&AuditLog{ActorUserID: &actorID, Action: "admin.operator_permissions.update", Method: http.MethodPut, Path: "/api/v1/admin/users/:id/operator-permissions", StatusCode: http.StatusOK, RequestBody: string(diff)})
	return after, nil
}
func (s *OperatorAccessService) AllowsAdminExport(ctx context.Context, userID int64) bool {
	if s == nil || s.userRepo == nil {
		return false
	}
	u, err := s.userRepo.GetByID(ctx, userID)
	if err != nil || u == nil || u.DeletedAt != nil || u.Role != RoleOperator || !u.IsActive() {
		return false
	}
	p, err := s.permissions(ctx, userID)
	return err == nil && p.Can(operatoraccess.ModuleUsage, operatoraccess.LevelView) && p.Has(operatoraccess.FlagDataExport)
}

// MeAccess contains only the console bootstrap fields from B11.1. Custom menu
// entries remain raw JSON here to keep service independent of handler/dto.
type MeAccess struct {
	Role            string            `json:"role"`
	IsSuperAdmin    bool              `json:"is_super_admin"`
	Modules         map[string]string `json:"modules"`
	Flags           []string          `json:"flags"`
	Settings        MeAccessSettings  `json:"settings"`
	CustomMenuItems json.RawMessage   `json:"custom_menu_items"`
}

// operatorMenuItem mirrors the existing DTO JSON shape without a service/handler cycle.
type operatorMenuItem struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	IconSVG        string `json:"icon_svg"`
	URL            string `json:"url"`
	PageSlug       string `json:"page_slug,omitempty"`
	Visibility     string `json:"visibility"`
	SortOrder      int    `json:"sort_order"`
	HideOpenButton bool   `json:"hide_open_button,omitempty"`
}

type MeAccessSettings struct {
	OpsMonitoringEnabled            bool    `json:"ops_monitoring_enabled"`
	OpsRealtimeMonitoringEnabled    bool    `json:"ops_realtime_monitoring_enabled"`
	OpsQueryModeDefault             string  `json:"ops_query_mode_default"`
	AdminUsageCacheHitRateEnabled   bool    `json:"admin_usage_cache_hit_rate_enabled"`
	AdminUsageTokenSpeedEnabled     bool    `json:"admin_usage_token_speed_enabled"`
	PaymentEnabled                  bool    `json:"payment_enabled"`
	AccountQuotaNotifyEnabled       bool    `json:"account_quota_notify_enabled"`
	WebSearchEmulationEnabled       bool    `json:"web_search_emulation_enabled"`
	PaymentSubscriptionUSDToCNYRate float64 `json:"payment_subscription_usd_to_cny_rate"`
	PaymentRechargeFeeRate          float64 `json:"payment_recharge_fee_rate"`
}

func (s *OperatorAccessService) MeAccess(ctx context.Context, user *User) (MeAccess, error) {
	out := MeAccess{Modules: map[string]string{}, Flags: []string{}, CustomMenuItems: json.RawMessage("[]")}
	if user == nil || !user.CanAccessAdmin() {
		return out, infraerrors.New(http.StatusForbidden, "SUPER_ADMIN_REQUIRED", "admin access required")
	}
	out.Role = user.Role
	out.IsSuperAdmin = user.Role == RoleAdmin
	p := operatoraccess.Permissions{}
	if out.IsSuperAdmin {
		p.Modules = map[operatoraccess.Module]operatoraccess.Level{}
		p.Flags = map[operatoraccess.Flag]bool{}
		for _, m := range operatoraccess.Modules {
			p.Modules[m] = operatoraccess.LevelManage
		}
		for _, f := range operatoraccess.Flags {
			p.Flags[f] = true
		}
	} else {
		var err error
		p, err = s.permissions(ctx, user.ID)
		if err != nil {
			return out, err
		}
	}
	w := p.Wire()
	out.Modules = w.Modules
	out.Flags = w.Flags
	warn := func(field string) { slog.Warn("operator_access.me_access_setting_failed", "field", field) }
	if s.settingService == nil {
		warn("settings")
	} else {
		settings, err := s.settingService.GetAllSettings(ctx)
		if err != nil || settings == nil {
			warn("settings")
		} else {
			if s.opsService != nil {
				out.Settings.OpsMonitoringEnabled = s.opsService.IsMonitoringEnabled(ctx) && settings.OpsMonitoringEnabled
			} else {
				warn("ops_monitoring_enabled")
			}
			out.Settings.OpsRealtimeMonitoringEnabled = settings.OpsRealtimeMonitoringEnabled
			out.Settings.OpsQueryModeDefault = settings.OpsQueryModeDefault
			out.Settings.AdminUsageCacheHitRateEnabled = settings.AdminUsageCacheHitRateEnabled
			out.Settings.AdminUsageTokenSpeedEnabled = settings.AdminUsageTokenSpeedEnabled
			out.Settings.AccountQuotaNotifyEnabled = settings.AccountQuotaNotifyEnabled
		}
		cfg, err := s.settingService.GetWebSearchEmulationConfig(ctx)
		if err != nil || cfg == nil {
			warn("web_search_emulation_enabled")
		} else {
			out.Settings.WebSearchEmulationEnabled = cfg.Enabled && len(cfg.Providers) > 0
		}
		if out.IsSuperAdmin {
			raw := s.settingService.GetCustomMenuItemsRaw(ctx)
			var items []operatorMenuItem
			if json.Unmarshal([]byte(raw), &items) == nil && items != nil {
				out.CustomMenuItems, _ = json.Marshal(items)
			}
		}
	}
	if s.paymentConfigService == nil {
		warn("payment")
	} else {
		cfg, err := s.paymentConfigService.GetPaymentConfig(ctx)
		if err != nil || cfg == nil {
			warn("payment")
		} else {
			out.Settings.PaymentEnabled = cfg.Enabled
			out.Settings.PaymentSubscriptionUSDToCNYRate = cfg.SubscriptionUSDToCNYRate
			out.Settings.PaymentRechargeFeeRate = cfg.RechargeFeeRate
		}
	}
	return out, nil
}
