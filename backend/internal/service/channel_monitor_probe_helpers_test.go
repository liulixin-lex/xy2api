//go:build unit

package service

import "context"

// validateChallenge 在完整的响应正文中查找 expected 整数答案，返回是否通过校验。
// <think> 标签内的内容不参与校验。
func validateChallenge(responseText, expected string) bool {
	return probeAnswerFound(stripThinkBlocks(responseText), expected, true)
}

// runCheckForModel 用缺省预算对单个 (provider, model) 做一轮探测（见 channel_monitor_probe_run.go）。
func runCheckForModel(ctx context.Context, provider, endpoint, apiKey, model string, opts *CheckOptions) *CheckResult {
	return runProbe(ctx, provider, endpoint, apiKey, model, opts, defaultProbeLimits())
}
