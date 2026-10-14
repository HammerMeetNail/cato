package webauthn

import (
	"encoding/binary"
	"errors"
	"math"
)

// A minimal CBOR (RFC 8949) decoder for what WebAuthn sends: unsigned and
// negative integers, byte and text strings, arrays, maps, and the simple
// values false/true/null. Indefinite lengths and tags are refused.

var errCBOR = errors.New("malformed CBOR")

const maxDepth = 16

type decoder struct {
	b     []byte
	depth int
}

// decodeCBOR returns the first item and the bytes after it.
func decodeCBOR(b []byte) (any, []byte, error) {
	d := &decoder{b: b}
	v, err := d.item()
	return v, d.b, err
}

func (d *decoder) head() (major byte, arg uint64, err error) {
	if len(d.b) == 0 {
		return 0, 0, errCBOR
	}
	ib := d.b[0]
	d.b = d.b[1:]
	major, info := ib>>5, ib&0x1f
	switch {
	case info < 24:
		return major, uint64(info), nil
	case info == 24:
		if len(d.b) < 1 {
			return 0, 0, errCBOR
		}
		arg, d.b = uint64(d.b[0]), d.b[1:]
	case info == 25:
		if len(d.b) < 2 {
			return 0, 0, errCBOR
		}
		arg, d.b = uint64(binary.BigEndian.Uint16(d.b)), d.b[2:]
	case info == 26:
		if len(d.b) < 4 {
			return 0, 0, errCBOR
		}
		arg, d.b = uint64(binary.BigEndian.Uint32(d.b)), d.b[4:]
	case info == 27:
		if len(d.b) < 8 {
			return 0, 0, errCBOR
		}
		arg, d.b = binary.BigEndian.Uint64(d.b), d.b[8:]
	default:
		return 0, 0, errCBOR // indefinite lengths and reserved values
	}
	return major, arg, nil
}

func (d *decoder) item() (any, error) {
	d.depth++
	defer func() { d.depth-- }()
	if d.depth > maxDepth {
		return nil, errCBOR
	}
	major, arg, err := d.head()
	if err != nil {
		return nil, err
	}
	switch major {
	case 0:
		if arg > math.MaxInt64 {
			return nil, errCBOR
		}
		return int64(arg), nil
	case 1:
		if arg > math.MaxInt64 {
			return nil, errCBOR
		}
		return -1 - int64(arg), nil
	case 2, 3:
		if arg > uint64(len(d.b)) {
			return nil, errCBOR
		}
		v := d.b[:arg]
		d.b = d.b[arg:]
		if major == 3 {
			return string(v), nil
		}
		return append([]byte{}, v...), nil
	case 4:
		if arg > uint64(len(d.b)) {
			return nil, errCBOR
		}
		out := make([]any, 0, arg)
		for i := uint64(0); i < arg; i++ {
			v, err := d.item()
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case 5:
		if arg > uint64(len(d.b)) {
			return nil, errCBOR
		}
		out := make(map[any]any, arg)
		for i := uint64(0); i < arg; i++ {
			k, err := d.item()
			if err != nil {
				return nil, err
			}
			switch k.(type) {
			case int64, string:
			default:
				return nil, errCBOR
			}
			v, err := d.item()
			if err != nil {
				return nil, err
			}
			out[k] = v
		}
		return out, nil
	case 7:
		switch arg {
		case 20:
			return false, nil
		case 21:
			return true, nil
		case 22, 23:
			return nil, nil
		}
	}
	return nil, errCBOR
}
