package native

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// RecoveryRecord binds a native transaction outcome to one physical target.
// Source database identities remain in the independently verified manifest.
type RecoveryRecord struct {
	OperationID           string         `json:"operationID"`
	Phase                 string         `json:"phase"`
	Binding               string         `json:"binding"`
	DatabaseIdentity      string         `json:"databaseIdentity"`
	Installation          string         `json:"installation"`
	Instance              string         `json:"instance"`
	ApplicationUID        string         `json:"applicationUID"`
	PVCUID                string         `json:"pvcUID"`
	RepositoryID          string         `json:"repositoryID"`
	SnapshotID            string         `json:"snapshotID"`
	DumpSHA256            string         `json:"dumpSHA256"`
	DumpBytes             int64          `json:"dumpBytes"`
	StartupStarted        bool           `json:"startupStarted,omitempty"`
	InitializationStarted bool           `json:"initializationStarted,omitempty"`
	Upgrade               *UpgradeRecord `json:"upgrade,omitempty"`
}

func recoveryComment(r *RecoveryRecord) string {
	data, _ := json.Marshal(r)
	return r.Binding + ":recovery:" + string(data)
}

func validateRecoveryRecord(r *RecoveryRecord, binding string) error {
	if err := validateUpgrade(r.Upgrade); err != nil {
		return err
	}
	if r.Upgrade != nil && r.Phase != "Restored" && r.Phase != "Complete" {
		return errors.New("recovery upgrade requires restored or complete native state")
	}
	if r.Binding != binding || r.Binding != fmt.Sprintf("bartl-keycloak:%s:%s:%s:%s", r.Installation, r.Instance, r.ApplicationUID, r.PVCUID) {
		return errors.New("recovery binding does not match the current target")
	}
	for _, v := range []string{r.OperationID, r.Installation, r.Instance, r.ApplicationUID, r.PVCUID, r.DatabaseIdentity} {
		if v == "" || strings.ContainsAny(v, "\x00\r\n,") {
			return errors.New("recovery identity is malformed")
		}
	}
	if !backupFullID(r.RepositoryID) {
		return errors.New("recovery repository identity is malformed")
	}
	switch r.Phase {
	case "Restored":
		if r.InitializationStarted {
			return errors.New("restored recovery contains an initialization claim")
		}
	case "NoBackup":
		if r.SnapshotID != "" || r.DumpSHA256 != "" || r.DumpBytes != 0 || r.StartupStarted {
			return errors.New("no-backup record contains unexpected snapshot evidence")
		}
		return nil
	case "Complete":
		if r.InitializationStarted && r.SnapshotID == "" && r.DumpSHA256 == "" && r.DumpBytes == 0 {
			return nil
		}
		if r.InitializationStarted {
			return errors.New("initialized recovery cannot contain a restored snapshot")
		}
		if !r.StartupStarted {
			return errors.New("completed restore has no startup admission")
		}
	default:
		return errors.New("recovery phase is not supported")
	}
	if !backupFullID(r.SnapshotID) || !backupFullID(r.DumpSHA256) || r.DumpBytes <= 0 {
		return errors.New("recovery snapshot evidence is malformed")
	}
	return nil
}

// ReadRecovery recognizes recovery metadata only after the complete target binding.
// Observe validates all ordinary or foreign database comments.
func ReadRecovery(ctx context.Context, conn *pgx.Conn, binding string) (*RecoveryRecord, error) {
	var comment, identity string
	if err := conn.QueryRow(ctx, "SELECT coalesce(shobj_description(oid,'pg_database'),''),oid::text FROM pg_database WHERE datname=current_database()").Scan(&comment, &identity); err != nil {
		return nil, err
	}
	prefix := binding + ":recovery:"
	if !strings.HasPrefix(comment, prefix) {
		return nil, nil
	}
	// Convert completed historical input metadata once. No live catalog digest is
	// computed or compared; pending/ambiguous historical operations are refused.
	payload := strings.TrimPrefix(comment, prefix)
	var fields map[string]json.RawMessage
	converted := false
	if json.Unmarshal([]byte(payload), &fields) == nil && fields["schemaFingerprint"] != nil {
		var digest, phase string
		if json.Unmarshal(fields["schemaFingerprint"], &digest) != nil || json.Unmarshal(fields["phase"], &phase) != nil || phase != "Complete" || len(digest) != 32 {
			return nil, errors.New("historical recovery input is not completed metadata")
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return nil, errors.New("historical recovery input digest is malformed")
		}
		version, err := nativeVersion(ctx, conn)
		if err != nil || version != UpgradeSourceVersion && version != Version {
			return nil, errors.New("historical recovery conversion requires a supported native version")
		}
		delete(fields, "schemaFingerprint")
		if string(fields["initializationStarted"]) != "true" {
			fields["startupStarted"] = json.RawMessage("true")
		}
		data, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		payload, converted = string(data), true
	}
	var r RecoveryRecord
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return nil, errors.New("recovery comment is malformed")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("recovery comment has trailing content")
	}
	if err := validateRecoveryRecord(&r, binding); err != nil {
		return nil, err
	}
	if r.DatabaseIdentity != identity {
		return nil, errors.New("recovery native database identity changed")
	}
	if !converted && recoveryComment(&r) != comment {
		return nil, errors.New("recovery comment is not a canonical native record")
	}
	if converted {
		if err := writeRecovery(ctx, conn, &r); err != nil {
			return nil, err
		}
	}
	return &r, nil
}

