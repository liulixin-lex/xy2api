package service

import (
	"encoding/json"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
)

func TestIQProbePayloadCarriesCanonicalModel(t *testing.T) {
	c := iqcheck.NewChallenge()
	b, err := json.Marshal(iqcheck.Payload(false, "gpt-6.1-sol", "low", c))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err = json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if v["model"] != "gpt-6.1-sol" {
		t.Fatalf("model=%v", v["model"])
	}
}
