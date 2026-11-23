//go:build unit

package handler

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type collabPolicyPanicRepo struct {
	contentModerationHandlerTestRepo
}

func (*collabPolicyPanicRepo) CreateLog(context.Context, *service.ContentModerationLog) error {
	panic("synthetic policy recorder failure")
}

type collabPolicyLogSink chan *logger.LogEvent

func (s collabPolicyLogSink) WriteLogEvent(e *logger.LogEvent) {
	if e.Message == "billing.policy_recorder_panic" {
		s <- e
	}
}

func TestCollabPolicyRecorderPanicIsLoggedAndCounted(t *testing.T) {
	logger.InitBootstrap()
	logs := make(collabPolicyLogSink, 1)
	logger.SetSink(logs)
	t.Cleanup(func() { logger.SetSink(nil) })
	before := resilience.CollabSafetySnapshot().ConsumerPanics
	setting := &contentModerationHandlerSettingRepo{values: map[string]string{service.SettingKeyRiskControlEnabled: "true"}}
	svc := service.NewContentModerationService(setting, &collabPolicyPanicRepo{}, nil, nil, nil, nil, nil, nil)
	handler := &OpenAIGatewayHandler{contentModerationService: svc}
	c := newTestGinContext()
	service.MarkOpsCyberPolicy(c, service.CyberPolicyMark{Message: "synthetic rejection", UpstreamStatus: 400})
	handler.recordCyberPolicyIfMarked(c, nil, nil, nil, "synthetic", false, nil, service.ChannelUsageFields{}, "")
	select {
	case event := <-logs:
		require.Equal(t, "error", event.Level)
		require.Equal(t, "openai.policy_recorder", event.Fields["component"])
		require.Equal(t, "synthetic policy recorder failure", event.Fields["panic"])
	case <-time.After(time.Second):
		t.Fatal("missing policy recorder panic log")
	}
	require.Equal(t, before+1, resilience.CollabSafetySnapshot().ConsumerPanics)
}
