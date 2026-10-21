package ingest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/pklnx/paperless-go/internal/store"
)

// Test files are generated here; no real documents.

func pdfFile(pad int) []byte {
	return append(bytes.Repeat([]byte{' '}, pad), []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n%%EOF\n")...)
}

func encodedImage(t *testing.T, format string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(1, 1, color.RGBA{R: 200, A: 255})
	var b bytes.Buffer
	var err error
	if format == "png" {
		err = png.Encode(&b, img)
	} else {
		err = jpeg.Encode(&b, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// pngHeader is a PNG signature with an IHDR chunk for w×h: enough for
// DecodeConfig, without pixel data.
func pngHeader(w, h uint32) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 2 // 8-bit RGB
	chunk := binary.BigEndian.AppendUint32(nil, 13)
	chunk = append(chunk, "IHDR"...)
	chunk = append(chunk, ihdr...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(chunk[4:]))
	return append([]byte("\x89PNG\r\n\x1a\n"), chunk...)
}

// jpegHeader is SOI, a baseline SOF0 segment for w×h and the start of a
// scan: enough for DecodeConfig, without scan data.
func jpegHeader(w, h uint16) []byte {
	b := []byte{0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x11, 8}
	b = binary.BigEndian.AppendUint16(b, h)
	b = binary.BigEndian.AppendUint16(b, w)
	b = append(b, 3, 1, 0x22, 0, 2, 0x11, 1, 3, 0x11, 1)
	return append(b, 0xFF, 0xDA, 0x00, 0x08)
}

// mkbox builds an ISO BMFF box.
func mkbox(typ string, payload ...[]byte) []byte {
	body := bytes.Join(payload, nil)
	b := binary.BigEndian.AppendUint32(nil, uint32(8+len(body))) //nolint:gosec // small test boxes
	return append(append(b, typ...), body...)
}

// mkfull builds a FullBox.
func mkfull(typ string, version uint8, flags uint32, payload ...[]byte) []byte {
	vf := binary.BigEndian.AppendUint32(nil, uint32(version)<<24|flags)
	return mkbox(typ, append([][]byte{vf}, payload...)...)
}

func ftyp(major string, compatible ...string) []byte {
	b := []byte(major + "\x00\x00\x00\x00")
	for _, c := range compatible {
		b = append(b, c...)
	}
	return mkbox("ftyp", b)
}

func ispe(w, h uint32) []byte {
	return mkfull("ispe", 0, 0, binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, w), h))
}

// heif is a HEIF file header: ftyp and a meta box whose primary item 1 has
// the size w×h, plus item 2 (a thumbnail, say) with a huge size that must
// not count. Property 1 is a dummy, 2 the primary's ispe, 3 item 2's.
func heif(major string, w, h uint32) []byte {
	ipma := binary.BigEndian.AppendUint32(nil, 2) // entry count
	ipma = append(ipma, 0, 1, 2, 0x81, 0x02)      // item 1: properties 1 (essential), 2
	ipma = append(ipma, 0, 2, 1, 0x03)            // item 2: property 3
	meta := mkfull("meta", 0, 0,
		mkfull("hdlr", 0, 0, make([]byte, 4), []byte("pict"), make([]byte, 13)),
		mkfull("pitm", 0, 0, []byte{0, 1}),
		mkbox("iprp",
			mkbox("ipco", mkbox("colr", []byte("nclx\x00\x01\x00\x01\x00\x01\x00")), ispe(w, h), ispe(60000, 60000)),
			mkfull("ipma", 0, 0, ipma)))
	return append(append(ftyp(major, "mif1", major), meta...), mkbox("mdat", []byte("image data"))...)
}

func refusal(t *testing.T, err error) Reason {
	t.Helper()
	var r *RefusedError
	if !errors.As(err, &r) || !errors.Is(err, ErrRefused) {
		t.Fatalf("error = %v, want a *RefusedError", err)
	}
	return r.Reason
}

func TestDetectTypes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		data   []byte
		mime   string
		source string
	}{
		{"pdf", pdfFile(0), MIMEPDF, store.SourceFile},
		{"pdf with leading junk", pdfFile(pdfWindow - len("%PDF-")), MIMEPDF, store.SourceFile},
		{"jpeg", encodedImage(t, "jpeg"), MIMEJPEG, store.SourceImage},
		{"png", encodedImage(t, "png"), MIMEPNG, store.SourceImage},
		{"heic", heif("heic", 4032, 3024), MIMEHEIC, store.SourceImage},
		{"heix", heif("heix", 10, 10), MIMEHEIC, store.SourceImage},
		{"hevc", heif("hevc", 10, 10), MIMEHEIC, store.SourceImage},
		{"hevx", heif("hevx", 10, 10), MIMEHEIC, store.SourceImage},
		{"mif1", heif("mif1", 10, 10), MIMEHEIF, store.SourceImage},
		{"msf1", heif("msf1", 10, 10), MIMEHEIF, store.SourceImage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			typ, err := Detect(tc.data)
			if err != nil || typ.MIME != tc.mime || typ.Source != tc.source {
				t.Errorf("Detect = %+v, %v; want %s, %s", typ, err, tc.mime, tc.source)
			}
		})
	}
}

