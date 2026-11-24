package native

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestNativeStartupObservation(t *testing.T) {
	dsn := os.Getenv("KC_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("KC_TEST_POSTGRES_DSN must name a disposable PostgreSQL 17 administrator connection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	name := fmt.Sprintf("observation_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE ROLE "+identifier+" LOGIN"); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP ROLE "+identifier)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier+" OWNER "+identifier); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
	cfg := admin.Config().Copy()
	cfg.Database = name
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, "SET ROLE "+identifier); err != nil {
		t.Fatal(err)
	}
	const binding = "bartl-keycloak:qualification:instance:application:pvc"
	if _, err := conn.Exec(ctx, "COMMENT ON DATABASE "+identifier+" IS '"+binding+":initialization-started'"); err != nil {
		t.Fatal(err)
	}
	// Native initialization creates migration_model before update_time. The
	// retained execution claim, not queries into that unfinished table, controls admission.
	if _, err := conn.Exec(ctx, "CREATE TABLE migration_model (version text)"); err != nil {
		t.Fatal(err)
	}
	observed, err := Observe(ctx, conn, binding)
	if err != nil || observed.Classification != "Unverified" || observed.Reason != "NativeInitializationAdmitted" {
		t.Fatalf("admitted initialization must not query unfinished native schema: %+v, %v", observed, err)
	}
	// Completed historical metadata must work both before and after the supported
	// migration. The current version is also the ordinary 0.4.0 replacement path.
	if _, err := conn.Exec(ctx, "ALTER TABLE migration_model ADD COLUMN update_time bigint; INSERT INTO migration_model VALUES ('"+Version+"',1)"); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{UpgradeSourceVersion, Version} {
		t.Run("legacy-"+version, func(t *testing.T) {
			if _, err := conn.Exec(ctx, "UPDATE migration_model SET version=$1", version); err != nil {
				t.Fatal(err)
			}
			legacy := struct {
				RecoveryRecord
				SchemaFingerprint string `json:"schemaFingerprint"`
			}{
				RecoveryRecord: RecoveryRecord{
					OperationID: "operation", Phase: "Complete", Binding: binding,
					DatabaseIdentity: observed.Identity, Installation: "qualification", Instance: "instance",
					ApplicationUID: "application", PVCUID: "pvc", RepositoryID: strings.Repeat("a", 64),
					SnapshotID: strings.Repeat("b", 64), DumpSHA256: strings.Repeat("c", 64), DumpBytes: 123,
				},
				SchemaFingerprint: strings.Repeat("d", 32),
			}
			payload, err := json.Marshal(legacy)
			if err != nil {
				t.Fatal(err)
			}
			if err := setDatabaseComment(ctx, conn, binding+":recovery:"+string(payload)); err != nil {
				t.Fatal(err)
			}
			result, err := Observe(ctx, conn, binding)
			want := "Valid"
			if version == UpgradeSourceVersion {
				want = "Upgradeable"
			}
			if err != nil || result.Classification != want {
				t.Fatalf("completed historical state must permit native startup: %+v, %v", result, err)
			}
			converted, err := ReadRecovery(ctx, conn, binding)
			if err != nil || converted == nil || !converted.StartupStarted || converted.SnapshotID != legacy.SnapshotID {
				t.Fatalf("conversion must preserve the completed recovery identity: %+v, %v", converted, err)
			}
			legacy.Phase = "Restored"
			payload, err = json.Marshal(legacy)
			if err != nil {
				t.Fatal(err)
			}
			if err := setDatabaseComment(ctx, conn, binding+":recovery:"+string(payload)); err != nil {
				t.Fatal(err)
			}
			if _, err := Observe(ctx, conn, binding); err == nil {
				t.Fatal("ambiguous historical recovery must not become ordinary startup")
			}
		})
	}
	failed, cancelFailed := context.WithCancel(ctx)
	cancelFailed()
	if result, err := Observe(failed, conn, binding); err == nil || result.Classification == "Absent" {
		t.Fatalf("failed observation must not establish absence: %+v, %v", result, err)
	}
}
