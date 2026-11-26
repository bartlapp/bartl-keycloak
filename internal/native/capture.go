package native

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// readRetainedCapture never trusts staging metadata to select another database,
// repository or installation. A damaged retained point is preserved for diagnosis.
func readRetainedCapture(ctx context.Context, dir string, expected BackupResult, binding string) (captureManifest, []byte, error) {
	var m captureManifest
	f, err := os.Open(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return m, nil, errors.New("retained capture manifest unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	closeErr := f.Close()
	if err != nil || closeErr != nil || len(data) > 1<<20 {
		return m, nil, errors.New("retained capture manifest unreadable or oversized")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil || decoder.Decode(new(any)) != io.EOF {
		return m, nil, errors.New("retained capture manifest malformed")
	}
	if m.FormatVersion != 1 || m.Installation != expected.Installation || m.Instance != expected.Instance ||
		m.ApplicationUID != expected.ApplicationUID || m.PVCUID != expected.PVCUID ||
		m.DatabaseBinding != binding || m.DatabaseIdentity != expected.DatabaseIdentity ||
		m.RepositoryID != expected.RepositoryID || m.KeycloakImage != expected.KeycloakImage ||
		m.PostgresImage != expected.PostgresImage || m.PackageRelease != expected.PackageRelease ||
		m.OperationUID == "" || m.PodUID == "" || m.SnapshotID != "" || m.DumpFile != "keycloak.dump" ||
		m.DumpBytes <= 0 || !backupFullID(m.DumpSHA256) {
		return m, nil, errors.New("retained capture does not match current owned state and repository")
	}
	f, err = os.Open(filepath.Join(dir, "keycloak.dump"))
	if err != nil {
		return m, nil, errors.New("retained capture archive unavailable")
	}
	hash := sha256.New()
	size, err := io.Copy(hash, &backupContextReader{Context: ctx, Reader: f})
	closeErr = f.Close()
	if err != nil || closeErr != nil || size != m.DumpBytes || hex.EncodeToString(hash.Sum(nil)) != m.DumpSHA256 {
		return m, nil, errors.New("retained capture archive integrity failed")
	}
	return m, data, nil
}

func persistCaptureManifest(dir string, data []byte) error {
	f, err := os.OpenFile(filepath.Join(dir, "manifest.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("capture manifest could not be retained")
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return syncCaptureDirectory(dir)
}

func syncCaptureDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
