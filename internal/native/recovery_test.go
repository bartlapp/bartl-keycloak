package native

import (
	"strings"
	"testing"
)

func TestRecoveryRecordValidation(t *testing.T) {
	binding := "bartl-keycloak:installation:instance:application:pvc"
	restored := RecoveryRecord{
		OperationID: "operation", Phase: "Restored", Binding: binding,
		DatabaseIdentity: "16384", Installation: "installation", Instance: "instance",
		ApplicationUID: "application", PVCUID: "pvc", RepositoryID: strings.Repeat("a", 64),
		SnapshotID: strings.Repeat("b", 64), DumpSHA256: strings.Repeat("c", 64), DumpBytes: 123,
	}
	cases := []struct {
		name   string
		change func(*RecoveryRecord)
		valid  bool
	}{
		{"restored", func(r *RecoveryRecord) {}, true},
		{"complete", func(r *RecoveryRecord) { r.Phase = "Complete"; r.StartupStarted = true }, true},
		{"completionWithoutStartup", func(r *RecoveryRecord) { r.Phase = "Complete" }, false},
		{"restoredWithInitializationClaim", func(r *RecoveryRecord) { r.InitializationStarted = true }, false},
		{"foreignPVC", func(r *RecoveryRecord) { r.PVCUID = "foreign" }, false},
		{"unknownPhase", func(r *RecoveryRecord) { r.Phase = "Success" }, false},
		{"missingArchiveBytes", func(r *RecoveryRecord) { r.DumpBytes = 0 }, false},
		{"abbreviatedSnapshot", func(r *RecoveryRecord) { r.SnapshotID = "abcdef12" }, false},
		{"noBackupWithSnapshot", func(r *RecoveryRecord) { r.Phase = "NoBackup" }, false},
		{"noBackup", func(r *RecoveryRecord) { r.Phase = "NoBackup"; r.SnapshotID = ""; r.DumpSHA256 = ""; r.DumpBytes = 0 }, true},
		{"claimedNoBackup", func(r *RecoveryRecord) {
			r.Phase = "NoBackup"
			r.SnapshotID = ""
			r.DumpSHA256 = ""
			r.DumpBytes = 0
			r.InitializationStarted = true
		}, true},
		{"initializedComplete", func(r *RecoveryRecord) {
			r.Phase = "Complete"
			r.SnapshotID = ""
			r.DumpSHA256 = ""
			r.DumpBytes = 0
			r.InitializationStarted = true
		}, true},
		{"unclaimedEmptyComplete", func(r *RecoveryRecord) {
			r.Phase = "Complete"
			r.SnapshotID = ""
			r.DumpSHA256 = ""
			r.DumpBytes = 0
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := restored
			tc.change(&r)
			err := validateRecoveryRecord(&r, binding)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestRecoveryManifestPortableProvenance(t *testing.T) {
	destination := RecoveryRecord{
		Installation: "installation", Instance: "instance",
		ApplicationUID: "target-application", PVCUID: "target-pvc",
		Binding:      "bartl-keycloak:installation:instance:target-application:target-pvc",
		RepositoryID: strings.Repeat("a", 64), SnapshotID: strings.Repeat("b", 64),
	}
	postgresImage := "postgres:17.6@sha256:" + strings.Repeat("c", 64)
	manifest := captureManifest{
		BackupResult: BackupResult{
			FormatVersion: 1, Installation: "installation", Instance: "instance",
			ApplicationUID: "source-application", PVCUID: "source-pvc",
			OperationUID: "source-operation", PodUID: "source-pod", DatabaseIdentity: "16384",
			RepositoryID: strings.Repeat("d", 64), PackageRelease: "0.5.0",
			KeycloakImage: KeycloakImage, PostgresImage: postgresImage,
		},
		DatabaseBinding: "bartl-keycloak:installation:instance:source-application:source-pvc",
		Database:        "source_database", DatabaseUser: "source_user",
		CapturedAt: "2026-11-02T12:00:00Z", DumpFile: "keycloak.dump",
		DumpSHA256: strings.Repeat("e", 64), DumpBytes: 123,
	}
	originalID := strings.Repeat("f", 64)
	for _, tc := range []struct {
		name   string
		change func(*captureManifest, *recoverySnapshot)
		valid  bool
	}{
		{"copiedFormat1EmptySnapshot", func(*captureManifest, *recoverySnapshot) {}, true},
		{"sameRepository", func(m *captureManifest, _ *recoverySnapshot) { m.RepositoryID = destination.RepositoryID }, true},
		{"supportedForwardImage", func(m *captureManifest, _ *recoverySnapshot) { m.KeycloakImage = UpgradeSourceImage }, true},
		{"sourceSnapshotMatchesOriginal", func(m *captureManifest, _ *recoverySnapshot) { m.SnapshotID = originalID }, true},
		{"sourceSnapshotWithoutOriginal", func(m *captureManifest, s *recoverySnapshot) { s.Original = ""; m.SnapshotID = s.ID }, true},
		{"emptySnapshotWithoutOriginal", func(_ *captureManifest, s *recoverySnapshot) { s.Original = "" }, true},
		{"sourceSnapshotCannotUseCopiedID", func(m *captureManifest, s *recoverySnapshot) { m.SnapshotID = s.ID }, false},
		{"sourceSnapshotMismatch", func(m *captureManifest, _ *recoverySnapshot) { m.SnapshotID = strings.Repeat("1", 64) }, false},
		{"abbreviatedSourceSnapshot", func(m *captureManifest, _ *recoverySnapshot) { m.SnapshotID = originalID[:8] }, false},
		{"malformedOriginalEvenWithEmptyManifestSnapshot", func(_ *captureManifest, s *recoverySnapshot) { s.Original = "not-an-id" }, false},
		{"abbreviatedOriginal", func(_ *captureManifest, s *recoverySnapshot) { s.Original = originalID[:8] }, false},
		{"selectedSnapshotMismatch", func(_ *captureManifest, s *recoverySnapshot) { s.ID = strings.Repeat("1", 64) }, false},
		{"abbreviatedSelectedSnapshot", func(_ *captureManifest, s *recoverySnapshot) { s.ID = s.ID[:8] }, false},
		{"missingSourceRepository", func(m *captureManifest, _ *recoverySnapshot) { m.RepositoryID = "" }, false},
		{"abbreviatedSourceRepository", func(m *captureManifest, _ *recoverySnapshot) { m.RepositoryID = m.RepositoryID[:8] }, false},
		{"malformedSourceRepository", func(m *captureManifest, _ *recoverySnapshot) { m.RepositoryID = strings.Repeat("g", 64) }, false},
		{"foreignInstallation", func(m *captureManifest, _ *recoverySnapshot) {
			m.Installation = "foreign"
			m.DatabaseBinding = "bartl-keycloak:foreign:instance:source-application:source-pvc"
		}, false},
		{"foreignInstance", func(m *captureManifest, _ *recoverySnapshot) {
			m.Instance = "foreign"
			m.DatabaseBinding = "bartl-keycloak:installation:foreign:source-application:source-pvc"
		}, false},
		{"wrongSourceBinding", func(m *captureManifest, _ *recoverySnapshot) { m.DatabaseBinding = destination.Binding }, false},
		{"missingSourceApplication", func(m *captureManifest, _ *recoverySnapshot) { m.ApplicationUID = "" }, false},
		{"missingSourcePVC", func(m *captureManifest, _ *recoverySnapshot) { m.PVCUID = "" }, false},
		{"missingSourceDatabase", func(m *captureManifest, _ *recoverySnapshot) { m.DatabaseIdentity = "" }, false},
		{"missingSourceOperation", func(m *captureManifest, _ *recoverySnapshot) { m.OperationUID = "" }, false},
		{"missingSourcePod", func(m *captureManifest, _ *recoverySnapshot) { m.PodUID = "" }, false},
		{"unsupportedKeycloakImage", func(m *captureManifest, _ *recoverySnapshot) { m.KeycloakImage = "quay.io/keycloak/keycloak:26.6.2" }, false},
		{"unsupportedPostgresImage", func(m *captureManifest, _ *recoverySnapshot) { m.PostgresImage = "postgres:18" }, false},
		{"unsupportedFormat", func(m *captureManifest, _ *recoverySnapshot) { m.FormatVersion = 2 }, false},
		{"missingPackage", func(m *captureManifest, _ *recoverySnapshot) { m.PackageRelease = "" }, false},
		{"missingDatabaseName", func(m *captureManifest, _ *recoverySnapshot) { m.Database = "" }, false},
		{"missingDatabaseUser", func(m *captureManifest, _ *recoverySnapshot) { m.DatabaseUser = "" }, false},
		{"invalidCaptureTime", func(m *captureManifest, _ *recoverySnapshot) { m.CapturedAt = "yesterday" }, false},
		{"wrongArchiveName", func(m *captureManifest, _ *recoverySnapshot) { m.DumpFile = "foreign.dump" }, false},
		{"invalidChecksum", func(m *captureManifest, _ *recoverySnapshot) { m.DumpSHA256 = "bad" }, false},
		{"invalidArchiveSize", func(m *captureManifest, _ *recoverySnapshot) { m.DumpBytes = 0 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := manifest
			selected := recoverySnapshot{ID: destination.SnapshotID, Original: originalID}
			tc.change(&m, &selected)
			record := destination
			err := validateRecoveryManifest(m, &record, selected, KeycloakImage, postgresImage)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
			if record != destination {
				t.Fatalf("source evidence changed destination binding: got %+v, want %+v", record, destination)
			}
		})
	}
}
