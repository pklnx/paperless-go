package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/pklnx/paperless-go/internal/blobstore"
	"github.com/pklnx/paperless-go/internal/config"
	"github.com/pklnx/paperless-go/internal/consume"
	"github.com/pklnx/paperless-go/internal/ingest"
)

// newConsumer sets up the consumer of a's consume folder. A folder that
// is missing or not writable is only logged: the consumer keeps trying at
// every scan, and the web server runs either way. A consume folder that
// overlaps the data directory is an error: the consumer would delete the
// document files.
func newConsumer(a *app, log *slog.Logger) (*consume.Consumer, error) {
	blobs, err := blobstore.New(a.cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("open the document files: %w", err)
	}
	im, err := ingest.New(a.store, blobs, a.cfg.MaxUploadSize)
	if err != nil {
		return nil, err
	}
	dir := a.cfg.ConsumeDir
	if err := checkSeparate(dir, a.cfg.DataDir); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		log.Warn("the consume folder is not a folder; create it or unset "+config.EnvConsumeDir, "dir", dir, "err", err)
	} else if err := tryWrite(dir); err != nil {
		log.Warn("the consume folder is not writable: consumed files cannot be deleted or moved; make it writable for UID "+
			fmt.Sprint(os.Getuid()), "dir", dir, "err", err)
	}
	return consume.New(dir, a.store, im, consume.Options{Log: log}), nil
}

// checkSeparate refuses a consume folder that is the data directory, inside
// it or around it, after resolving symlinks.
func checkSeparate(consumeDir, dataDir string) error {
	c, err := resolvePath(consumeDir)
	if err != nil {
		return err
	}
	d, err := resolvePath(dataDir)
	if err != nil {
		return err
	}
	if within(c, d) || within(d, c) {
		return fmt.Errorf("%s (%s) and the data directory (%s) must not be inside each other", config.EnvConsumeDir, consumeDir, dataDir)
	}
	return nil
}

// resolvePath makes p absolute and resolves the symlinks of its longest
// existing part (the rest may not exist yet).
func resolvePath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	rest := ""
	for cur := abs; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// within reports whether p is base or below it.
func within(p, base string) bool {
	rel, err := filepath.Rel(base, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
