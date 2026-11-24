package native

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// CompleteInitialization records the controller's observed exclusive readiness.
// It does not fingerprint or certify Keycloak's schema.
func CompleteInitialization(ctx context.Context, conn *pgx.Conn, binding string) error {
	recovery, err := ReadRecovery(ctx, conn, binding)
	if err != nil {
		return err
	}
	if recovery != nil {
		if recovery.Phase != "NoBackup" || !recovery.InitializationStarted {
			return errors.New("first-start recovery claim is missing")
		}
		recovery.Phase = "Complete"
		return writeRecovery(ctx, conn, recovery)
	}
	var comment string
	if err := conn.QueryRow(ctx, "SELECT coalesce(shobj_description(oid,'pg_database'),'') FROM pg_database WHERE datname=current_database()").Scan(&comment); err != nil {
		return err
	}
	if comment != binding+":initialization-started" {
		return errors.New("first-start execution binding is missing")
	}
	return setDatabaseComment(ctx, conn, binding+":established")
}

func setDatabaseComment(ctx context.Context, conn *pgx.Conn, comment string) error {
	var name string
	if err := conn.QueryRow(ctx, "SELECT current_database()").Scan(&name); err != nil {
		return err
	}
	_, err := conn.Exec(ctx, "COMMENT ON DATABASE "+pgx.Identifier{name}.Sanitize()+" IS "+literal(comment))
	return err
}
