package dto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountCapabilitySummaryAndPrivateExtra(t *testing.T) {
	expiry := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	digest := sha256.Sum256([]byte(`{"Platform":"openai","Type":"apikey","Credentials":null}`))
	fingerprint := hex.EncodeToString(digest[:])
	for _, tc := range []struct {
		name, fingerprint string
		expiry            time.Time
		want              int
	}{
		{"active", fingerprint, expiry, 2},
		{"expired", fingerprint, time.Now().Add(-time.Hour), 0},
		{"credentials changed", "old-fingerprint", expiry, 0},
		{"absent", "", time.Time{}, 0},
		{"cleared null", "", time.Time{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &service.Account{ID: 11, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
				Extra: map[string]any{"ordinary": "kept"}}
			if tc.fingerprint != "" {
				for _, protocol := range []string{"responses", "chat"} {
					src.Extra["capability_learned_"+protocol] = map[string]any{
						"expires_at": tc.expiry.Format(time.RFC3339Nano), "fingerprint": tc.fingerprint,
					}
				}
			}
			if tc.name == "cleared null" {
				require.NoError(t, json.Unmarshal([]byte(`{"ordinary":"kept","capability_learned_responses":null,"capability_learned_chat":null}`), &src.Extra))
			}
			before, err := json.Marshal(src.Extra)
			require.NoError(t, err)
			detail := AccountFromService(src)
			lite := AccountListItemFromAccount(detail)
			for _, dto := range []any{detail, lite} {
				raw, err := json.Marshal(dto)
				require.NoError(t, err)
				var response map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(raw, &response))
				require.Contains(t, response, "capability_learned")
				require.NotEqual(t, "null", string(response["capability_learned"]))
				var learned []struct {
					Protocol string    `json:"protocol"`
					Mode     string    `json:"mode"`
					Expiry   time.Time `json:"expires_at"`
				}
				require.NoError(t, json.Unmarshal(response["capability_learned"], &learned))
				require.Len(t, learned, tc.want)
				if tc.want > 0 {
					require.Equal(t, "responses", learned[0].Protocol)
					require.Equal(t, "chat", learned[1].Protocol)
					for _, record := range learned {
						require.Equal(t, "stream_only", record.Mode)
						require.True(t, expiry.Equal(record.Expiry))
					}
				}
				require.NotContains(t, string(raw), "fingerprint")
				require.NotContains(t, string(raw), "capability_learned_responses")
				require.NotContains(t, string(raw), "capability_learned_chat")
				require.JSONEq(t, `{"ordinary":"kept"}`, string(response["extra"]))
			}
			after, err := json.Marshal(src.Extra)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after), "mapping must not mutate the scheduler's account snapshot")
		})
	}
}
