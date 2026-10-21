package process

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WorkDir is this process's directory below <data>/work, where jobs copy
// the original and the tools write their files. It is not the blob
// store's tmp/. Several processes may share the data directory (serve and
// a process command): each holds a lock on its own directory, and a
// process that starts removes only the directories whose lock nobody
// holds, left behind by a crash.
type WorkDir struct {
	path string
	lock *os.File
}

// workPrefix starts the name of each process's directory; lockName is the
// lock file inside it.
const (
	workPrefix = "proc-"
	lockName   = ".lock"
)

// OpenWorkDir creates this process's work directory below dataDir/work and
// removes the stale ones.
func OpenWorkDir(dataDir string, log *slog.Logger) (*WorkDir, error) {
	root := filepath.Join(dataDir, "work")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	removeStale(root, log)
	dir, err := os.MkdirTemp(root, workPrefix+"*")
	if err != nil {
		return nil, err
	}
	// The lock is taken on a temporary name and then renamed: another
	// process never finds a lock file nobody holds yet.
	tmp, err := os.CreateTemp(dir, "lock-*")
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := lockFile(tmp); err != nil {
		_ = tmp.Close()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("lock work directory: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, lockName)); err != nil {
		_ = tmp.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &WorkDir{path: dir, lock: tmp}, nil
}

// staleUnlocked is how old a work directory without a lock file must be
// to be removed: a younger one may be another process's that is about to
// rename its lock in.
const staleUnlocked = time.Hour

// removeStale removes the work directories of processes that ended: those
// whose lock nobody holds, and those without a lock file (a crash between
// creating the directory and its lock) older than staleUnlocked.
func removeStale(root string, log *slog.Logger) {
	entries, err := os.ReadDir(root)
	if err != nil {
		log.Warn("read work directory", "dir", root, "err", err)
		return
	}
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		if !e.IsDir() || !strings.HasPrefix(e.Name(), workPrefix) {
			_ = os.RemoveAll(dir) // work/ holds nothing else
			continue
		}
		f, err := os.Open(filepath.Join(dir, lockName)) //nolint:gosec // below the data directory
		if errors.Is(err, fs.ErrNotExist) {
			if fi, serr := os.Stat(dir); serr == nil && time.Since(fi.ModTime()) > staleUnlocked {
				_ = os.RemoveAll(dir)
			}
			continue
		}
		if err != nil {
			log.Warn("open work directory lock", "dir", dir, "err", err)
			continue
		}
		locked, err := tryLockFile(f)
		if err != nil || !locked {
			_ = f.Close()
			continue // its process is running
		}
		if err := os.RemoveAll(dir); err != nil {
			log.Warn("remove stale work directory", "dir", dir, "err", err)
		}
		_ = f.Close()
	}
}

// Job creates a fresh directory for one attempt of a job.
func (d *WorkDir) Job(id int64) (string, error) {
	return os.MkdirTemp(d.path, fmt.Sprintf("job-%d-*", id))
}

// Path returns the directory.
func (d *WorkDir) Path() string { return d.path }

// Close removes the directory and releases its lock.
func (d *WorkDir) Close() error {
	err := os.RemoveAll(d.path)
	_ = d.lock.Close()
	return err
}