// CompleteRecovery follows exclusive startup readiness.
func CompleteRecovery(ctx context.Context, conn *pgx.Conn, binding, operationID string) error {
	r, err := ReadRecovery(ctx, conn, binding)
	if err != nil {
		return err
	}
	if r == nil || r.OperationID != operationID || r.Phase != "Restored" || !r.StartupStarted {
		return errors.New("recovery completion does not match an admitted startup")
	}
	version, err := nativeVersion(ctx, conn)
	if err != nil {
		return err
	}
	if version != Version {
		return errors.New("recovery startup did not reach the supported target version")
	}
	r.Upgrade = nil
	r.Phase = "Complete"
	return writeRecovery(ctx, conn, r)
}

func writeRecovery(ctx context.Context, conn *pgx.Conn, r *RecoveryRecord) error {
	var database string
	if err := conn.QueryRow(ctx, "SELECT current_database()").Scan(&database); err != nil {
		return err
	}
	_, err := conn.Exec(ctx, "COMMENT ON DATABASE "+pgx.Identifier{database}.Sanitize()+" IS "+literal(recoveryComment(r)))
	return err
}

// Recovery reads backup contents and creates/removes native Restic locks. Every
// target mutation rechecks the exact native binding under database exclusion;
// restore exclusion belongs to the transaction that loads SQL and records outcome.
func Recovery(parent context.Context, output io.Writer) error {
	ctx, cancel := context.WithTimeout(parent, 25*time.Minute)
	defer cancel()
	r := RecoveryRecord{OperationID: os.Getenv("BARTL_RECOVERY_ID"), Binding: os.Getenv("BARTL_DATABASE_BINDING"), Installation: os.Getenv("BARTL_INSTALLATION"), Instance: os.Getenv("BARTL_INSTANCE"), ApplicationUID: os.Getenv("BARTL_APPLICATION_UID"), PVCUID: os.Getenv("BARTL_PVC_UID")}
	for _, v := range []string{r.OperationID, r.Installation, r.Instance, r.ApplicationUID, r.PVCUID, os.Getenv("BARTL_PACKAGE_RELEASE"), os.Getenv("BARTL_KEYCLOAK_IMAGE"), os.Getenv("BARTL_POSTGRES_IMAGE")} {
		if v == "" || strings.ContainsAny(v, "\x00\r\n,") {
			return errors.New("recovery requires exact nonempty operation and native image identities")
		}
	}
	if r.Binding != fmt.Sprintf("bartl-keycloak:%s:%s:%s:%s", r.Installation, r.Instance, r.ApplicationUID, r.PVCUID) {
		return errors.New("recovery database binding does not match target inputs")
	}
	host := os.Getenv("PGHOST")
	c := Credentials{Username: os.Getenv("PGUSER"), Password: os.Getenv("PGPASSWORD"), Database: os.Getenv("PGDATABASE")}
	for _, v := range []string{host, c.Username, c.Password, c.Database, os.Getenv("RESTIC_PASSWORD"), os.Getenv("RESTIC_REST_USERNAME"), os.Getenv("RESTIC_REST_PASSWORD")} {
		if v == "" || strings.ContainsRune(v, 0) {
			return errors.New("recovery requires native database and reader credentials")
		}
	}
	if strings.ContainsAny(host, "\r\n\x00,/") {
		return errors.New("recovery database host must be a single network host")
	}
	repository := os.Getenv("RESTIC_REPOSITORY")
	u, err := url.Parse(strings.TrimPrefix(repository, "rest:"))
	if err != nil || !strings.HasPrefix(repository, "rest:https://") || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(repository, "\r\n\x00") {
		return errors.New("recovery repository must be an HTTPS REST URL without embedded credentials, query or fragment")
	}
	conn, err := connectBackupDatabase(ctx, host, c)
	if err != nil {
		return errors.New("recovery database connection unavailable")
	}
	defer conn.Close(context.Background())
	observed, err := Observe(ctx, conn, r.Binding)
	if err != nil {
		return fmt.Errorf("recovery target observation refused: %w", err)
	}
	r.DatabaseIdentity = observed.Identity
	if current, err := ReadRecovery(ctx, conn, r.Binding); err != nil {
		return err
	} else if current != nil {
		return errors.New("recovery already attempted on this target; refusing replay")
	}
	if observed.Classification != "Absent" {
		return errors.New("recovery requires a confirmed empty owned target")
	}
	dir, err := os.MkdirTemp("", "bartl-keycloak-recovery-")
	if err != nil {
		return errors.New("private recovery temporary directory unavailable")
	}
	defer os.RemoveAll(dir)
	base := []string{"PATH=/usr/local/bin:/usr/lib/postgresql/17/bin:/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir}
	restEnv := append(append([]string{}, base...), "RESTIC_REPOSITORY="+repository, "RESTIC_PASSWORD="+os.Getenv("RESTIC_PASSWORD"), "RESTIC_REST_USERNAME="+os.Getenv("RESTIC_REST_USERNAME"), "RESTIC_REST_PASSWORD="+os.Getenv("RESTIC_REST_PASSWORD"))
	if info, e := os.Stat("/repository-ca/ca.crt"); e == nil {
		if !info.Mode().IsRegular() {
			return errors.New("repository CA must be a regular file")
		}
		restEnv = append(restEnv, "RESTIC_CACERT=/repository-ca/ca.crt")
	} else if !errors.Is(e, os.ErrNotExist) {
		return errors.New("repository CA unavailable")
	}
	restic := func(args ...string) *exec.Cmd {
		return backupCommand(ctx, dir, restEnv, "restic", append([]string{"--no-cache", "--json"}, args...)...)
	}
	var config bytes.Buffer
	if err := runBackupCommand(restic("cat", "config"), &limitedBackupWriter{Writer: &config, Remaining: 1 << 20}); err != nil {
		return errors.New("recovery repository config could not be read")
	}
	var cfg struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	if json.Unmarshal(config.Bytes(), &cfg) != nil || !backupFullID(cfg.ID) || (cfg.Version != 1 && cfg.Version != 2) {
		return errors.New("recovery repository identity or version is invalid")
	}
	r.RepositoryID = cfg.ID
	var snapshots, diagnostics bytes.Buffer
	cmd := restic("snapshots", "--tag", "installation="+r.Installation+",instance="+r.Instance)
	cmd.Stderr = &limitedBackupWriter{Writer: &diagnostics, Remaining: 64 << 10}
	// Restic can exit successfully with a partial list after snapshot read errors.
	if err := runBackupCommand(cmd, &limitedBackupWriter{Writer: &snapshots, Remaining: 32 << 20}); err != nil || diagnostics.Len() != 0 {
		return errors.New("recovery snapshot lookup failed")
	}
	var list []recoverySnapshot
	if json.Unmarshal(snapshots.Bytes(), &list) != nil {
		return errors.New("recovery snapshot lookup returned malformed output")
	}
	matches := list[:0]
	for _, s := range list {
		if !hasSnapshotTag(s.Tags, "installation="+r.Installation) || !hasSnapshotTag(s.Tags, "instance="+r.Instance) {
			continue
		}
		if !backupFullID(s.ID) || s.Time.IsZero() || s.Hostname != r.Installation {
			return errors.New("matching recovery snapshot identity is malformed")
		}
		if s.Original != "" && !backupFullID(s.Original) {
			return errors.New("matching recovery snapshot original identity is malformed")
		}
		matches = append(matches, s)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Time.Equal(matches[j].Time) {
			return matches[i].ID > matches[j].ID
		}
		return matches[i].Time.After(matches[j].Time)
	})
	if len(matches) == 0 {
		r.Phase = "NoBackup"
	} else {
		r.SnapshotID = matches[0].ID
		r.Phase = "Restored"
	}
	dumpPath := filepath.Join(dir, "keycloak.dump")
	if r.SnapshotID != "" {
		if err := verifyRecoverySnapshot(ctx, restic, base, dir, dumpPath, &r, matches[0]); err != nil {
			return err
		}
	}
	if r.Phase == "NoBackup" {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(748920164)"); err != nil {
			return err
		}
		var empty bool
		if err = tx.QueryRow(ctx, "SELECT oid::text=$1 AND coalesce(shobj_description(oid,'pg_database'),'')=$2 AND NOT EXISTS ("+recoveryObjectsSQL+") FROM pg_database WHERE datname=current_database()", r.DatabaseIdentity, r.Binding).Scan(&empty); err != nil || !empty {
			return errors.New("no-backup target changed during lookup")
		}
		if _, err = tx.Exec(ctx, "COMMENT ON DATABASE "+pgx.Identifier{c.Database}.Sanitize()+" IS "+literal(recoveryComment(&r))); err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return errors.New("no-backup commit outcome unknown; refusing retry")
		}
		return json.NewEncoder(output).Encode(&r)
	}
	sqlPath := filepath.Join(dir, "restore.sql")
	// pg_restore is the only SQL generator; there is no custom archive/SQL engine.
	if err := runBackupCommand(backupCommand(ctx, dir, base, "pg_restore", "--exit-on-error", "--no-owner", "--no-acl", "--file="+sqlPath, dumpPath), io.Discard); err != nil {
		return errors.New("native restore SQL generation failed")
	}
	if err := os.Chmod(sqlPath, 0600); err != nil {
		return errors.New("native restore SQL permissions could not be restricted")
	}
	scriptPath := filepath.Join(dir, "transaction.sql")
	script, err := os.OpenFile(scriptPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("private restore transaction unavailable")
	}
	expectedComment := r.Binding
	guardBody := "BEGIN IF (SELECT oid::text <> " + literal(r.DatabaseIdentity) + " OR coalesce(shobj_description(oid,'pg_database'),'') <> " + literal(expectedComment) + " FROM pg_database WHERE datname=current_database()) OR EXISTS (" + recoveryObjectsSQL + ") THEN RAISE EXCEPTION 'restore target binding or empty state changed'; END IF; END"
	guard := "SET LOCAL lock_timeout='5s'; SELECT pg_advisory_xact_lock(748920164); SET LOCAL lock_timeout=0; DO " + literal(guardBody) + ";\n"
	_, err = io.WriteString(script, guard+"\\ir restore.sql\nCOMMENT ON DATABASE "+pgx.Identifier{c.Database}.Sanitize()+" IS "+literal(recoveryComment(&r))+";\n")
	closeErr := script.Close()
	if err != nil || closeErr != nil {
		return errors.New("native restore transaction assembly failed")
	}
	quote := func(s string) string { return "'" + strings.NewReplacer("\\", "\\\\", "'", "\\'").Replace(s) + "'" }
	dsn := "host=" + quote(host) + " port=5432 user=" + quote(c.Username) + " dbname=" + quote(c.Database) + " sslmode=disable connect_timeout=5"
	pgEnv := append(append([]string{}, base...), "PGPASSWORD="+c.Password)
	if err := runBackupCommand(backupCommand(ctx, dir, pgEnv, "psql", "--no-psqlrc", "--no-password", "--dbname="+dsn, "--single-transaction", "--set=ON_ERROR_STOP=1", "--file="+scriptPath), io.Discard); err != nil {
		return errors.New("native restore failed or acknowledgement lost; discard this target before retry")
	}
	result, err := ReadRecovery(ctx, conn, r.Binding)
	if err != nil {
		return err
	}
	final, err := Observe(ctx, conn, r.Binding)
	if err != nil {
		return err
	}
	if result == nil || *result != r || final.Classification != "Restored" {
		return errors.New("native restore result does not match committed schema and operation")
	}
	return json.NewEncoder(output).Encode(result)
}

