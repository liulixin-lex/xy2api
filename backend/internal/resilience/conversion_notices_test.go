//go:build unit

package resilience

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
)

func TestFoundationConversionNotices(t *testing.T) {
	a := apicompat.ConversionNotice{Field: "stop", Action: "omit"}
	b := apicompat.ConversionNotice{Field: "reasoning.effort", Action: "map"}
	RecordConversionNotices(nil, a)
	RecordConversionNotices(context.Background(), a)
	if SnapshotConversionNotices(nil) != nil {
		t.Fatal("nil context")
	}
	s := &RequestState{}
	s.EnableGatewayRequest(time.Now())
	ctx := WithRequestState(context.Background(), s)
	RecordConversionNotices(ctx, a, b, a, apicompat.ConversionNotice{Field: "x", Action: "bad"}, apicompat.ConversionNotice{Action: "omit"})
	got := SnapshotConversionNotices(ctx)
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatal(got)
	}
	got[0].Field = "changed"
	if SnapshotConversionNotices(ctx)[0] != a {
		t.Fatal("snapshot aliases state")
	}
	_, _ = s.ClaimGatewayRequest(false)
	RecordConversionNotices(ctx, apicompat.ConversionNotice{Field: "late", Action: "default"})
	if len(SnapshotConversionNotices(ctx)) != 2 {
		t.Fatal("late mutation")
	}
}
