package service

import "context"

func schedQualitySettingEnabled(value string) bool {
	return value == "" || value == "true"
}

// SchedQualityEnabled returns the global quality switch, defaulting to enabled.
func (s *SettingService) SchedQualityEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return true
	}
	return s.getGatewayForwardingSettingsCached(ctx).schedQualityEnabled
}

// SchedQualityTestEnabled returns the automatic test switch, defaulting to enabled.
func (s *SettingService) SchedQualityTestEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return true
	}
	return s.getGatewayForwardingSettingsCached(ctx).schedQualityTestEnabled
}
