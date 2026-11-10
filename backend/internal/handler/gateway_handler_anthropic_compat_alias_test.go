//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func (*handlerCompatAccounts) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]service.Account, error) {
	return nil, nil
}

func TestAnthropicCompatResponsesChannelAlias(t *testing.T) {
	channel := service.NewChannelService(&openAIWSUsageHandlerChannelRepoStub{
		channels: []service.Channel{{ID: 1, Status: service.StatusActive, GroupIDs: []int64{8100},
			ModelMapping: map[string]map[string]string{service.PlatformAnthropic: {"public-opus": "claude-opus-5-5"}}}},
		groupPlatforms: map[int64]string{8100: service.PlatformAnthropic},
	}, nil, nil, nil, nil)
	f := newHandlerCompatFixture(t, channel)
	rec := httptest.NewRecorder()
	f.call("responses", strings.ReplaceAll(handlerCompatRequest("responses", false), "claude-opus-5-5", "public-opus"), rec)
	t.Logf("status=%d upstream_calls=%d", rec.Code, len(f.wire))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, f.wire, 1)
	require.Equal(t, "claude-opus-5-5", gjson.GetBytes((<-f.wire).body, "model").String())
}
