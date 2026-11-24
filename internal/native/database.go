package native

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

const Version = "26.6.2"

type Credentials struct{ Username, Password, Database string }
type Observation struct{ Classification, Reason, Identity, Version string }

func Connect(ctx context.Context, host string, c Credentials) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig("")
	if err != nil {
		return nil, err
	}
	cfg.Host = host
	cfg.Port = 5432
	cfg.User = c.Username
	cfg.Password = c.Password
	cfg.Database = c.Database
	cfg.TLSConfig = nil
	cfg.Fallbacks = nil
	cfg.ConnectTimeout = 5 * time.Second
	cfg.RuntimeParams["application_name"] = "bartl-keycloak"
	return pgx.ConnectConfig(ctx, cfg)
}

// Observe selects an owned empty target or native startup. It does not certify
// Keycloak's schema. An admitted initializer is identified before any model query.
func Observe(ctx context.Context, conn *pgx.Conn, binding string) (Observation, error) {
	var o Observation
	var server int
	if err := conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&server); err != nil {
		return o, err
	}
	if server < 170000 || server >= 180000 {
		return o, errors.New("PostgreSQL major version is not supported (requires 17)")
	}
	var ownedRole bool
	if err := conn.QueryRow(ctx, `SELECT d.datdba=r.oid AND r.rolcanlogin AND NOT (r.rolsuper OR r.rolcreaterole OR r.rolcreatedb OR r.rolreplication OR r.rolbypassrls) FROM pg_database d JOIN pg_roles r ON r.rolname=current_user WHERE d.datname=current_database()`).Scan(&ownedRole); err != nil {
		return o, err
	}
	if !ownedRole {
		return o, errors.New("application database must be owned by its unprivileged login role")
	}
	var comment string
	if err := conn.QueryRow(ctx, "SELECT coalesce(shobj_description(oid,'pg_database'),''),oid::text FROM pg_database WHERE datname=current_database()").Scan(&comment, &o.Identity); err != nil {
		return o, err
	}
	recovery, err := ReadRecovery(ctx, conn, binding)
	if err != nil {
		return o, err
	}
	upgrade, err := readUpgrade(ctx, conn, binding, recovery)
	if err != nil {
		return o, err
	}
	switch {
	case upgrade != nil:
		o.Classification, o.Reason = "Upgrading", "NativeUpgradeAdmitted"
		if upgrade.Started {
			o.Reason = "NativeUpgradeStarted"
		}
		return o, nil
	case recovery != nil && recovery.Phase == "Restored":
		o.Classification, o.Reason = "Restored", "NativeRestoreTransactionObserved"
		return o, nil
	case comment == binding+":initialization-started" || recovery != nil && recovery.Phase == "NoBackup" && recovery.InitializationStarted:
		o.Classification, o.Reason = "Unverified", "NativeInitializationAdmitted"
		return o, nil
	case recovery != nil && recovery.Phase == "NoBackup":
		o.Classification, o.Reason = "Absent", "EmptyRecoveryTarget"
		return o, nil
	case recovery != nil && recovery.Phase == "Complete", comment == binding+":established", legacyEstablished(comment, binding):
		// Only the native model version is needed to refuse unsupported downgrade.
	case comment == binding:
		var populated bool
		if err := conn.QueryRow(ctx, "SELECT EXISTS ("+recoveryObjectsSQL+")").Scan(&populated); err != nil {
			return o, err
		}
		if !populated {
			o.Classification, o.Reason = "Absent", "EmptyOwnedDatabase"
			return o, nil
		}
	default:
		return o, errors.New("database identity does not match the owned installation and PVC")
	}
	o.Version, err = nativeVersion(ctx, conn)
	if err != nil {
		return o, err
	}
	switch o.Version {
	case Version:
		o.Classification, o.Reason = "Valid", "NativeStartupAvailable"
	case UpgradeSourceVersion:
		o.Classification, o.Reason = "Upgradeable", "SupportedNativeUpgradeSource"
	default:
		o.Classification, o.Reason = "Incomplete", "UnsupportedOrIncompleteKeycloakVersion"
	}
	return o, nil
}

func nativeVersion(ctx context.Context, conn *pgx.Conn) (string, error) {
	var version string
	err := conn.QueryRow(ctx, "SELECT version FROM migration_model ORDER BY update_time DESC LIMIT 1").Scan(&version)
	return version, err
}

