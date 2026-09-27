// Package box speaks the Merlin box's TCP protocol (see docs/protocol.md).
package box

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// MaxPayload is the largest payload a frame can carry: the length byte counts the
// command byte, the payload and the 4 CRC bytes.
const MaxPayload = 255 - 5

// ErrBadCRC reports a frame whose checksum does not match its contents.
var ErrBadCRC = errors.New("box: bad frame checksum")

// checksum is CRC-32/MPEG-2: poly 0x04C11DB7, init 0xFFFFFFFF, MSB-first, no
// reflection, no final xor.
func checksum(data []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, b := range data {
		crc ^= uint32(b) << 24
		for range 8 {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// WriteFrame writes `[len][cmd][payload][crc32 LE]` to w.
func WriteFrame(w io.Writer, cmd byte, payload []byte) error {
	if len(payload) > MaxPayload {
		return fmt.Errorf("box: payload of %d bytes exceeds %d", len(payload), MaxPayload)
	}
	frame := make([]byte, 0, len(payload)+6)
	frame = append(frame, byte(len(payload)+5), cmd)
	frame = append(frame, payload...)
	frame = binary.LittleEndian.AppendUint32(frame, checksum(frame[1:]))
	_, err := w.Write(frame)
	return err
}

// ReadFrame reads one frame from r and verifies its checksum.
func ReadFrame(r io.Reader) (cmd byte, payload []byte, err error) {
	var size [1]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return 0, nil, err
	}
	if size[0] < 5 {
		return 0, nil, fmt.Errorf("box: frame length %d is too small", size[0])
	}
	body := make([]byte, size[0])
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	end := len(body) - 4
	if binary.LittleEndian.Uint32(body[end:]) != checksum(body[:end]) {
		return 0, nil, ErrBadCRC
	}
	return body[0], body[1:end], nil
}
