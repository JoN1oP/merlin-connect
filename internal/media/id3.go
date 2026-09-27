// Package media prepares local files for the box: ID3 tags, cover images and
// folder import.
package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"unicode/utf16"
)

// Tag holds the ID3v2 fields we use.
type Tag struct {
	Title string
	Cover []byte
}

const frontCover = 3

// ReadID3 reads the ID3v2 tag (v2.2, v2.3 or v2.4) at the start of r. A stream
// without a tag yields an empty Tag and no error.
func ReadID3(r io.Reader) (Tag, error) {
	var h [10]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return Tag{}, nil
		}
		return Tag{}, err
	}
	major, flags := h[3], h[5]
	if string(h[:3]) != "ID3" || major < 2 || major > 4 {
		return Tag{}, nil
	}
	body := make([]byte, syncsafe(h[6:10]))
	if _, err := io.ReadFull(r, body); err != nil {
		return Tag{}, err
	}
	if flags&0x80 != 0 && major < 4 {
		body = unsync(body)
	}
	if flags&0x40 != 0 {
		switch major {
		case 2:
			return Tag{}, nil // compressed v2.2 tags are not supported
		case 3:
			if len(body) < 4 {
				return Tag{}, nil
			}
			body = body[min(len(body), 4+int(binary.BigEndian.Uint32(body))):]
		case 4:
			if len(body) < 4 {
				return Tag{}, nil
			}
			body = body[min(len(body), syncsafe(body)):]
		}
	}
	return parseFrames(body, major), nil
}

func parseFrames(b []byte, major byte) Tag {
	var tag Tag
	coverType := -1
	idLen, headLen := 4, 10
	if major == 2 {
		idLen, headLen = 3, 6
	}
	for len(b) >= headLen && b[0] != 0 {
		id := string(b[:idLen])
		var size int
		var flags uint16
		switch major {
		case 2:
			size = int(b[3])<<16 | int(b[4])<<8 | int(b[5])
		case 3:
			size = int(binary.BigEndian.Uint32(b[4:]))
			flags = binary.BigEndian.Uint16(b[8:])
		case 4:
			size = syncsafe(b[4:8])
			flags = binary.BigEndian.Uint16(b[8:])
		}
		if size > len(b)-headLen {
			break
		}
		data := b[headLen : headLen+size]
		b = b[headLen+size:]

		if major == 3 && flags&0x00C0 != 0 || major == 4 && flags&0x000C != 0 {
			continue // compressed or encrypted frame
		}
		if major == 4 && flags&0x0001 != 0 && len(data) >= 4 {
			data = data[4:] // data length indicator
		}
		if major == 4 && flags&0x0002 != 0 {
			data = unsync(data)
		}

		switch id {
		case "TIT2", "TT2":
			if tag.Title == "" {
				tag.Title = decodeText(data)
			}
		case "APIC", "PIC":
			pictureType, image := parsePicture(data, id == "PIC")
			if image != nil && (tag.Cover == nil || pictureType == frontCover && coverType != frontCover) {
				tag.Cover, coverType = image, pictureType
			}
		}
	}
	return tag
}

// parsePicture reads APIC `[enc][mime\0][type][desc\0][data]` or v2.2 PIC
// `[enc][fmt 3 bytes][type][desc\0][data]`.
func parsePicture(data []byte, v22 bool) (int, []byte) {
	if len(data) < 2 {
		return 0, nil
	}
	enc, rest := data[0], data[1:]
	if v22 {
		if len(rest) < 3 {
			return 0, nil
		}
		rest = rest[3:]
	} else {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			return 0, nil
		}
		rest = rest[i+1:]
	}
	if len(rest) < 1 {
		return 0, nil
	}
	pictureType, rest := int(rest[0]), rest[1:]
	end := terminator(rest, enc)
	if end < 0 {
		return 0, nil
	}
	return pictureType, rest[end:]
}

// terminator returns the offset just past the null terminator of an encoded string.
func terminator(b []byte, enc byte) int {
	if enc == 1 || enc == 2 {
		for i := 0; i+1 < len(b); i += 2 {
			if b[i] == 0 && b[i+1] == 0 {
				return i + 2
			}
		}
		return -1
	}
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return -1
	}
	return i + 1
}

// decodeText decodes a text frame and returns its first value.
func decodeText(data []byte) string {
	if len(data) < 1 {
		return ""
	}
	enc, b := data[0], data[1:]
	var s string
	switch enc {
	case 1, 2:
		order := binary.ByteOrder(binary.BigEndian)
		if enc == 1 && len(b) >= 2 {
			if b[0] == 0xFF && b[1] == 0xFE {
				order = binary.LittleEndian
			}
			b = b[2:]
		}
		units := make([]uint16, 0, len(b)/2)
		for i := 0; i+1 < len(b); i += 2 {
			units = append(units, order.Uint16(b[i:]))
		}
		s = string(utf16.Decode(units))
	case 3:
		s = string(b)
	default: // ISO-8859-1
		runes := make([]rune, len(b))
		for i, c := range b {
			runes[i] = rune(c)
		}
		s = string(runes)
	}
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func syncsafe(b []byte) int {
	return int(b[0]&0x7F)<<21 | int(b[1]&0x7F)<<14 | int(b[2]&0x7F)<<7 | int(b[3]&0x7F)
}

// unsync reverses ID3 unsynchronisation (0xFF 0x00 -> 0xFF).
func unsync(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte{0xFF, 0x00}, []byte{0xFF})
}
