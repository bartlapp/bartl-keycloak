package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetainedCaptureProtectsBytesAndOwnership(t *testing.T) {
	body := []byte("consistent native archive bytes")
	hash := sha256.Sum256(body)
	expected := BackupResult{FormatVersion: 1, Installation: "installation", Instance: "keycloak", ApplicationUID: "application", PVCUID: "database-volume", DatabaseIdentity: "123", RepositoryID: strings.Repeat("a", 64), PackageRelease: "candidate", KeycloakImage: KeycloakImage, PostgresImage: "postgres@sha256:" + strings.Repeat("b", 64)}
	const binding = "bartl-keycloak:installation:keycloak:application:database-volume"
	for _, tc := range []struct {
		name   string
		mutate func(*captureManifest, *[]byte)
		valid  bool
	}{
		{"publication-retry", func(*captureManifest, *[]byte) {}, true},
		{"corrupt-archive", func(_ *captureManifest, b *[]byte) { (*b)[0] ^= 1 }, false},
		{"truncated-archive", func(_ *captureManifest, b *[]byte) { *b = (*b)[:3] }, false},
		{"foreign-application", func(m *captureManifest, _ *[]byte) { m.ApplicationUID = "other" }, false},
		{"foreign-volume", func(m *captureManifest, _ *[]byte) { m.PVCUID = "other" }, false},
		{"redirected-repository", func(m *captureManifest, _ *[]byte) { m.RepositoryID = strings.Repeat("c", 64) }, false},
		{"changed-native-data", func(m *captureManifest, _ *[]byte) { m.KeycloakImage = UpgradeSourceImage }, false},
		{"missing-database", func(m *captureManifest, _ *[]byte) { m.Database = "" }, false},
		{"missing-database-user", func(m *captureManifest, _ *[]byte) { m.DatabaseUser = "" }, false},
		{"missing-capture-time", func(m *captureManifest, _ *[]byte) { m.CapturedAt = "" }, false},
		{"malformed-capture-time", func(m *captureManifest, _ *[]byte) { m.CapturedAt = "not-a-time" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			m := captureManifest{BackupResult: expected, DatabaseBinding: binding, DumpFile: "keycloak.dump", DumpSHA256: hex.EncodeToString(hash[:]), DumpBytes: int64(len(body))}
			m.OperationUID, m.PodUID = "previous-operation", "previous-pod"
			m.Database, m.DatabaseUser, m.CapturedAt = "keycloak", "keycloak", "2026-11-26T00:00:00Z"
			archive := append([]byte(nil), body...)
			tc.mutate(&m, &archive)
			data, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "keycloak.dump"), archive, 0600); err != nil {
				t.Fatal(err)
			}
			if err := persistCaptureManifest(dir, data); err != nil {
				t.Fatal(err)
			}
			got, _, err := readRetainedCapture(context.Background(), dir, expected, binding)
			if (err == nil) != tc.valid {
				t.Fatalf("retained capture accepted=%v, want %v: %v", err == nil, tc.valid, err)
			}
			if tc.valid && (got.OperationUID != "previous-operation" || got.DumpSHA256 != m.DumpSHA256) {
				t.Fatal("retry lost original capture provenance")
			}
			retained, err := os.ReadFile(filepath.Join(dir, "keycloak.dump"))
			if err != nil || string(retained) != string(archive) {
				t.Fatal("validation changed retained evidence")
			}
		})
	}
}

func TestCaptureInterruptionReclaimsOnlyIneligibleWork(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{".capture-interrupted", ".retired", "pending", "diagnostics"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "keycloak.dump"), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := reclaimCaptureWork(root); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".capture-interrupted", ".retired"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("abandoned %s was not reclaimed: %v", name, err)
		}
	}
	for _, name := range []string{"pending", "diagnostics"} {
		data, err := os.ReadFile(filepath.Join(root, name, "keycloak.dump"))
		if err != nil || string(data) != name {
			t.Fatalf("reclamation changed %s", name)
		}
	}
	if err := retireCapture(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "pending")); !os.IsNotExist(err) {
		t.Fatal("verified capture remained eligible for publication retry")
	}
}
