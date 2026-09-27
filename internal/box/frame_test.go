package box

import (
	"bytes"
	"errors"
	"testing"
)

func TestChecksumKnownAnswer(t *testing.T) {
	// CRC-32/MPEG-2 check value for "123456789".
	if got := checksum([]byte("123456789")); got != 0x0376E6E7 {
		t.Fatalf("checksum = %#08x, want 0x0376e6e7", got)
	}
}

func TestWriteFrameLayout(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, 7, []byte{0xAA, 0xBB}); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if len(b) != 8 || b[0] != 7 || b[1] != 7 || b[2] != 0xAA || b[3] != 0xBB {
		t.Fatalf("frame = % x", b)
	}
	crc := uint32(b[4]) | uint32(b[5])<<8 | uint32(b[6])<<16 | uint32(b[7])<<24
	if crc != checksum([]byte{7, 0xAA, 0xBB}) {
		t.Fatalf("crc %#x does not cover cmd+payload", crc)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := bytes.Repeat([]byte{0x42}, MaxPayload)
	if err := WriteFrame(&buf, 13, payload); err != nil {
		t.Fatal(err)
	}
	cmd, got, err := ReadFrame(&buf)
	if err != nil || cmd != 13 || !bytes.Equal(got, payload) {
		t.Fatalf("ReadFrame = %d, %d bytes, %v", cmd, len(got), err)
	}
}

func TestWriteFrameRejectsOversizedPayload(t *testing.T) {
	if err := WriteFrame(&bytes.Buffer{}, 1, make([]byte, MaxPayload+1)); err == nil {
		t.Fatal("want error for oversized payload")
	}
}

func TestReadFrameRejectsBadChecksum(t *testing.T) {
	var buf bytes.Buffer
	WriteFrame(&buf, 2, nil)
	b := buf.Bytes()
	b[len(b)-1] ^= 0xFF
	if _, _, err := ReadFrame(bytes.NewReader(b)); !errors.Is(err, ErrBadCRC) {
		t.Fatalf("err = %v, want ErrBadCRC", err)
	}
}

func TestReadFrameRejectsShortLength(t *testing.T) {
	if _, _, err := ReadFrame(bytes.NewReader([]byte{4, 0, 0, 0, 0})); err == nil {
		t.Fatal("want error for length < 5")
	}
}
