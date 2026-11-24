package native

import (
	"strings"
	"testing"
)

func TestUpgradeAdmissionOnlyAcceptsExactForwardPair(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*UpgradeRecord)
		valid  bool
	}{
		{"admitted", func(*UpgradeRecord) {}, true},
		{"started", func(u *UpgradeRecord) { u.Started = true }, true},
		{"downgrade", func(u *UpgradeRecord) { u.From, u.To = Version, UpgradeSourceVersion }, false},
		{"unknownSource", func(u *UpgradeRecord) { u.From = "26.5.0" }, false},
		{"unknownTarget", func(u *UpgradeRecord) { u.To = "26.6.3" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &UpgradeRecord{From: UpgradeSourceVersion, To: Version}
			tc.mutate(u)
			if err := validateUpgrade(u); (err == nil) != tc.valid {
				t.Fatalf("validateUpgrade() = %v, valid = %v", err, tc.valid)
			}
		})
	}
}

func TestRecoveryImageCompatibilityIsOneWayAndDigestBound(t *testing.T) {
	for _, tc := range []struct {
		source, target string
		valid          bool
	}{
		{UpgradeSourceImage, KeycloakImage, true},
		{KeycloakImage, KeycloakImage, true},
		{UpgradeSourceImage, UpgradeSourceImage, true},
		{KeycloakImage, UpgradeSourceImage, false},
		{"quay.io/keycloak/keycloak:26.6.1", KeycloakImage, false},
		{UpgradeSourceImage, "quay.io/keycloak/keycloak:26.6.2", false},
		{"unknown@sha256:" + strings.Repeat("a", 64), KeycloakImage, false},
	} {
		if got := compatibleRecoveryImage(tc.source, tc.target); got != tc.valid {
			t.Errorf("compatibleRecoveryImage(%q, %q) = %v, want %v", tc.source, tc.target, got, tc.valid)
		}
	}
}
