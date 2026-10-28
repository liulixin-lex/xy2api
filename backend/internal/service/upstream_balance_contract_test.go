package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpstreamBalanceNewAPIParsingContract(t *testing.T) {
	for _, tc := range []struct {
		name, body, typ string
		want            float64
		ok              bool
	}{
		{"usd", `{"success":true,"data":{"quota_per_unit":100,"quota_display_type":"USD"}}`, "USD", 0, true},
		{"tokens", `{"success":true,"data":{"quota_per_unit":10,"quota_display_type":"TOKENS"}}`, "TOKENS", 0, true},
		{"fallback", `{"success":true,"data":{"quota_per_unit":10,"display_in_currency":false}}`, "TOKENS", 0, true},
		{"bad", `{"success":false}`, "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			site, ok := parseNewAPIStatus([]byte(tc.body))
			require.Equal(t, tc.ok, ok)
			if ok {
				require.Equal(t, tc.typ, site.DisplayType)
			}
		})
	}
}

func TestUpstreamBalanceTokenHeaderStyles(t *testing.T) {
	require.True(t, usableNewAPIToken("abc", ""))
	require.True(t, usableNewAPIToken("abc", "123"))
	require.False(t, usableNewAPIToken("a b", ""))
	require.False(t, usableNewAPIToken("abc", "bad"))
}

func TestUpstreamBalanceRetryAfterParsing(t *testing.T) {
	now := time.Unix(100, 0)
	for _, want := range []struct {
		value string
		d     time.Duration
	}{{"120", 120 * time.Second}, {"", 0}} {
		h := make(map[string][]string)
		if want.value != "" {
			h["Retry-After"] = []string{want.value}
		}
		got := parseRetryAfter(headerForTest(h), now)
		require.Equal(t, want.d, got)
	}
}

func TestUpstreamBalanceNegativeValuesPreserved(t *testing.T) {
	o, err := parseSub2APIUsage([]byte(`{"mode":"unrestricted","isValid":true,"balance":-3.2}`))
	require.NoError(t, err)
	require.NotNil(t, o)
	require.NotNil(t, o.amount)
	require.Equal(t, -3.2, *o.amount)
}

func TestUpstreamBalanceTransientBackoffCapsAtTenMinutes(t *testing.T) {
	st := &UpstreamBalanceState{}
	var got time.Duration
	for i := 0; i < 12; i++ {
		got = delayForOutcome(ubOutcome{reason: "upstream_error"}, st)
	}
	require.Equal(t, 10*time.Minute, got)
}

func TestUpstreamBalanceAuthBackoffCapsAtOneHour(t *testing.T) {
	st := &UpstreamBalanceState{}
	var got time.Duration
	for i := 0; i < 12; i++ {
		got = delayForOutcome(ubOutcome{reason: "auth_failed"}, st)
	}
	require.Equal(t, time.Hour, got)
}

func TestUpstreamBalanceUnsupportedBackoff(t *testing.T) {
	st := &UpstreamBalanceState{UnsupportedStreak: 3}
	require.Equal(t, 2*time.Hour, delayForOutcome(ubOutcome{reason: "unidentified"}, st))
}

func headerForTest(v map[string][]string) http.Header { return http.Header(v) }
