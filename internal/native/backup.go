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
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

// BackupResult is emitted only after the exact published snapshot has been read
// back. A snapshot left by an interrupted worker is not a verified result.
type BackupResult struct {
	FormatVersion    int    `json:"formatVersion"`
	Installation     string `json:"installation"`
	Instance         string `json:"instance"`
	ApplicationUID   string `json:"applicationUID"`
	PVCUID           string `json:"pvcUID"`
	OperationUID     string `json:"operationUID"`
	PodUID           string `json:"podUID"`
	DatabaseIdentity string `json:"databaseIdentity"`
	RepositoryID     string `json:"repositoryID"`
	SnapshotID       string `json:"snapshotID"`
	PackageRelease   string `json:"packageRelease"`
	KeycloakImage    string `json:"keycloakImage"`
	PostgresImage    string `json:"postgresImage"`
}

type captureManifest struct {
	BackupResult
	DatabaseBinding string `json:"databaseBinding"`
	Database        string `json:"database"`
	DatabaseUser    string `json:"databaseUser"`
	CapturedAt      string `json:"capturedAt"`
	DumpFile        string `json:"dumpFile"`
	DumpSHA256      string `json:"dumpSHA256"`
	DumpBytes       int64  `json:"dumpBytes"`
}

// Backup performs one finite native capture. No Kubernetes credentials or API
// calls are used. Repository initialization and separate destructive maintenance
// authority are the installation administrator's responsibility.
func Backup(parent context.Context, output io.Writer) error {
	ctx, cancel := context.WithTimeout(parent, 25*time.Minute)
	defer cancel()
	result := BackupResult{
		FormatVersion: 1,
		Installation:  os.Getenv("BARTL_INSTALLATION"), Instance: os.Getenv("BARTL_INSTANCE"),
		ApplicationUID: os.Getenv("BARTL_APPLICATION_UID"), PVCUID: os.Getenv("BARTL_PVC_UID"),
		OperationUID: os.Getenv("BARTL_OPERATION_UID"), PodUID: os.Getenv("BARTL_POD_UID"),
		PackageRelease: os.Getenv("BARTL_PACKAGE_RELEASE"), KeycloakImage: os.Getenv("BARTL_KEYCLOAK_IMAGE"),
		PostgresImage: os.Getenv("BARTL_POSTGRES_IMAGE"),
	}
	for _, value := range []string{result.Installation, result.Instance, result.ApplicationUID, result.PVCUID, result.OperationUID, result.PodUID, result.PackageRelease, result.KeycloakImage, result.PostgresImage} {
		if value == "" || strings.ContainsAny(value, "\x00\r\n,") {
			return errors.New("backup identity inputs must be nonempty and contain no control characters or commas")
		}
	}
	binding := os.Getenv("BARTL_DATABASE_BINDING")
	if binding != fmt.Sprintf("bartl-keycloak:%s:%s:%s:%s", result.Installation, result.Instance, result.ApplicationUID, result.PVCUID) {
		return errors.New("backup database binding does not match operation identity")
	}
	host := os.Getenv("PGHOST")
	credentials := Credentials{Username: os.Getenv("PGUSER"), Password: os.Getenv("PGPASSWORD"), Database: os.Getenv("PGDATABASE")}
	for _, value := range []string{host, credentials.Username, credentials.Password, credentials.Database, os.Getenv("RESTIC_PASSWORD"), os.Getenv("RESTIC_REST_USERNAME"), os.Getenv("RESTIC_REST_PASSWORD")} {
		if value == "" || strings.ContainsRune(value, 0) {
			return errors.New("backup requires nonempty native database and repository credentials")
		}
	}
	repository := os.Getenv("RESTIC_REPOSITORY")
	if !strings.HasPrefix(repository, "rest:https://") {
		return errors.New("backup repository must use rest:https")
	}
	u, err := url.Parse(strings.TrimPrefix(repository, "rest:"))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(repository, "\r\n\x00") {
		return errors.New("backup repository must be an HTTPS REST URL without embedded credentials, query or fragment")
	}
	if strings.ContainsAny(host, "\r\n\x00,/") {
		return errors.New("backup database host must be a single network host")
	}

	conn, err := connectBackupDatabase(ctx, host, credentials)
	if err != nil {
		return errors.New("backup database connection unavailable")
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = conn.Close(closeCtx)
	}()
	lockCtx, stopLock := context.WithTimeout(ctx, 5*time.Second)
	var acquired bool
	err = conn.QueryRow(lockCtx, "SELECT pg_try_advisory_lock(748920164)").Scan(&acquired)
	stopLock()
	if err != nil {
		return errors.New("backup exclusion lock could not be established")
	}
	if !acquired {
		return errors.New("backup refused: another native operation holds the database exclusion lock")
	}
	observeCtx, stopObserve := context.WithTimeout(ctx, 20*time.Second)
	observation, err := Observe(observeCtx, conn, binding)
	stopObserve()
	if err != nil {
		return errors.New("backup native database observation failed")
	}
	if observation.Classification != "Valid" {
		return fmt.Errorf("backup refused: %s (%s)", observation.Classification, observation.Reason)
	}
	result.DatabaseIdentity = observation.Identity

	// Only this goroutine touches the connection during external commands. Losing
	// this session loses exclusion, so cancel every child command immediately.
	stopHealth := make(chan struct{})
	healthDone := make(chan error, 1)
	go monitorBackupSession(ctx, cancel, conn, stopHealth, healthDone)
	healthStopped := false
	defer func() {
		if !healthStopped {
			close(stopHealth)
			<-healthDone
		}
	}()

	dir, err := os.MkdirTemp("", "bartl-keycloak-backup-")
	if err != nil {
		return errors.New("private backup temporary directory unavailable")
	}
	defer os.RemoveAll(dir)
	// Child environments are intentionally allowlisted: native connection/service
	// overrides, alternate repository/password commands and proxy credentials must
	// not redirect an operation away from its observed binding.
	baseEnv := []string{"PATH=/usr/local/bin:/usr/lib/postgresql/17/bin:/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir}
	pgEnv := append(append([]string{}, baseEnv...), "PGPASSWORD="+credentials.Password)
	resticEnv := append(append([]string{}, baseEnv...), "RESTIC_REPOSITORY="+repository, "RESTIC_PASSWORD="+os.Getenv("RESTIC_PASSWORD"), "RESTIC_REST_USERNAME="+os.Getenv("RESTIC_REST_USERNAME"), "RESTIC_REST_PASSWORD="+os.Getenv("RESTIC_REST_PASSWORD"))
	if info, statErr := os.Stat("/repository-ca/ca.crt"); statErr == nil {
		if !info.Mode().IsRegular() {
			return errors.New("repository CA must be a regular file")
		}
		resticEnv = append(resticEnv, "RESTIC_CACERT=/repository-ca/ca.crt")
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("repository CA unavailable")
	}
	restic := func(args ...string) *exec.Cmd {
		cmd := backupCommand(ctx, dir, resticEnv, "restic", append([]string{"--no-cache", "--json"}, args...)...)
		return cmd
	}
	var config bytes.Buffer
	if err = runBackupCommand(restic("cat", "config"), &limitedBackupWriter{Writer: &config, Remaining: 1 << 20}); err != nil {
		return errors.New("backup repository config could not be read")
	}
	var repositoryConfig struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	if err = json.Unmarshal(config.Bytes(), &repositoryConfig); err != nil || !backupFullID(repositoryConfig.ID) || (repositoryConfig.Version != 1 && repositoryConfig.Version != 2) {
		return errors.New("backup repository config identity or version is invalid")
	}
	result.RepositoryID = repositoryConfig.ID

	// Explicit libpq fields avoid interpreting database names as connection strings.
	quote := func(s string) string { return "'" + strings.NewReplacer("\\", "\\\\", "'", "\\'").Replace(s) + "'" }
	dsn := "host=" + quote(host) + " port=5432 user=" + quote(credentials.Username) + " dbname=" + quote(credentials.Database) + " sslmode=disable connect_timeout=5"
	dumpPath := filepath.Join(dir, "keycloak.dump")
	if err = runBackupCommand(backupCommand(ctx, dir, pgEnv, "pg_dump", "--format=custom", "--file="+dumpPath, "--dbname="+dsn, "--no-password"), io.Discard); err != nil {
		return errors.New("native PostgreSQL capture failed or was interrupted")
	}
	if err = os.Chmod(dumpPath, 0600); err != nil {
		return errors.New("native capture permissions could not be restricted")
	}
	if err = runBackupCommand(backupCommand(ctx, dir, baseEnv, "pg_restore", "--list", dumpPath), io.Discard); err != nil {
		return errors.New("native PostgreSQL archive validation failed")
	}
	dump, err := os.Open(dumpPath)
	if err != nil {
		return errors.New("native capture unavailable for hashing")
	}
	hash := sha256.New()
	size, hashErr := io.Copy(hash, &backupContextReader{Context: ctx, Reader: dump})
	closeErr := dump.Close()
	if hashErr != nil || closeErr != nil || size == 0 {
		return errors.New("native capture hashing failed")
	}
	manifest := captureManifest{BackupResult: result, DatabaseBinding: binding, Database: credentials.Database, DatabaseUser: credentials.Username, CapturedAt: time.Now().UTC().Format(time.RFC3339Nano), DumpFile: "keycloak.dump", DumpSHA256: hex.EncodeToString(hash.Sum(nil)), DumpBytes: size}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return errors.New("capture manifest encoding failed")
	}
	if err = os.WriteFile(filepath.Join(dir, "manifest.json"), manifestBytes, 0600); err != nil {
		return errors.New("capture manifest could not be retained")
	}
	cmd := restic("backup", "--host", result.Installation, "--tag", "installation="+result.Installation, "--tag", "instance="+result.Instance, "--tag", "operation="+result.OperationUID, "--", "keycloak.dump", "manifest.json")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return errors.New("backup publication output unavailable")
	}
	if err = cmd.Start(); err != nil {
		return errors.New("backup publication could not start")
	}
	decoder := json.NewDecoder(pipe)
	summaries := 0
	var decodeErr error
	for {
		var event struct {
			MessageType string `json:"message_type"`
			SnapshotID  string `json:"snapshot_id"`
			ErrorCount  uint64 `json:"error_count"`
			DryRun      bool   `json:"dry_run"`
		}
		if decodeErr = decoder.Decode(&event); decodeErr != nil {
			break
		}
		if event.MessageType == "error" || event.ErrorCount != 0 || event.DryRun {
			cancel()
			decodeErr = errors.New("publication reported incomplete work")
			break
		}
		if event.MessageType == "summary" {
			summaries++
			result.SnapshotID = event.SnapshotID
		}
	}
	if decodeErr != io.EOF {
		cancel()
	}
	waitErr := cmd.Wait()
	if decodeErr != io.EOF || waitErr != nil || summaries != 1 || !backupFullID(result.SnapshotID) {
		return errors.New("backup publication failed, was interrupted, or did not identify one complete snapshot")
	}
	var retrievedManifest bytes.Buffer
	if err = runBackupCommand(restic("dump", result.SnapshotID, "/manifest.json"), &limitedBackupWriter{Writer: &retrievedManifest, Remaining: int64(len(manifestBytes)) + 1}); err != nil || !bytes.Equal(retrievedManifest.Bytes(), manifestBytes) {
		return errors.New("published snapshot manifest readback did not match capture")
	}
	hash.Reset()
	counter := &backupCountingWriter{Writer: hash}
	if err = runBackupCommand(restic("dump", result.SnapshotID, "/keycloak.dump"), counter); err != nil || counter.Bytes != size || hex.EncodeToString(hash.Sum(nil)) != manifest.DumpSHA256 {
		return errors.New("published snapshot full archive readback did not match capture")
	}
	close(stopHealth)
	healthErr := <-healthDone
	healthStopped = true
	if healthErr != nil || ctx.Err() != nil {
		return errors.New("backup exclusion session or operation context was lost; publication is not verified")
	}
	// A final bounded query proves the same session still owns exclusion after
	// readback, before emitting the only success record.
	finalCtx, stopFinal := context.WithTimeout(ctx, 5*time.Second)
	err = conn.Ping(finalCtx)
	stopFinal()
	if err != nil || ctx.Err() != nil {
		return errors.New("backup exclusion session was lost before completion")
	}
	return json.NewEncoder(output).Encode(result)
}

