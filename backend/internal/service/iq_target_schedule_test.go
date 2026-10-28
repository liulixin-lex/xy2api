package service

import (
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
)

func TestIQTargetScheduleUsesNextSchedule(t *testing.T) {
	now := time.Unix(100, 0)
	s := iqcheck.NextSchedule(now, time.Minute, 0, 0, iqcheck.Result{Status: "degraded"}, 0)
	if s.NextRunAt == nil || !s.NextRunAt.After(now) {
		t.Fatal("degraded result must schedule a future retry")
	}
}
