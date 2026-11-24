package native

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

const UpgradeSourceVersion = "26.6.1"
const UpgradeSourceImage = "quay.io/keycloak/keycloak:26.6.1@sha256:dea26401d06341095cc4ea9d66896200b55de5ca1daa1d2fcbe58493afa6e0ad"
const KeycloakImage = "quay.io/keycloak/keycloak:26.6.2@sha256:f9ba7b2af90db8dc749a57ca9aedca51e840cb9224441ab546a968da941da900"

// UpgradeRecord is a one-way execution admission, never schema certification.
type UpgradeRecord struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Started bool   `json:"started"`
}

// legacyEstablished recognizes only the historical input metadata format. Its
// digest is deliberately not compared to the schema; admission replaces it.
func legacyEstablished(comment, binding string) bool {
	prefix := binding + ":schema:"
	if !strings.HasPrefix(comment, prefix) {
		return false
	}
	value := strings.TrimPrefix(comment, prefix)
	_, err := hex.DecodeString(value)
	return len(value) == 32 && err == nil
}

func readUpgrade(ctx context.Context, conn *pgx.Conn, binding string, recovery *RecoveryRecord) (*UpgradeRecord, error) {
	if recovery != nil {
		return recovery.Upgrade, validateUpgrade(recovery.Upgrade)
	}
	var comment string
	if err := conn.QueryRow(ctx, "SELECT coalesce(shobj_description(oid,'pg_database'),'') FROM pg_database WHERE datname=current_database()").Scan(&comment); err != nil {
		return nil, err
	}
	prefix := binding + ":upgrade:"
	if !strings.HasPrefix(comment, prefix) {
		return nil, nil
	}
	var u UpgradeRecord
	decoder := json.NewDecoder(strings.NewReader(strings.TrimPrefix(comment, prefix)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&u); err != nil {
		return nil, errors.New("native upgrade admission is malformed")
	}
	data, _ := json.Marshal(&u)
	if comment != prefix+string(data) {
		return nil, errors.New("native upgrade admission is not canonical")
	}
	return &u, validateUpgrade(&u)
}

func validateUpgrade(u *UpgradeRecord) error {
	if u != nil && (u.From != UpgradeSourceVersion || u.To != Version) {
		return errors.New("native upgrade admission is incompatible with this package")
	}
	return nil
}

func writeUpgrade(ctx context.Context, conn *pgx.Conn, binding string, recovery *RecoveryRecord, u *UpgradeRecord) error {
	if recovery != nil {
		recovery.Upgrade = u
		return writeRecovery(ctx, conn, recovery)
	}
	data, err := json.Marshal(u)
	if err != nil {
		return err
	}
	return setDatabaseComment(ctx, conn, binding+":upgrade:"+string(data))
}

// AdmitUpgrade is called under native exclusion after every old Pod is gone.
func AdmitUpgrade(ctx context.Context, conn *pgx.Conn, binding string) error {
	o, err := Observe(ctx, conn, binding)
	if err != nil {
		return err
	}
	if o.Classification != "Upgradeable" {
		return errors.New("upgrade requires the supported native source version")
	}
	recovery, err := ReadRecovery(ctx, conn, binding)
	if err != nil {
		return err
	}
	return writeUpgrade(ctx, conn, binding, recovery, &UpgradeRecord{From: UpgradeSourceVersion, To: Version})
}

// CompleteUpgrade is called only for the admitted, uninterrupted ready process.
func CompleteUpgrade(ctx context.Context, conn *pgx.Conn, binding string) error {
	version, err := nativeVersion(ctx, conn)
	if err != nil {
		return err
	}
	if version != Version {
		return errors.New("ready startup did not reach the supported target version")
	}
	recovery, err := ReadRecovery(ctx, conn, binding)
	if err != nil {
		return err
	}
	u, err := readUpgrade(ctx, conn, binding, recovery)
	if err != nil {
		return err
	}
	if u == nil || !u.Started {
		return errors.New("native upgrade execution claim is missing")
	}
	if recovery != nil {
		recovery.Upgrade = nil
		return writeRecovery(ctx, conn, recovery)
	}
	return setDatabaseComment(ctx, conn, binding+":established")
}

func compatibleRecoveryImage(source, target string) bool {
	return source == target || source == UpgradeSourceImage && target == KeycloakImage
}
