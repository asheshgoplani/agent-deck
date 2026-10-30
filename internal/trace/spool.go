package trace

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	// DBFileName is the trace database under a profile's data dir.
	DBFileName = "trace.db"
	// spoolDirName holds the spool under a profile's data dir.
	spoolDirName = "trace-spool"
	// spoolFileName is the live spool every hook appends to.
	spoolFileName = "spool.jsonl"
	// maxSpoolBytes bounds the live spool when nothing ingests it: past
	// this size appends are dropped (tracing is best-effort, the disk is not).
	maxSpoolBytes = 32 << 20
	// maxLineBytes bounds one record; a longer one loses its summary.
	maxLineBytes = 2048
)

// DBPath is trace.db under the profile's data dir (session.GetProfileDir).
func DBPath(profileDir string) string { return filepath.Join(profileDir, DBFileName) }

// SpoolDir is the spool directory under the profile's data dir.
func SpoolDir(profileDir string) string { return filepath.Join(profileDir, spoolDirName) }

// SpoolPath is the live spool file hooks append to.
func SpoolPath(profileDir string) string {
	return filepath.Join(SpoolDir(profileDir), spoolFileName)
}

// AppendSpool appends rec as one JSON line to the live spool under
// profileDir. It is the hook handler's only trace write: one open with
// O_APPEND, one fstat, one write, no locks, no SQLite. Callers ignore the
// error (best-effort); it is returned for tests.
func AppendSpool(profileDir string, rec SpoolRecord) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if len(line) > maxLineBytes {
		rec.Summary = ""
		if line, err = json.Marshal(rec); err != nil {
			return err
		}
		if len(line) > maxLineBytes {
			return errors.New("trace: spool record too large")
		}
	}
	line = append(line, '\n')

	p := SpoolPath(profileDir)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if errors.Is(err, fs.ErrNotExist) {
		if mkErr := os.MkdirAll(filepath.Dir(p), 0o700); mkErr != nil {
			return mkErr
		}
		f, err = os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	}
	if err != nil {
		return err
	}
	if st, err := f.Stat(); err == nil && st.Size() > maxSpoolBytes {
		return errors.Join(errors.New("trace: spool full, record dropped"), spoolClose(f))
	}
	_, err = f.Write(line)
	return errors.Join(err, spoolClose(f))
}

// spoolClose closes the writable spool handle; a test swaps it to check that
// a failed close is reported.
var spoolClose = func(f *os.File) error { return f.Close() }
