package process

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ThumbnailSize is the longer side of a thumbnail in pixels.
const ThumbnailSize = 512

// MaxThumbnailBytes bounds a thumbnail file: a 512-pixel JPEG is far
// smaller, so a bigger file is not one.
const MaxThumbnailBytes = 2 << 20

// DefaultThumbnailTimeout limits rendering the thumbnail. A first page at
// 512 pixels takes well under a second; a PDF that makes pdftoppm hang
// costs this much once, and the document is stored without a thumbnail.
const DefaultThumbnailTimeout = 60 * time.Second

// thumbnailStem is the name pdftoppm writes the thumbnail under, without
// its extension (-singlefile adds ".jpg").
const thumbnailStem = "thumbnail"

// errBadThumbnail is a thumbnail file that is not a JPEG of the expected
// size.
var errBadThumbnail = errors.New("pdftoppm wrote no usable thumbnail")

// errThumbnailTimeout is a thumbnail that took longer than its own limit
// (the cause of the thumbnail's context). It is not the attempt's
// *timeoutError: the attempt goes on and stores its result without a
// thumbnail.
var errThumbnailTimeout = errors.New("the thumbnail took too long")

// thumbnailTimeout is the limit of the thumbnail: ThumbnailTimeout, or
// DefaultThumbnailTimeout when it is not set.
func (p *Pipeline) thumbnailTimeout() time.Duration {
	if p.ThumbnailTimeout > 0 {
		return p.ThumbnailTimeout
	}
	return DefaultThumbnailTimeout
}

// Thumbnail renders the first page of the PDF at pdf (inside the work
// directory dir) as a JPEG whose longer side is ThumbnailSize pixels, and
// returns its path in dir. pdftoppm gets the thumbnail's own limit (see
// thumbnailTimeout) within ctx; when that limit ends it, the error wraps
// errThumbnailTimeout. An error means there is no thumbnail; the caller
// stores the result without one, unless ctx ended.
func (p *Pipeline) Thumbnail(ctx context.Context, dir, pdf string) (string, error) {
	limit := p.thumbnailTimeout()
	tctx, cancel := context.WithTimeoutCause(ctx, limit, errThumbnailTimeout)
	defer cancel()
	_, err := p.Runner.Run(tctx, Command{
		Name: "pdftoppm",
		Args: []string{"-f", "1", "-l", "1", "-singlefile", "-scale-to", fmt.Sprint(ThumbnailSize),
			"-jpeg", "-jpegopt", "quality=80", filepath.Base(pdf), thumbnailStem},
		Dir: dir,
	})
	if err != nil {
		// Only the thumbnail's own limit: when ctx ended (shutdown, lost
		// lease, the attempt's limit), Cause is ctx's cause, and the
		// error stays ctx's for the caller.
		if ctx.Err() == nil && errors.Is(context.Cause(tctx), errThumbnailTimeout) {
			return "", fmt.Errorf("%w (limit %s): %w", errThumbnailTimeout, limit, err)
		}
		return "", err
	}
	path := filepath.Join(dir, thumbnailStem+".jpg")
	if err := checkThumbnail(path); err != nil {
		return "", err
	}
	return path, nil
}

// checkThumbnail checks that path is a JPEG of at most MaxThumbnailBytes
// whose sides are at most ThumbnailSize pixels, and that it decodes in
// full: a truncated file has a valid header but would be served as a
// broken image. The size checks run first and bound the decode.
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
	// DecodeConfig read the header; Decode starts from the beginning.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("%w: %w", errBadThumbnail, err)
	}
	if _, err := jpeg.Decode(f); err != nil {
		return fmt.Errorf("%w: %w", errBadThumbnail, err)
	}
	return nil
}

func sizeOK(cfg image.Config) bool {
	return cfg.Width > 0 && cfg.Height > 0 && cfg.Width <= ThumbnailSize && cfg.Height <= ThumbnailSize
}
