package media

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// tag builds an ID3v2 tag. frames are raw frames (header included).
func tag(major, flags byte, frames ...[]byte) []byte {
	body := bytes.Join(frames, nil)
	size := len(body)
	h := []byte{'I', 'D', '3', major, 0, flags,
		byte(size >> 21 & 0x7F), byte(size >> 14 & 0x7F), byte(size >> 7 & 0x7F), byte(size & 0x7F)}
	return append(append(h, body...), "audio data"...)
}

func frame3(id string, data []byte) []byte {
	return append(append([]byte(id), binary.BigEndian.AppendUint32(nil, uint32(len(data)))...), append([]byte{0, 0}, data...)...)
}

func frame4(id string, flags uint16, data []byte) []byte {
	n := len(data)
	h := append([]byte(id), byte(n>>21&0x7F), byte(n>>14&0x7F), byte(n>>7&0x7F), byte(n&0x7F))
	return append(binary.BigEndian.AppendUint16(h, flags), data...)
}

func frame2(id string, data []byte) []byte {
	n := len(data)
	return append(append([]byte(id), byte(n>>16), byte(n>>8), byte(n)), data...)
}

func apic(pictureType byte, image string) []byte {
	return append([]byte("\x00image/jpeg\x00"+string(pictureType)+"desc\x00"), image...)
}

func TestReadID3v23(t *testing.T) {
	data := tag(3, 0,
		frame3("TIT2", []byte("\x00Le petit pompier")),
		frame3("APIC", apic(0, "OTHER")),
		frame3("APIC", apic(frontCover, "FRONT")),
		frame3("APIC", apic(4, "BACK")),
	)
	got, err := ReadID3(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Le petit pompier" || string(got.Cover) != "FRONT" {
		t.Fatalf("got %q / %q", got.Title, got.Cover)
	}
}

func TestReadID3v24UTF8AndFrameFlags(t *testing.T) {
	data := tag(4, 0,
		frame4("TIT2", 0, []byte("\x03Anaïg et les nuages\x00Second value")),
		// Unsynchronised frame with a data length indicator: 0xFF 0x00 -> 0xFF.
		frame4("APIC", 0x0003, append([]byte{0, 0, 0, 9}, apic(frontCover, "\xFF\x00\xD8")...)),
	)
	got, err := ReadID3(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Anaïg et les nuages" || !bytes.Equal(got.Cover, []byte{0xFF, 0xD8}) {
		t.Fatalf("got %q / % x", got.Title, got.Cover)
	}
}

func TestReadID3UTF16Title(t *testing.T) {
	le := []byte{1, 0xFF, 0xFE, 'L', 0, 'o', 0, 'u', 0, 'p', 0}
	be := []byte{2, 0, 'L', 0, 'o', 0, 'u', 0, 'p'}
	for _, text := range [][]byte{le, be} {
		got, _ := ReadID3(bytes.NewReader(tag(3, 0, frame3("TIT2", text))))
		if got.Title != "Loup" {
			t.Fatalf("title = %q", got.Title)
		}
	}
}

func TestReadID3v22(t *testing.T) {
	pic := append([]byte("\x00JPG\x03desc\x00"), "COVER"...)
	data := tag(2, 0, frame2("TT2", []byte("\x00Caf\xe9")), frame2("PIC", pic))
	got, err := ReadID3(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Café" || string(got.Cover) != "COVER" {
		t.Fatalf("got %q / %q", got.Title, got.Cover)
	}
}

func TestReadID3WholeTagUnsync(t *testing.T) {
	frame := frame3("APIC", apic(frontCover, "\xFF\xD8"))
	stuffed := bytes.ReplaceAll(frame, []byte{0xFF}, []byte{0xFF, 0x00})
	// The frame size field still describes the original (unstuffed) data.
	got, err := ReadID3(bytes.NewReader(tag(3, 0x80, stuffed)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Cover, []byte{0xFF, 0xD8}) {
		t.Fatalf("cover = % x", got.Cover)
	}
}

func TestReadID3NoTag(t *testing.T) {
	for _, in := range []string{"", "ID", "\xFF\xFB\x90\x00 mp3 frame data"} {
		got, err := ReadID3(bytes.NewReader([]byte(in)))
		if err != nil || got.Title != "" || got.Cover != nil {
			t.Fatalf("%q: got %+v, %v", in, got, err)
		}
	}
}

func TestReadID3TruncatedFrameIsIgnored(t *testing.T) {
	data := tag(3, 0, frame3("TIT2", []byte("\x00Titre")))
	// Corrupt the frame size so it claims more data than the tag holds.
	data[10+7] = 0xFF
	got, err := ReadID3(bytes.NewReader(data))
	if err != nil || got.Title != "" {
		t.Fatalf("got %+v, %v", got, err)
	}
}
