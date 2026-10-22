package process

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // registers JPEG for image.DecodeConfig
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/pklnx/paperless-go/internal/ingest"
)

// Image conversion: JPEG and PNG become a one-page PDF with img2pdf, which
// embeds the image without re-encoding it; HEIC/HEIF is first decoded to
// JPEG with heif-dec. The pixel limit of ingest is checked again before
// anything is converted, and once more on the decoded HEIC.

// DPI of converted images. An image's own resolution is used when it is
// between minImageDPI and maxImageDPI; cameras write 72 or 96, which says
// nothing about the paper, so those images (and those without a
// resolution, HEIC among them) get defaultImageDPI, a scanner's usual
// resolution. The DPI sets the page size only; OCR reads the pixels.
const (
	defaultImageDPI = 300
	minImageDPI     = 100
	maxImageDPI     = 2400
)

// heifJPEGQuality is the JPEG quality of decoded HEIC images.
const heifJPEGQuality = 92

// Work files of the image conversion.
const (
	decodedImage = "decoded.jpg"
	imagePDF     = "image.pdf"
)

// User-visible messages of the image conversion.
var msgTooManyPixels = fmt.Sprintf("The image has more than %d megapixels.", ingest.MaxPixels/1_000_000)

const msgUnreadableImage = "The image cannot be read: it is damaged or in a format this server cannot convert."

// imageToPDF converts the image in to a one-page PDF in its work
// directory and returns the PDF's path.
func (p *Pipeline) imageToPDF(ctx context.Context, in Input) (string, error) {
	if err := checkImage(in.Path, in.MIME); err != nil {
		return "", err
	}
	src := in.Path
	dpi := 0
	switch in.MIME {
	case mimeHEIC, mimeHEIF:
		_, err := p.Runner.Run(ctx, Command{
			Name: "heif-dec",
			Args: []string{"--quiet", "-q", fmt.Sprint(heifJPEGQuality), filepath.Base(in.Path), decodedImage},
			Dir:  in.Dir,
		})
		if err != nil {
			return "", convertError(err)
		}
		src = filepath.Join(in.Dir, decodedImage)
		// The decoded image, not the container's header, is what img2pdf
		// and OCR get.
		if err := checkDecoded(src); err != nil {
			return "", err
		}
	default:
		if x, ok := imageDPI(in.Path, in.MIME); ok {
			dpi = x
		}
	}
	if dpi == 0 {
		dpi = defaultImageDPI
	}
	_, err := p.Runner.Run(ctx, Command{
		Name: "img2pdf",
		Args: []string{"--imgsize", fmt.Sprintf("%ddpi", dpi), "--rotation=ifvalid", "-o", imagePDF, filepath.Base(src)},
		Dir:  in.Dir,
	})
	if err != nil {
		return "", convertError(err)
	}
	return filepath.Join(in.Dir, imagePDF), nil
}

// convertError maps a failed heif-dec or img2pdf: both exit with status 1
// when they cannot read the image, which a retry does not change. A tool
// killed by a signal (out of memory) or that did not start may pass.
func convertError(err error) error {
	var te *ToolError
	if errors.As(err, &te) && te.ExitCode == 1 {
		return fmt.Errorf("%w: %w", permanent(msgUnreadableImage), err)
	}
	return err
}

// checkImage checks the stored image as ingest did when it was stored: its
// type must match mime and it may have at most ingest.MaxPixels pixels.
func checkImage(path, mime string) error {
	f, err := os.Open(path) //nolint:gosec // in the job's work directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	prefix, err := io.ReadAll(io.LimitReader(f, ingest.PrefixSize))
	if err != nil {
		return err
	}
	t, err := ingest.Detect(prefix)
	var re *ingest.RefusedError
	switch {
	case errors.As(err, &re) && re.Reason == ingest.ReasonTooManyPixels:
		return permanent(msgTooManyPixels)
	case err != nil:
		return fmt.Errorf("%w: %w", permanent(msgUnreadableImage), err)
	case t.MIME != mime && (!isHEIF(t.MIME) || !isHEIF(mime)):
		return permanent(msgUnreadableImage)
	}
	return nil
}

func isHEIF(mime string) bool { return mime == mimeHEIC || mime == mimeHEIF }

// checkDecoded checks the pixel limit of a decoded JPEG.
func checkDecoded(path string) error {
	f, err := os.Open(path) //nolint:gosec // in the job's work directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return fmt.Errorf("%w: %w", permanent(msgUnreadableImage), err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return permanent(msgUnreadableImage)
	}
	if uint64(cfg.Width)*uint64(cfg.Height) > ingest.MaxPixels {
		return permanent(msgTooManyPixels)
	}
	return nil
}