type recoverySnapshot struct {
	ID       string    `json:"id"`
	Original string    `json:"original"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Tags     []string  `json:"tags"`
}

func hasSnapshotTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}

func verifyRecoverySnapshot(ctx context.Context, restic func(...string) *exec.Cmd, base []string, dir, dumpPath string, r *RecoveryRecord, selected recoverySnapshot) error {
	var raw bytes.Buffer
	if err := runBackupCommand(restic("dump", r.SnapshotID, "/manifest.json"), &limitedBackupWriter{Writer: &raw, Remaining: 1 << 20}); err != nil {
		return errors.New("selected recovery manifest could not be read")
	}
	var m captureManifest
	if json.Unmarshal(raw.Bytes(), &m) != nil {
		return errors.New("selected recovery manifest is malformed")
	}
	if err := validateRecoveryManifest(m, r, selected, os.Getenv("BARTL_KEYCLOAK_IMAGE"), os.Getenv("BARTL_POSTGRES_IMAGE")); err != nil {
		return err
	}
	file, err := os.OpenFile(dumpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("private recovery archive unavailable")
	}
	hash := sha256.New()
	count := &backupCountingWriter{Writer: io.MultiWriter(file, hash)}
	err = runBackupCommand(restic("dump", r.SnapshotID, "/keycloak.dump"), &limitedBackupWriter{Writer: count, Remaining: m.DumpBytes})
	closeErr := file.Close()
	if err != nil || closeErr != nil || count.Bytes != m.DumpBytes || hex.EncodeToString(hash.Sum(nil)) != m.DumpSHA256 {
		return errors.New("selected recovery archive checksum or byte count does not match manifest")
	}
	if err := runBackupCommand(backupCommand(ctx, dir, base, "pg_restore", "--list", dumpPath), io.Discard); err != nil {
		return errors.New("selected recovery PostgreSQL archive validation failed")
	}
	r.DumpSHA256 = m.DumpSHA256
	r.DumpBytes = m.DumpBytes
	return nil
}

func validateRecoveryManifest(m captureManifest, r *RecoveryRecord, selected recoverySnapshot, keycloakImage, postgresImage string) error {
	if !backupFullID(selected.ID) || selected.ID != r.SnapshotID || selected.Original != "" && !backupFullID(selected.Original) {
		return errors.New("selected recovery snapshot identity is malformed")
	}
	sourceSnapshotID := selected.ID
	if selected.Original != "" {
		sourceSnapshotID = selected.Original
	}
	// RepositoryID is capture provenance, not a restriction to the source repository.
	// Format 1 producers capture before publication and leave SnapshotID empty.
	// When present, it identifies the source snapshot preserved by Restic copy.
	if m.FormatVersion != 1 || m.Installation != r.Installation || m.Instance != r.Instance || !backupFullID(m.RepositoryID) || m.SnapshotID != "" && (!backupFullID(m.SnapshotID) || m.SnapshotID != sourceSnapshotID) || m.ApplicationUID == "" || m.PVCUID == "" || m.DatabaseIdentity == "" || m.OperationUID == "" || m.PodUID == "" || m.DatabaseBinding != fmt.Sprintf("bartl-keycloak:%s:%s:%s:%s", m.Installation, m.Instance, m.ApplicationUID, m.PVCUID) || m.Database == "" || m.DatabaseUser == "" || m.PackageRelease == "" || !compatibleRecoveryImage(m.KeycloakImage, keycloakImage) || m.PostgresImage != postgresImage || m.DumpFile != "keycloak.dump" || !backupFullID(m.DumpSHA256) || m.DumpBytes <= 0 {
		return errors.New("selected recovery manifest binding, native image identity or archive evidence is incompatible")
	}
	if _, err := time.Parse(time.RFC3339Nano, m.CapturedAt); err != nil {
		return errors.New("selected recovery capture time is malformed")
	}
	return nil
}

// Unexpected routines, schemas or extensions are not an empty application database.
const recoveryObjectsSQL = `SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND n.nspname NOT LIKE 'pg_temp_%' AND c.relkind IN ('r','p','v','m','S','f') UNION ALL SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname NOT IN ('pg_catalog','information_schema') UNION ALL SELECT 1 FROM pg_extension WHERE extname <> 'plpgsql' UNION ALL SELECT 1 FROM pg_namespace WHERE nspname NOT IN ('public','pg_catalog','information_schema') AND nspname NOT LIKE 'pg_toast%' AND nspname NOT LIKE 'pg_temp_%'`
