package native

import (
	"strings"
	"testing"
)

func TestRecoveryImageCompatibilityIsOneWayAndDigestBound(t *testing.T) {
	for _, tc := range []struct {
		source, target string
		valid          bool
	}{
		{UpgradeSourceImage, KeycloakImage, true},
		{KeycloakImage, KeycloakImage, true},
		{UpgradeSourceImage, UpgradeSourceImage, false},
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