func TestDetectRefusesUnsupported(t *testing.T) {
	gif := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;")
	for name, data := range map[string][]byte{
		"empty":          nil,
		"text":           []byte("just some notes\n"),
		"gif":            gif,
		"pdf too late":   pdfFile(pdfWindow - len("%PDF-") + 1),
		"avif":           heif("avif", 10, 10),
		"avis":           heif("avis", 10, 10),
		"mif1 with avif": append(ftyp("mif1", "avif", "miaf"), heif("mif1", 10, 10)[len(ftyp("mif1", "mif1", "mif1")):]...),
		"mp4":            append(ftyp("isom", "iso2", "mp41"), mkbox("mdat")...),
		"short ftyp":     []byte("\x00\x00\x00\x0cftypheic"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Detect(data)
			if got := refusal(t, err); got != ReasonUnsupported {
				t.Errorf("reason = %s, want %s", got, ReasonUnsupported)
			}
		})
	}
}

func TestDetectPixelLimit(t *testing.T) {
	// 8000×5000 is exactly MaxPixels.
	for _, tc := range []struct {
		name string
		at   []byte // exactly MaxPixels
		over []byte // one row more
	}{
		{"png", pngHeader(8000, 5000), pngHeader(8000, 5001)},
		{"jpeg", jpegHeader(8000, 5000), jpegHeader(8000, 5001)},
		{"heic", heif("heic", 8000, 5000), heif("heic", 8000, 5001)},
		{"heic huge", heif("heic", 10, 10), heif("heic", 1<<31, 1<<31)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Detect(tc.at); err != nil {
				t.Errorf("at the limit: %v", err)
			}
			_, err := Detect(tc.over)
			if got := refusal(t, err); got != ReasonTooManyPixels {
				t.Errorf("over the limit: reason = %s, want %s", got, ReasonTooManyPixels)
			}
		})
	}
}

func TestDetectUnreadable(t *testing.T) {
	ok := heif("heic", 10, 10)
	metaAt := len(ftyp("heic", "mif1", "heic"))
	noIspe := append(ftyp("heic", "mif1"), mkfull("meta", 0, 0,
		mkfull("pitm", 0, 0, []byte{0, 1}),
		mkbox("iprp", mkbox("ipco", ispe(10, 10)),
			mkfull("ipma", 0, 0, []byte{0, 0, 0, 1, 0, 2, 1, 1})), // only item 2 has it
	)...)
	for name, data := range map[string][]byte{
		"jpeg truncated":   {0xFF, 0xD8, 0xFF},
		"png truncated":    pngHeader(10, 10)[:20],
		"png zero size":    pngHeader(0, 10),
		"heic no meta":     append(ftyp("heic", "mif1"), mkbox("mdat", []byte("x"))...),
		"heic meta cut":    ok[:metaAt+30],
		"heic no ispe":     noIspe,
		"heic zero size":   heif("heic", 0, 10),
		"heic no pitm":     append(ftyp("heic"), mkfull("meta", 0, 0, mkbox("iprp"))...),
		"heic bad box":     append(ftyp("heic"), 0, 0, 0, 3, 'm', 'e', 't', 'a'),
		"heic meta beyond": append(append(ftyp("heic"), 0, 0x20, 0, 0, 'm', 'd', 'a', 't'), make([]byte, 64)...),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Detect(data)
			if got := refusal(t, err); got != ReasonUnreadable {
				t.Errorf("reason = %s, want %s (%v)", got, ReasonUnreadable, err)
			}
		})
	}
}

// Large box sizes, version-1 pitm and ipma with 16-bit property indexes.
func TestHEIFSizeVariants(t *testing.T) {
	ipma := binary.BigEndian.AppendUint32(nil, 1)
	ipma = binary.BigEndian.AppendUint32(ipma, 70000) // 32-bit item ID
	ipma = append(ipma, 1, 0x80, 0x01)                // property 1, essential, 16 bits
	meta := mkfull("meta", 0, 0,
		mkfull("pitm", 1, 0, binary.BigEndian.AppendUint32(nil, 70000)),
		mkbox("iprp", mkbox("ipco", ispe(1234, 567)), mkfull("ipma", 1, 1, ipma)))
	// The same meta box with a 64-bit size header.
	large := binary.BigEndian.AppendUint32(nil, 1)
	large = append(large, "meta"...)
	large = binary.BigEndian.AppendUint64(large, uint64(len(meta)+8))
	large = append(large, meta[8:]...)
	for name, m := range map[string][]byte{"plain": meta, "largesize": large} {
		t.Run(name, func(t *testing.T) {
			w, h, err := heifSize(append(append(ftyp("heic"), mkbox("free", []byte("x"))...), m...))
			if err != nil || w != 1234 || h != 567 {
				t.Errorf("heifSize = %d×%d, %v", w, h, err)
			}
		})
	}
}

func TestRefusedErrorMessage(t *testing.T) {
	_, err := Detect([]byte("hello"))
	if !strings.HasPrefix(err.Error(), "refused: unsupported") {
		t.Errorf("message = %q", err)
	}
}
