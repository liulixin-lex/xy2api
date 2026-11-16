//go:build unit

package schedquality

import (
	"testing"
	"time"
)

func TestPlacementAndOwnerMigration(t *testing.T) {
	rear := LineView{Identity: IdentityRear}
	trial := LineView{Identity: IdentityUnknown, Trial: true}
	for _, tc := range []struct {
		name    string
		view    StateView
		place   Placement
		slow    bool
		migrate bool
	}{
		{"unknown", StateView{}, PlaceFront, false, false},
		{"pass", StateView{TTFO: LineView{Identity: IdentityPass}}, PlaceFront, false, false},
		{"trial", StateView{TTFO: trial}, PlaceTrial, false, false},
		{"cache-rear-beats-trial", StateView{TTFO: trial, Cache: rear}, PlaceRear, true, false},
		{"error-rear", StateView{Error: rear}, PlaceRear, false, true},
		{"ttfo-owner-not-slow", StateView{TTFO: rear}, PlaceRear, false, false},
		{"ttfo-owner-slow-twice", StateView{TTFO: rear}, PlaceRear, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.view.Placement(true, machineNow); got != tc.place {
				t.Fatalf("place %d", got)
			}
			if got := tc.view.MigrateOwner(true, machineNow, tc.slow); got != tc.migrate {
				t.Fatalf("migration %v", got)
			}
			if tc.view.Placement(false, machineNow) != PlaceFront || tc.view.MigrateOwner(false, machineNow, true) {
				t.Fatal("disabled quality still applied")
			}
			v := tc.view
			v.AllOverride = OverrideRef{Mode: OverrideExempt}
			if v.Placement(true, machineNow) != PlaceFront || v.MigrateOwner(true, machineNow, true) {
				t.Fatal("exemption ignored")
			}
		})
	}
	// State is advanced by the engine, never implicitly expired by the view.
	v := StateView{Error: LineView{Identity: IdentityRear, Until: machineNow.Add(-time.Hour)}}
	if v.Placement(true, machineNow) != PlaceRear {
		t.Fatal("view released an unreviewed rear line")
	}
}

func TestEffectiveOverrideExpiryAndPriority(t *testing.T) {
	v := StateView{Error: LineView{Identity: IdentityRear}, GroupOverride: OverrideRef{Mode: OverrideExempt, Expires: machineNow}, AllOverride: OverrideRef{Mode: OverrideExempt, Expires: machineNow.Add(time.Hour)}}
	ref, all := v.EffectiveOverride(machineNow)
	if !all || ref != v.AllOverride {
		t.Fatal("expired group override masked account override")
	}
	v.GroupOverride.Expires = time.Time{}
	if ref, all = v.EffectiveOverride(machineNow); all || ref != v.GroupOverride {
		t.Fatal("group override not preferred")
	}
	v.GroupOverride.Expires = machineNow
	v.AllOverride.Expires = machineNow
	if v.Placement(true, machineNow) != PlaceRear || !v.MigrateOwner(true, machineNow, false) {
		t.Fatal("expiry boundary not exclusive")
	}
}
