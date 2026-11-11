//go:build unit

package schedtest

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

func TestSchedTestBuild(t *testing.T) {
	count := func(b []byte) int { return len(b) / 4 }
	for tier := schedquality.TierSmall; tier <= schedquality.TierLarge; tier++ {
		session := Session{ID: "test-one", Model: "gpt-6.1-sol"}
		var previous []byte
		var key string
		previousCount := 0
		for turn := 1; turn <= 3; turn++ {
			body := Build(tier, turn, session, count)
			var got struct {
				Input     json.RawMessage `json:"input"`
				Key       string          `json:"prompt_cache_key"`
				Stream    bool            `json:"stream"`
				Store     bool            `json:"store"`
				Reasoning struct {
					Effort  string `json:"effort"`
					Summary string `json:"summary"`
				} `json:"reasoning"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if !got.Stream || got.Store || got.Reasoning.Summary != "auto" || got.Reasoning.Effort != schedquality.TestTiers[tier].Effort {
				t.Fatal("invalid request settings")
			}
			if turn == 1 {
				n := count(body)
				target := schedquality.TestTiers[tier].Tokens
				if float64(abs(n-target)) > float64(target)*schedquality.TestPromptTolerance {
					t.Fatalf("tier %s: %d vs %d", tier, n, target)
				}
				key = got.Key
			} else if got.Key != key || !bytes.HasPrefix(got.Input, previous[:len(previous)-1]) {
				t.Fatal("cache key or input prefix changed")
			}
			if turn > 1 && float64(abs(count(body)-previousCount-schedquality.TestTurnGrowthTokens)) > float64(schedquality.TestTurnGrowthTokens)*schedquality.TestPromptTolerance {
				t.Fatalf("tier %s turn %d: growth=%d", tier, turn, count(body)-previousCount)
			}
			previousCount = count(body)
			previous = append([]byte(nil), got.Input...)
			session.Input = got.Input
			session.Answer = "Example response"
		}
		other := Build(tier, 1, Session{ID: "test-two", Model: session.Model}, count)
		if bytes.Contains(other, []byte(key)) {
			t.Fatal("cache key reused")
		}
	}
}
