package ingest

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// The HEIF reader below reads only what the pixel limit needs: the size of
// the primary item, from its ispe (image spatial extents) property
// (ISO/IEC 23008-12). The path is meta → pitm (the primary item's ID) and
// meta → iprp → ipco (the properties) + ipma (which item has which). No
// image data is read, and nothing outside the buffered prefix.

func be32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }

// box is one ISO BMFF box: its four-character type and its payload.
type box struct {
	typ     string
	payload []byte
}

// readBoxes splits b into its boxes. A box that runs past the end of b is
// an error: the prefix may have cut it off.
func readBoxes(b []byte) ([]box, error) {
	var out []box
	for len(b) > 0 {
		if len(b) < 8 {
			return out, errors.New("truncated box header")
		}
		size, typ, hdr := uint64(be32(b[0:4])), string(b[4:8]), uint64(8)
		switch size {
		case 0: // to the end
			size = uint64(len(b))
		case 1: // 64-bit size follows
			if len(b) < 16 {
				return out, errors.New("truncated box header")
			}
			size, hdr = binary.BigEndian.Uint64(b[8:16]), 16
		}
		if size < hdr {
			return out, fmt.Errorf("box %q: bad size %d", typ, size)
		}
		if size > uint64(len(b)) {
			return out, fmt.Errorf("box %q: truncated", typ)
		}
		out = append(out, box{typ: typ, payload: b[hdr:size]})
		b = b[size:]
	}
	return out, nil
}

// fullBox splits a FullBox payload into its version, flags and body.
func fullBox(b box) (version uint8, flags uint32, body []byte, err error) {
	if len(b.payload) < 4 {
		return 0, 0, nil, fmt.Errorf("box %q: truncated", b.typ)
	}
	v := be32(b.payload[0:4])
	return uint8(v >> 24), v & 0xFFFFFF, b.payload[4:], nil
}

func find(bs []box, typ string) (box, bool) {
	for _, b := range bs {
		if b.typ == typ {
			return b, true
		}
	}
	return box{}, false
}

// topLevelMeta returns the top-level meta box of prefix. Boxes after it
// (usually mdat) may be cut off by the prefix; meta itself must be whole.
func topLevelMeta(prefix []byte) (box, error) {
	for b := prefix; len(b) > 0; {
		if len(b) < 8 {
			break
		}
		size, typ, hdr := uint64(be32(b[0:4])), string(b[4:8]), uint64(8)
		switch size {
		case 0:
			size = uint64(len(b))
		case 1:
			if len(b) < 16 {
				return box{}, errors.New("truncated box header")
			}
			size, hdr = binary.BigEndian.Uint64(b[8:16]), 16
		}
		if size < hdr {
			return box{}, fmt.Errorf("box %q: bad size %d", typ, size)
		}
		if size > uint64(len(b)) {
			if typ == "meta" {
				return box{}, errors.New("meta box truncated")
			}
			break
		}
		if typ == "meta" {
			return box{typ: typ, payload: b[hdr:size]}, nil
		}
		b = b[size:]
	}
	return box{}, errors.New("no meta box in the readable part")
}

// heifSize returns the width and height of the primary item of a HEIF
// file from its ispe property. It fails when no size can be found.
func heifSize(prefix []byte) (w, h uint64, err error) {
	meta, err := topLevelMeta(prefix)
	if err != nil {
		return 0, 0, err
	}
	_, _, body, err := fullBox(meta)
	if err != nil {
		return 0, 0, err
	}
	children, err := readBoxes(body)
	if err != nil {
		return 0, 0, fmt.Errorf("meta: %w", err)
	}

	pitm, ok := find(children, "pitm")
	if !ok {
		return 0, 0, errors.New("no primary item")
	}
	v, _, pb, err := fullBox(pitm)
	if err != nil {
		return 0, 0, err
	}
	var primary uint32
	switch {
	case v == 0 && len(pb) >= 2:
		primary = uint32(binary.BigEndian.Uint16(pb))
	case v > 0 && len(pb) >= 4:
		primary = be32(pb)
	default:
		return 0, 0, errors.New("pitm: truncated")
	}

	iprp, ok := find(children, "iprp")
	if !ok {
		return 0, 0, errors.New("no item properties")
	}
	props, err := readBoxes(iprp.payload)
	if err != nil {
		return 0, 0, fmt.Errorf("iprp: %w", err)
	}
	ipco, ok := find(props, "ipco")
	if !ok {
		return 0, 0, errors.New("no property container")
	}
	container, err := readBoxes(ipco.payload)
	if err != nil {
		return 0, 0, fmt.Errorf("ipco: %w", err)
	}

	for _, ipma := range props {
		if ipma.typ != "ipma" {
			continue
		}
		indexes, err := associations(ipma, primary)
		if err != nil {
			return 0, 0, err
		}
		for _, i := range indexes {
			// Property indexes are 1-based; 0 means none.
			if i == 0 || int(i) > len(container) || container[i-1].typ != "ispe" {
				continue
			}
			_, _, eb, err := fullBox(container[i-1])
			if err != nil || len(eb) < 8 {
				return 0, 0, errors.New("ispe: truncated")
			}
			return uint64(be32(eb[0:4])), uint64(be32(eb[4:8])), nil
		}
	}
	return 0, 0, errors.New("primary item has no size")
}

// associations returns the property indexes ipma assigns to item.
func associations(ipma box, item uint32) ([]uint16, error) {
	version, flags, b, err := fullBox(ipma)
	if err != nil {
		return nil, err
	}
	errShort := errors.New("ipma: truncated")
	if len(b) < 4 {
		return nil, errShort
	}
	count := be32(b)
	b = b[4:]
	for range count {
		var id uint32
		if version < 1 {
			if len(b) < 2 {
				return nil, errShort
			}
			id, b = uint32(binary.BigEndian.Uint16(b)), b[2:]
		} else {
			if len(b) < 4 {
				return nil, errShort
			}
			id, b = be32(b), b[4:]
		}
		if len(b) < 1 {
			return nil, errShort
		}
		n := int(b[0])
		b = b[1:]
		width := 1
		if flags&1 != 0 {
			width = 2
		}
		if len(b) < n*width {
			return nil, errShort
		}
		if id != item {
			b = b[n*width:]
			continue
		}
		out := make([]uint16, n)
		for i := range n {
			if width == 2 {
				out[i] = binary.BigEndian.Uint16(b[2*i:]) & 0x7FFF
			} else {
				out[i] = uint16(b[i] & 0x7F)
			}
		}
		return out, nil
	}
	return nil, nil
}
