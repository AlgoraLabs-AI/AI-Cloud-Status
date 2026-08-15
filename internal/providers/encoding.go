package providers

import (
	"encoding/binary"
	"unicode/utf16"
)

// decodeFeed normalizes raw feed bytes to UTF-8 with no byte-order mark, so a
// standard JSON/XML parser can read them. It transparently handles:
//
//   - a UTF-8 BOM (EF BB BF), which otherwise makes encoding/json fail on the
//     leading rune, and
//   - UTF-16 (LE or BE) with a BOM, which AWS's public events feed is actually
//     served as — naively treating those bytes as UTF-8 yields NUL-laced garbage
//     that no JSON parser accepts.
//
// Bytes without a recognized BOM are returned unchanged.
func decodeFeed(b []byte) []byte {
	switch {
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		return b[3:] // UTF-8 BOM
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		return utf16ToUTF8(b[2:], binary.BigEndian)
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		return utf16ToUTF8(b[2:], binary.LittleEndian)
	default:
		return b
	}
}

// utf16ToUTF8 decodes UTF-16 code units (in the given byte order) to a UTF-8
// byte slice. A trailing odd byte, if any, is ignored.
func utf16ToUTF8(b []byte, order binary.ByteOrder) []byte {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, order.Uint16(b[i:i+2]))
	}
	return []byte(string(utf16.Decode(units)))
}