// A newly scheduled Pod can precede CNI rule propagation. Wait only for
// transport admission, before taking any lock or starting native work.
// Authentication, observation and publication failures are never replayed.
func connectBackupDatabase(parent context.Context, host string, credentials Credentials) (*pgx.Conn, error) {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	for {
		conn, err := Connect(ctx, host, credentials)
		if err == nil {
			return conn, nil
		}
		var networkError *net.OpError
		if !errors.As(err, &networkError) {
			return nil, err
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func monitorBackupSession(ctx context.Context, cancel context.CancelFunc, conn *pgx.Conn, stop <-chan struct{}, done chan<- error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			done <- nil
			return
		case <-ctx.Done():
			done <- ctx.Err()
			return
		case <-ticker.C:
			queryCtx, end := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Ping(queryCtx)
			end()
			if err != nil {
				cancel()
				done <- errors.New("database exclusion session lost")
				return
			}
		}
	}
}

func backupCommand(ctx context.Context, dir string, environment []string, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = environment
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
	cmd.Stderr = io.Discard // Native diagnostics can contain URLs, names and credentials.
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

func runBackupCommand(cmd *exec.Cmd, output io.Writer) error {
	cmd.Stdout = output
	return cmd.Run()
}

func backupFullID(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && id == strings.ToLower(id)
}

type limitedBackupWriter struct {
	io.Writer
	Remaining int64
}

func (w *limitedBackupWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.Remaining {
		return 0, errors.New("native output exceeds expected size")
	}
	n, err := w.Writer.Write(p)
	w.Remaining -= int64(n)
	return n, err
}

type backupCountingWriter struct {
	io.Writer
	Bytes int64
}

func (w *backupCountingWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.Bytes += int64(n)
	return n, err
}

type backupContextReader struct {
	context.Context
	io.Reader
}

func (r *backupContextReader) Read(p []byte) (int, error) {
	if err := r.Context.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}
