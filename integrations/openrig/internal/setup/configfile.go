package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/config"
)

// Change is one field that differs between two configurations. The file holds no secret, so values are shown.
type Change struct{ Field, Old, New string }

// DiffConfig lists the fields whose values differ. old may be nil (no usable previous file).
func DiffConfig(old, new *config.Config) []Change {
	fields := func(c *config.Config) [][2]string {
		if c == nil {
			return nil
		}
		return [][2]string{
			{"participant_url", c.ParticipantURL}, {"certificate_sha256", c.CertificateSHA256},
			{"participant", c.Participant}, {"session", c.Session}, {"operator_seat", c.OperatorSeat},
			{"rig_path", c.RigPath}, {"rig_env_path", c.RigEnvPath}, {"openrig_home", c.OpenRigHome},
			{"openrig_url", c.OpenRigURL}, {"state_dir", c.StateDir}, {"credential_path", c.CredentialPath},
			{"limits", fmt.Sprintf("%d/%d/%d", c.Limits.InlineBodyBytes, c.Limits.PerSenderPerMinute, c.Limits.TotalPerMinute)},
			{"ready_check_timeout_ms", fmt.Sprint(c.ReadyCheckTimeoutMS)},
		}
	}
	was, now := fields(old), fields(new)
	var out []Change
	for i := range now {
		oldValue := ""
		if i < len(was) {
			oldValue = was[i][1]
		}
		if oldValue != now[i][1] {
			out = append(out, Change{Field: now[i][0], Old: oldValue, New: now[i][1]})
		}
	}
	return out
}

// WriteConfig writes c to path atomically: a private temp file in the same directory (mode 0600, created
// O_EXCL), fsync, config.Load on the temp file (so only a configuration the bridge accepts is installed),
// then rename. An existing file is first copied to path.bak-<UTC time> (never over an existing backup).
// It returns the backup path, empty when there was no previous file.
func WriteConfig(path string, c *config.Config, insecureHTTP bool, now time.Time) (string, error) {
	dir := filepath.Dir(path)
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", errors.New("cannot encode the configuration")
	}
	raw = append(raw, '\n')

	tmp, err := createTemp(dir, filepath.Base(path))
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	done := false
	defer func() {
		_ = tmp.Close()
		if !done {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(raw); err != nil {
		return "", errors.New("cannot write the temporary configuration file")
	}
	if err := tmp.Sync(); err != nil {
		return "", errors.New("cannot sync the temporary configuration file")
	}
	if _, err := config.Load(tmpPath, insecureHTTP); err != nil {
		return "", fmt.Errorf("the new configuration would not load (nothing was changed): %w", err)
	}

	backup := ""
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return "", errors.New("the existing configuration path is not a regular file (nothing was changed)")
		}
		if backup, err = copyAside(path, now); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("cannot inspect the existing configuration file")
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return backup, errors.New("cannot move the new configuration into place (the old file is untouched)")
	}
	done = true
	syncDir(dir)
	return backup, nil
}

// CopyAside copies a regular file to path.bak-<UTC time> with mode 0600, never over an existing file.
func CopyAside(path string, now time.Time) (string, error) { return copyAside(path, now) }

func copyAside(path string, now time.Time) (string, error) {
	src, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot read the file to back it up (nothing was changed)")
	}
	defer func() { _ = src.Close() }()
	backup := path + ".bak-" + now.UTC().Format("20060102T150405Z")
	dst, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", errors.New("cannot create the backup file (nothing was changed)")
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		_ = os.Remove(backup)
		return "", errors.New("cannot write the backup file (nothing was changed)")
	}
	if err := dst.Sync(); err != nil {
		_ = dst.Close()
		return "", errors.New("cannot sync the backup file (nothing was changed)")
	}
	return backup, dst.Close()
}

// WriteFileAtomic writes data to path through a private temp file in the same directory and a rename. It does
// not look at an existing file: the caller has already decided to replace it.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := createTemp(dir, filepath.Base(path))
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	done := false
	defer func() {
		_ = tmp.Close()
		if !done {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return errors.New("cannot write the temporary file")
	}
	if err := tmp.Chmod(mode); err != nil {
		return errors.New("cannot set the mode of the temporary file")
	}
	if err := tmp.Sync(); err != nil {
		return errors.New("cannot sync the temporary file")
	}
	if err := tmp.Close(); err != nil {
		return errors.New("cannot close the temporary file")
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return errors.New("cannot move the file into place")
	}
	done = true
	syncDir(dir)
	return nil
}

// createTemp makes dir/.base.tmp-N with O_EXCL and mode 0600.
func createTemp(dir, base string) (*os.File, error) {
	for i := 0; i < 100; i++ {
		name := filepath.Join(dir, fmt.Sprintf(".%s.tmp-%d-%d", base, os.Getpid(), i))
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, errors.New("cannot create a temporary file next to the target")
		}
	}
	return nil, errors.New("cannot create a temporary file next to the target")
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