// Provision creates only absent owned objects. It never repairs/adopts a foreign role or database.
func Provision(ctx context.Context, host string, admin, app Credentials, binding string) error {
	conn, err := Connect(ctx, host, Credentials{Username: admin.Username, Password: admin.Password, Database: "postgres"})
	if err != nil {
		return errors.New("database administration connection unavailable")
	}
	defer conn.Close(ctx)
	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock(748920163)"); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock(748920163)")
	var dbExists, roleExists bool
	if err = conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1), EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$2)", app.Database, app.Username).Scan(&dbExists, &roleExists); err != nil {
		return err
	}
	if dbExists {
		return errors.New("existing database cannot be reached with application credentials; refusing mutation")
	}
	if roleExists {
		return errors.New("database role exists without its database; refusing partial or foreign state")
	}
	if _, err = conn.Exec(ctx, "CREATE ROLE "+pgx.Identifier{app.Username}.Sanitize()+" LOGIN PASSWORD "+literal(app.Password)); err != nil {
		return errors.New("native role creation failed")
	}
	if _, err = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{app.Database}.Sanitize()+" OWNER "+pgx.Identifier{app.Username}.Sanitize()); err != nil {
		return errors.New("native database creation failed; partial state retained")
	}
	if _, err = conn.Exec(ctx, "COMMENT ON DATABASE "+pgx.Identifier{app.Database}.Sanitize()+" IS "+literal(binding)); err != nil {
		return errors.New("database binding failed; partial state retained")
	}
	return nil
}
func literal(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// KeycloakStart admits a state-changing startup once. Established process
// restarts use kc.sh's supported migration/validation, without backup credentials.
func KeycloakStart() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := Credentials{Username: os.Getenv("KC_DB_USERNAME"), Password: os.Getenv("KC_DB_PASSWORD"), Database: os.Getenv("BARTL_DATABASE")}
	conn, err := Connect(ctx, os.Getenv("BARTL_DATABASE_HOST"), c)
	if err != nil {
		return errors.New("startup database connection unavailable")
	}
	defer conn.Close(ctx)
	binding := os.Getenv("BARTL_DATABASE_BINDING")
	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock(748920164)"); err != nil {
		return err
	}
	o, err := Observe(ctx, conn, binding)
	if err != nil {
		return fmt.Errorf("startup native observation refused: %w", err)
	}
	recovery, err := ReadRecovery(ctx, conn, binding)
	if err != nil {
		return err
	}
	initial, _ := strconv.ParseBool(os.Getenv("BARTL_ALLOW_EMPTY"))
	switch {
	case initial && o.Classification == "Absent":
		if os.Getenv("KC_BOOTSTRAP_ADMIN_USERNAME") == "" || os.Getenv("KC_BOOTSTRAP_ADMIN_PASSWORD") == "" {
			return errors.New("fresh initialization requires nonempty bootstrap credentials")
		}
		var populated bool
		if err = conn.QueryRow(ctx, "SELECT EXISTS ("+recoveryObjectsSQL+")").Scan(&populated); err != nil || populated {
			return errors.New("first-start target is no longer confirmed empty")
		}
		claim := binding + ":initialization-started"
		if recovery != nil {
			if recovery.OperationID != os.Getenv("BARTL_RECOVERY_ID") || recovery.Phase != "NoBackup" || recovery.InitializationStarted {
				return errors.New("first-start recovery claim does not match native operation")
			}
			recovery.InitializationStarted = true
			claim = recoveryComment(recovery)
		}
		if _, err = conn.Exec(ctx, "COMMENT ON DATABASE "+pgx.Identifier{c.Database}.Sanitize()+" IS "+literal(claim)); err != nil {
			return err
		}
	case o.Classification == "Restored":
		if recovery.OperationID != os.Getenv("BARTL_RECOVERY_ID") || recovery.StartupStarted {
			return errors.New("recovery startup already attempted; refusing replay")
		}
		recovery.StartupStarted = true
		if err = writeRecovery(ctx, conn, recovery); err != nil {
			return err
		}
	case o.Classification == "Upgrading" && os.Getenv("BARTL_ALLOW_UPGRADE") == "true":
		u, err := readUpgrade(ctx, conn, binding, recovery)
		if err != nil {
			return err
		}
		if u.Started {
			return errors.New("native migration already attempted; refusing replay")
		}
		u.Started = true
		if err = writeUpgrade(ctx, conn, binding, recovery, u); err != nil {
			return err
		}
	case o.Classification == "Valid":
	default:
		return fmt.Errorf("startup refused: %s (%s)", o.Classification, o.Reason)
	}
	if o.Classification != "Absent" {
		os.Setenv("KC_SPI_CONNECTIONS_JPA__QUARKUS__INITIALIZE_EMPTY", "false")
		os.Unsetenv("KC_BOOTSTRAP_ADMIN_USERNAME")
		os.Unsetenv("KC_BOOTSTRAP_ADMIN_PASSWORD")
	}
	// Releasing this connection before exec leaves no custom supervisor around kc.sh.
	conn.Close(ctx)
	return syscall.Exec("/opt/keycloak/bin/kc.sh", []string{"/opt/keycloak/bin/kc.sh", "start"}, os.Environ())
}
