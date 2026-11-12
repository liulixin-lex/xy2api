package admin

import (
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
)

type opsLimitsLogSink struct{ events []*logger.LogEvent }

func (s *opsLimitsLogSink) WriteLogEvent(event *logger.LogEvent) {
	s.events = append(s.events, event)
}

func TestOpsWSRuntimeLimitsInt32Bounds(t *testing.T) {
	require.NoError(t, logger.Init(logger.InitOptions{Level: "info"}))
	sink := &opsLimitsLogSink{}
	logger.SetSink(sink)
	t.Cleanup(func() { logger.SetSink(nil) })
	for _, tc := range []struct {
		value        string
		total, perIP int32
		errors       int
	}{
		{"", defaultMaxWSConns, defaultMaxWSConnsPerIP, 0},
		{"abc", defaultMaxWSConns, defaultMaxWSConnsPerIP, 2},
		{"-1", defaultMaxWSConns, defaultMaxWSConnsPerIP, 2},
		{"0", defaultMaxWSConns, 0, 1},
		{"1", 1, 1, 0},
		{"2147483647", 2147483647, 2147483647, 0},
		{"2147483648", defaultMaxWSConns, defaultMaxWSConnsPerIP, 2},
		{"4294967296", defaultMaxWSConns, defaultMaxWSConnsPerIP, 2},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv(envOpsWSMaxConns, tc.value)
			t.Setenv(envOpsWSMaxConnsPerIP, tc.value)
			sink.events = nil
			got := loadOpsWSRuntimeLimitsFromEnv()
			require.Equal(t, tc.total, got.MaxConns)
			require.Equal(t, tc.perIP, got.MaxConnsPerIP)
			require.Len(t, sink.events, tc.errors)
			for _, event := range sink.events {
				require.Equal(t, "error", event.Level)
				require.Contains(t, event.Message, "invalid OPS_WS_MAX_CONNS")
				require.Contains(t, event.Message, "using default=")
			}
		})
	}
}
