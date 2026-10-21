package ingest

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // registers the JPEG format for image.DecodeConfig
	_ "image/png"  // registers the PNG format for image.DecodeConfig

	"github.com/pklnx/paperless-go/internal/store"
)

// MaxPixels is the most pixels (width × height) an image may have.
const MaxPixels = 40_000_000

// PrefixSize is how many leading bytes detection looks at. Type and pixel
// checks run on this prefix before anything is stored.
const PrefixSize = 1 << 20

// pdfWindow is how far into the file the PDF header may start.
const pdfWindow = 1024

// Detected MIME types.
const (
	MIMEPDF  = "application/pdf"
	MIMEJPEG = "image/jpeg"
	MIMEPNG  = "image/png"
	MIMEHEIC = "image/heic"
	MIMEHEIF = "image/heif"
)

// Type is a detected file type.
type Type struct {
	MIME   string // one of the MIME* constants
	Source string // store.SourceFile for PDFs, store.SourceImage for images
}

// Reason says why a file is refused. A refusal is the file's fault: trying
// the same file again gives the same answer.
type Reason string

// Refusal reasons.
const (
	// ReasonUnsupported: not a PDF, JPEG, PNG or HEIC/HEIF (AVIF included).
	ReasonUnsupported Reason = "unsupported"
	// ReasonTooLarge: more bytes than the configured size limit.
	ReasonTooLarge Reason = "too_large"
	// ReasonTooManyPixels: an image with more than MaxPixels pixels.
	ReasonTooManyPixels Reason = "too_many_pixels"
	// ReasonUnreadable: the type is recognized but its header cannot be
	// read, for example an image without a findable size.
	ReasonUnreadable Reason = "unreadable"
)

// RefusedError is a refused file. errors.Is(err, ErrRefused) holds.
type RefusedError struct {
	Reason Reason
	Detail string // for logs and the consume folder's sidecar; no user data
}

func (e *RefusedError) Error() string {
	if e.Detail == "" {
		return "refused: " + string(e.Reason)
	}
	return fmt.Sprintf("refused: %s: %s", e.Reason, e.Detail)
}

// Is makes errors.Is(err, ErrRefused) true.
func (e *RefusedError) Is(target error) bool { return target == ErrRefused }

// ErrRefused matches every *RefusedError.
var ErrRefused = errors.New("ingest: file refused")

func refuse(r Reason, format string, args ...any) error {
	return &RefusedError{Reason: r, Detail: fmt.Sprintf(format, args...)}
}

var (
	jpegMagic = []byte{0xFF, 0xD8, 0xFF}
	pngMagic  = []byte("\x89PNG\r\n\x1a\n")
	pdfMagic  = []byte("%PDF-")
)

// Detect decides a file's type from its first bytes, never from a name or
// a declared type, and checks the pixel limit of images. prefix holds the
// first PrefixSize bytes of the file, or the whole file if it is shorter.
// A refused file gives a *RefusedError.
func Detect(prefix []byte) (Type, error) {
	switch {
	case bytes.HasPrefix(prefix, jpegMagic):
		return imageType(prefix, MIMEJPEG, "JPEG")
	case bytes.HasPrefix(prefix, pngMagic):
		return imageType(prefix, MIMEPNG, "PNG")
	case len(prefix) >= 12 && string(prefix[4:8]) == "ftyp":
		mime, err := heifBrand(prefix)
		if err != nil {
			return Type{}, err
		}
		w, h, err := heifSize(prefix)
		if err != nil {
			return Type{}, refuse(ReasonUnreadable, "HEIF: %v", err)
		}
		if err := checkPixels(w, h); err != nil {
			return Type{}, err
		}
		return Type{MIME: mime, Source: store.SourceImage}, nil
	case bytes.Contains(prefix[:min(len(prefix), pdfWindow)], pdfMagic):
		return Type{MIME: MIMEPDF, Source: store.SourceFile}, nil
	}
	return Type{}, refuse(ReasonUnsupported, "unknown file type")
}

// imageType checks the pixel limit of a JPEG or PNG from its header,
// without decoding the image.
func imageType(prefix []byte, mime, name string) (Type, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(prefix))
	if err != nil {
		return Type{}, refuse(ReasonUnreadable, "%s header: %v", name, err)
	}
	if err := checkPixels(uint64(max(cfg.Width, 0)), uint64(max(cfg.Height, 0))); err != nil { //nolint:gosec // clamped to ≥ 0
		return Type{}, err
	}
	return Type{MIME: mime, Source: store.SourceImage}, nil
}

func checkPixels(w, h uint64) error {
	if w == 0 || h == 0 {
		return refuse(ReasonUnreadable, "image has no size")
	}
	// Each side fits in 32 bits, so the product cannot overflow.
	if w > 1<<32 || h > 1<<32 || w*h > MaxPixels {
		return refuse(ReasonTooManyPixels, "%d×%d pixels, at most %d allowed", w, h, MaxPixels)
	}
	return nil
}

// heifBrands are the ftyp brands accepted as HEIC/HEIF, with the MIME type
// each gives.
var heifBrands = map[string]string{
	"heic": MIMEHEIC, "heix": MIMEHEIC, "hevc": MIMEHEIC, "hevx": MIMEHEIC,
	"mif1": MIMEHEIF, "msf1": MIMEHEIF,
}

// avifBrands mark AVIF, which shares the HEIF container (and often lists
// mif1) but is not supported.
var avifBrands = map[string]bool{"avif": true, "avis": true}

// heifBrand reads the ftyp box at the start of prefix and returns the MIME
// type of an accepted HEIC/HEIF file. The major brand decides if it is
// known; otherwise the compatible brands do. Any AVIF brand refuses the
// file.
func heifBrand(prefix []byte) (string, error) {
	size := uint64(be32(prefix[0:4]))
	if size < 16 || len(prefix) < 16 {
		return "", refuse(ReasonUnsupported, "malformed ftyp box")
	}
	size = min(size, uint64(len(prefix)))
	major := string(prefix[8:12])
	var compatible []string
	for off := uint64(16); off+4 <= size; off += 4 {
		compatible = append(compatible, string(prefix[off:off+4]))
	}
	if avifBrands[major] {
		return "", refuse(ReasonUnsupported, "AVIF")
	}
	for _, b := range compatible {
		if avifBrands[b] {
			return "", refuse(ReasonUnsupported, "AVIF")
		}
	}
	if m, ok := heifBrands[major]; ok {
		return m, nil
	}
	for _, b := range compatible {
		if m, ok := heifBrands[b]; ok {
			return m, nil
		}
	}
	return "", refuse(ReasonUnsupported, "ftyp brand %q", major)
}