// imageDPI reads the horizontal resolution of a JPEG (JFIF, then EXIF) or
// PNG (pHYs) from its first ingest.PrefixSize bytes. ok is false without
// a usable resolution: none, or one outside minImageDPI..maxImageDPI.
func imageDPI(path, mime string) (dpi int, ok bool) {
	f, err := os.Open(path) //nolint:gosec // in the job's work directory
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, ingest.PrefixSize))
	if err != nil {
		return 0, false
	}
	var d float64
	switch mime {
	case mimeJPEG:
		d = jpegDPI(b)
	case mimePNG:
		d = pngDPI(b)
	}
	if math.IsNaN(d) || d < minImageDPI || d > maxImageDPI {
		return 0, false
	}
	return int(math.Round(d)), true
}

// jpegDPI returns the resolution from the JFIF header, or else from the
// EXIF data, or 0.
func jpegDPI(b []byte) float64 {
	if len(b) < 2 || b[0] != 0xFF || b[1] != 0xD8 {
		return 0
	}
	var exif float64
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xFF {
			return exif
		}
		marker := b[i+1]
		if marker == 0xFF { // fill byte
			i++
			continue
		}
		// Markers without a length; SOS and the frame headers end the
		// metadata.
		if marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
			i += 2
			continue
		}
		if marker == 0xDA || (marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC) {
			return exif
		}
		n := int(binary.BigEndian.Uint16(b[i+2 : i+4]))
		if n < 2 || i+2+n > len(b) {
			return exif
		}
		seg := b[i+4 : i+2+n]
		switch {
		case marker == 0xE0 && len(seg) >= 12 && bytes.HasPrefix(seg, []byte("JFIF\x00")):
			x := float64(binary.BigEndian.Uint16(seg[8:10]))
			switch seg[7] {
			case 1: // dots per inch
				if x > 0 {
					return x
				}
			case 2: // dots per cm
				if x > 0 {
					return x * 2.54
				}
			}
		case marker == 0xE1 && exif == 0 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")):
			exif = exifDPI(seg[6:])
		}
		i += 2 + n
	}
	return exif
}

// exifDPI reads XResolution and ResolutionUnit from IFD0 of the TIFF
// structure t, or returns 0.
func exifDPI(t []byte) float64 {
	if len(t) < 8 {
		return 0
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	if bo.Uint16(t[2:4]) != 42 {
		return 0
	}
	ifd := uint64(bo.Uint32(t[4:8]))
	if ifd+2 > uint64(len(t)) {
		return 0
	}
	count := uint64(bo.Uint16(t[ifd : ifd+2]))
	var res float64
	unit := uint16(2) // inches unless stated
	for k := range count {
		e := ifd + 2 + 12*k
		if e+12 > uint64(len(t)) {
			return 0
		}
		tag, typ := bo.Uint16(t[e:e+2]), bo.Uint16(t[e+2:e+4])
		switch {
		case tag == 0x011A && typ == 5: // XResolution, RATIONAL
			off := uint64(bo.Uint32(t[e+8 : e+12]))
			if off+8 > uint64(len(t)) {
				return 0
			}
			num, den := bo.Uint32(t[off:off+4]), bo.Uint32(t[off+4:off+8])
			if den == 0 {
				return 0
			}
			res = float64(num) / float64(den)
		case tag == 0x0128 && typ == 3: // ResolutionUnit, SHORT
			unit = bo.Uint16(t[e+8 : e+10])
		}
	}
	switch unit {
	case 2:
		return res
	case 3:
		return res * 2.54
	}
	return 0
}

// pngDPI returns the resolution from the pHYs chunk, or 0.
func pngDPI(b []byte) float64 {
	const sig = "\x89PNG\r\n\x1a\n"
	if !bytes.HasPrefix(b, []byte(sig)) {
		return 0
	}
	for i := len(sig); i+8 <= len(b); {
		n := int(binary.BigEndian.Uint32(b[i : i+4])) // < 2³², and int has 64 bits on the supported platforms
		typ := string(b[i+4 : i+8])
		if typ == "IDAT" || n > len(b)-i-12 {
			return 0
		}
		if typ == "pHYs" && n == 9 {
			d := b[i+8 : i+17]
			if d[8] != 1 { // the unit is not the metre
				return 0
			}
			return float64(binary.BigEndian.Uint32(d[0:4])) * 0.0254
		}
		i += 12 + n
	}
	return 0
}
