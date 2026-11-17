package service

import "context"

const SettingKeySchedQualityTTFTRule = "sched_quality_ttft_rule"

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
