//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type fallbackLiteGroups struct {
	service.GroupRepository
	groups map[int64]*service.Group
}

func (r *fallbackLiteGroups) GetByIDLite(_ context.Context, id int64) (*service.Group, error) {
	if g, ok := r.groups[id]; ok {
		cp := *g
		return &cp, nil
	}
	return nil, service.ErrGroupNotFound
}

func TestAvailableChannelGroupsShowAvailabilityFallback(t *testing.T) {
	orig := *resilience.Current()
	resilience.Publish(resilience.Defaults())
	t.Cleanup(func() { resilience.Publish(orig) })

	fb := int64(20)
	r := service.NewResilienceService(nil, nil)
	r.ConfigureFallbackGroups(nil, &fallbackLiteGroups{groups: map[int64]*service.Group{
		20: {ID: 20, Name: "Backup", Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 1.5},
	}})
	service.SetResilienceInstance(r)
	t.Cleanup(func() { service.SetResilienceInstance(nil) })

	userGroups := []service.Group{
		{ID: 10, Name: "Main", Platform: service.PlatformOpenAI, AvailabilityFallbackGroupID: &fb},
		{ID: 11, Name: "Plain", Platform: service.PlatformOpenAI},
	}
	visible := []userAvailableGroup{{ID: 10, Name: "Main"}, {ID: 11, Name: "Plain"}}
	attachAvailabilityFallbacks(visible, availabilityFallbacksOf(context.Background(), userGroups))
	require.Equal(t, &userFallbackGroup{ID: 20, Name: "Backup", RateMultiplier: 1.5}, visible[0].AvailabilityFallback)
	require.Nil(t, visible[1].AvailabilityFallback)

	raw, err := json.Marshal(visible)
	require.NoError(t, err)
	var decoded []map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, map[string]any{"id": float64(20), "name": "Backup", "rate_multiplier": 1.5}, decoded[0]["availability_fallback"])
	_, exists := decoded[1]["availability_fallback"]
	require.False(t, exists)

	// Without a resilience instance nothing is attached.
	service.SetResilienceInstance(nil)
	require.Empty(t, availabilityFallbacksOf(context.Background(), userGroups))
}
