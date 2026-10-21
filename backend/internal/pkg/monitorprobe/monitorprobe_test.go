package monitorprobe

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	id, ok := Verify(Sign(42, "sk-site", now), "sk-site", now.Add(10*time.Second))
	if !ok || id != 42 {
		t.Fatalf("Verify = %d, %v; want 42, true", id, ok)
	}
}

func TestVerifyRejectsTamperingSkewAndOtherKeys(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	value := Sign(7, "sk-site", now)
	cases := map[string]struct {
		value, apiKey string
		at            time.Time
	}{
		"other monitor": {strings.Replace(value, "v1.7.", "v1.8.", 1), "sk-site", now},
		"bad mac":       {value[:len(value)-1] + "0", "sk-site", now},
		"too old":       {value, "sk-site", now.Add(61 * time.Second)},
		"from future":   {value, "sk-site", now.Add(-61 * time.Second)},
		"empty":         {"", "sk-site", now},
		"garbage":       {"v1.x.y.z", "sk-site", now},
		"wrong version": {strings.Replace(value, "v1.", "v2.", 1), "sk-site", now},
		"replayed by a third party with another key": {value, "sk-other", now},
		"no api key": {value, "", now},
	}
	for name, tc := range cases {
		if _, ok := Verify(tc.value, tc.apiKey, tc.at); ok {
			t.Fatalf("%s: Verify accepted %q", name, tc.value)
		}
	}
}

func TestContextMark(t *testing.T) {
	if IsProbe(context.Background()) || IsProbe(nil) { //nolint:staticcheck // nil ctx is part of the contract
		t.Fatal("unmarked context reported as probe")
	}
	if !IsProbe(WithProbe(context.Background(), 3)) {
		t.Fatal("marked context not reported as probe")
	}
}
