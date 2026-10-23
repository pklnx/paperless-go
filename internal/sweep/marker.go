package sweep

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pklnx/paperless-go/internal/store"
)

// MarkerFile is the name of the installation marker in the data directory:
// it holds the UUID of the installation (the installation row of
// 00001_initial.sql) whose documents the directory's files are. The walk
// queues files only in a data directory bound to the database it runs on,
// so a wrong PAPERLESS_GO_DATABASE_URL, a restore into the wrong database
// or a reinstall that kept the data directory cannot queue the whole store.
const MarkerFile = "installation"

// The walk's refusals. Each queues nothing.
var (
	// ErrUnbound means the data directory has no marker: an installation
	// from before the marker, until `sweep --bind`.
	ErrUnbound = errors.New("sweep: the data directory is not bound to this installation yet")
	// ErrOtherInstallation means the marker names another installation
	// than the database's.
	ErrOtherInstallation = errors.New("sweep: the data directory belongs to another installation")
	// ErrEmptyDatabase means the database holds no document while the
	// data directory holds files.
	ErrEmptyDatabase = errors.New("sweep: the database has no documents, but the data directory has files")
	// ErrTooMany means one walk would queue more than a tenth of the
	// files it saw (and more than MinRefusal of them).
	ErrTooMany = errors.New("sweep: the walk would queue more than 10 % of the files")
)

// ReadMarker returns the installation the data directory root is bound
// to, or ErrUnbound when it has no marker.
func ReadMarker(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, MarkerFile)) //nolint:gosec // the configured data directory
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrUnbound
	}
	if err != nil {
		return "", fmt.Errorf("read the installation marker: %w", err)
	}
	id := strings.TrimSpace(string(b))
	if !isUUID(id) {
		return "", fmt.Errorf("the installation marker %s holds no installation ID", filepath.Join(root, MarkerFile))
	}
	return id, nil
}

// isUUID reports whether s is a UUID in its canonical form, as
// store.Installation returns it (lowercase hex with four dashes).
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range []byte(s) {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	return true
}

// CheckBinding returns nil when root is bound to installation, ErrUnbound
// without a marker and an error wrapping ErrOtherInstallation when the
// marker names another one.
func CheckBinding(root, installation string) error {
	id, err := ReadMarker(root)
	if err != nil {
		return err
	}
	if id != installation {
		return fmt.Errorf("%w: %s names installation %s, the database is installation %s",
			ErrOtherInstallation, filepath.Join(root, MarkerFile), id, installation)
	}
	return nil
}

// writeMarker binds root to installation durably: a temporary file in
// root, synchronized, renamed over the marker, and root synchronized.
func writeMarker(root, installation string) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".installation-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.WriteString(installation + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(root, MarkerFile)); err != nil {
		return err
	}
	d, err := os.Open(root) //nolint:gosec // the configured data directory
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}

// hasBlobFiles reports whether root's documents/ holds any regular file;
// a missing documents/ holds none.
func hasBlobFiles(root string) (bool, error) {
	found := false
	err := filepath.WalkDir(filepath.Join(root, "documents"), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return found, err
}

// FreshBinding is what BindFresh did.
type FreshBinding int

// The outcomes of BindFresh.
const (
	// Bound: the marker was written into a data directory without files.
	Bound FreshBinding = iota + 1
	// AlreadyBound: the marker names this installation.
	AlreadyBound
	// LeftUnbound: no marker, but the data directory has files; the
	// operator binds it with `sweep --bind`.
	LeftUnbound
	// BoundElsewhere: the marker names another installation.
	BoundElsewhere
)

// BindFresh is migrate's step: it binds root to installation when root
// has no marker and no file below documents/ (a fresh data directory,
// created if missing). A data directory with files and no marker stays
// unbound, and one bound to another installation is left alone.
func BindFresh(root, installation string) (FreshBinding, error) {
	switch err := CheckBinding(root, installation); {
	case err == nil:
		return AlreadyBound, nil
	case errors.Is(err, ErrOtherInstallation):
		return BoundElsewhere, err
	case !errors.Is(err, ErrUnbound):
		return 0, err
	}
	files, err := hasBlobFiles(root)
	if err != nil {
		return 0, err
	}
	if files {
		return LeftUnbound, nil
	}
	if err := writeMarker(root, installation); err != nil {
		return 0, fmt.Errorf("write the installation marker: %w", err)
	}
	return Bound, nil
}

// Binder is the part of *store.Store Bind uses.
type Binder interface {
	Installation(ctx context.Context) (*store.Installation, error)
	HasDocuments(ctx context.Context) (bool, error)
}

// Bind is `sweep --bind`: it binds root to the database's installation.
// It refuses, unless force, when the database has no documents
// (ErrEmptyDatabase: probably the wrong database) and when the marker
// names another installation (ErrOtherInstallation). It reports whether
// the marker was written (false: it already named this installation).
// With dryRun it only checks.
func Bind(ctx context.Context, st Binder, root string, force, dryRun bool) (bool, error) {
	inst, err := st.Installation(ctx)
	if err != nil {
		return false, err
	}
	id := inst.ID
	switch err := CheckBinding(root, id); {
	case err == nil:
		return false, nil
	case errors.Is(err, ErrOtherInstallation) && !force:
		return false, fmt.Errorf("%w; check %s and the data directory, or bind it anyway with --force", err, "PAPERLESS_GO_DATABASE_URL")
	case !errors.Is(err, ErrUnbound) && !errors.Is(err, ErrOtherInstallation):
		return false, err
	}
	has, err := st.HasDocuments(ctx)
	if err != nil {
		return false, err
	}
	if !has && !force {
		return false, fmt.Errorf("%w; is PAPERLESS_GO_DATABASE_URL right? Bind anyway with --force", ErrEmptyDatabase)
	}
	if dryRun {
		return true, nil
	}
	if err := writeMarker(root, id); err != nil {
		return false, fmt.Errorf("write the installation marker: %w", err)
	}
	return true, nil
}
