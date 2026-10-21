package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/pklnx/paperless-go/internal/blobstore"
	"github.com/pklnx/paperless-go/internal/config"
	"github.com/pklnx/paperless-go/internal/consume"
	"github.com/pklnx/paperless-go/internal/ingest"
)

// newConsumer sets up the consumer of a's consume folder. A folder that
// is missing or not writable is only logged: the consumer keeps trying at
// every scan, and the web server runs either way.
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
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		log.Warn("the consume folder is not a folder; create it or unset "+config.EnvConsumeDir, "dir", dir, "err", err)
	} else if err := tryWrite(dir); err != nil {
		log.Warn("the consume folder is not writable: consumed files cannot be deleted or moved; make it writable for UID "+
			fmt.Sprint(os.Getuid()), "dir", dir, "err", err)
	}
	return consume.New(dir, a.store, im, consume.Options{Log: log}), nil
}
