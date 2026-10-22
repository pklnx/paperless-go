package process

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
)

// ThumbnailSize is the longer side of a thumbnail in pixels.
const ThumbnailSize = 512

// MaxThumbnailBytes bounds a thumbnail file: a 512-pixel JPEG is far
// smaller, so a bigger file is not one.
const MaxThumbnailBytes = 2 << 20

// thumbnailStem is the name pdftoppm writes the thumbnail under, without
// its extension (-singlefile adds ".jpg").
const thumbnailStem = "thumbnail"

// errBadThumbnail is a thumbnail file that is not a JPEG of the expected
// size.
var errBadThumbnail = errors.New("pdftoppm wrote no usable thumbnail")

// Thumbnail renders the first page of the PDF at pdf (inside the work
// directory dir) as a JPEG whose longer side is ThumbnailSize pixels, and
// returns its path in dir. An error means there is no thumbnail; the
// caller stores the result without one, unless ctx ended.
func (p *Pipeline) Thumbnail(ctx context.Context, dir, pdf string) (string, error) {
	_, err := p.Runner.Run(ctx, Command{
		Name: "pdftoppm",
		Args: []string{"-f", "1", "-l", "1", "-singlefile", "-scale-to", fmt.Sprint(ThumbnailSize),
			"-jpeg", "-jpegopt", "quality=80", filepath.Base(pdf), thumbnailStem},
		Dir: dir,
	})
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, thumbnailStem+".jpg")
	if err := checkThumbnail(path); err != nil {
		return "", err
	}
	return path, nil
}

// checkThumbnail checks that path is a JPEG of at most MaxThumbnailBytes
// whose sides are at most ThumbnailSize pixels.
func checkThumbnail(path string) error {
	f, err := os.Open(path) //nolint:gosec // in the job's work directory
	if err != nil {
		return fmt.Errorf("%w: %w", errBadThumbnail, err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("%w: %w", errBadThumbnail, err)
	}
	if fi.Size() > MaxThumbnailBytes {
		return fmt.Errorf("%w: %d bytes", errBadThumbnail, fi.Size())
	}
	cfg, err := jpeg.DecodeConfig(f)
	if err != nil {
		return fmt.Errorf("%w: %w", errBadThumbnail, err)
	}
	if !sizeOK(cfg) {
		return fmt.Errorf("%w: %d×%d pixels", errBadThumbnail, cfg.Width, cfg.Height)
	}
	return nil
}

func sizeOK(cfg image.Config) bool {
	return cfg.Width > 0 && cfg.Height > 0 && cfg.Width <= ThumbnailSize && cfg.Height <= ThumbnailSize
}
