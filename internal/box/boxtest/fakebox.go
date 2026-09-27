// Package boxtest provides an in-memory Merlin box for tests.
package boxtest

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"sync"
	"time"

	"merlin-connect/internal/box"
	"merlin-connect/internal/playlist"
)

// FakeBox speaks the box protocol over net.Pipe and stores files in memory.
// Its updatePlaylist turns the uploaded JSON into playlist.bin, checking that
// every node has its image and every story its audio, like the real box.
type FakeBox struct {
	mu       sync.Mutex
	Files    map[string][]byte
	Battery  byte
	Free     uint32
	Commands []byte // every command id received, in order
	// CutUploadAfter, when > 0, drops the connection once that many bytes of a
	// file upload have been received (simulates a lost connection).
	CutUploadAfter int
	// RequireFields makes updatePlaylist answer MISSING_FIELD (9) when a node of
	// the playlist JSON lacks one of these keys. It defaults to what the real
	// box requires.
	RequireFields []string
	// TitleLimit is the longest title JSON text updatePlaylist accepts (bytes).
	TitleLimit int
	// GetFileDelay slows every getFile reply down (a real box takes seconds).
	GetFileDelay time.Duration
	// Hashed counts searchFile calls that asked the box to hash a file (slow on
	// the real box).
	Hashed int
}

// New returns an empty box with 80 % battery and 1 GiB free.
func New() *FakeBox {
	return &FakeBox{Files: map[string][]byte{}, Battery: 80, Free: 1 << 30,
		RequireFields: []string{"uuid", "title", "add_time", "limit_time"}, TitleLimit: playlist.MaxTitleBytes}
}

// SetTree stores tree as the box's playlist.bin.
func (f *FakeBox) SetTree(tree *playlist.Node) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Files["playlist.bin"] = playlist.Encode(tree)
}

// Tree decodes the box's current playlist.bin.
func (f *FakeBox) Tree() (*playlist.Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return playlist.Decode(f.Files["playlist.bin"])
}

// File returns a stored file and whether it exists.
func (f *FakeBox) File(name string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.Files[name]
	return data, ok
}

// Dial returns a client connected to this box.
func (f *FakeBox) Dial() *box.Client {
	server, client := net.Pipe()
	go f.Serve(server)
	return box.NewClient(client)
}

// Serve answers commands on conn until it closes.
func (f *FakeBox) Serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	for {
		cmd, payload, err := box.ReadFrame(r)
		if err != nil {
			return
		}
		if err := f.handle(conn, r, cmd, payload); err != nil {
			return
		}
	}
}

func (f *FakeBox) handle(w io.Writer, r io.Reader, cmd byte, p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Commands = append(f.Commands, cmd)
	reply := func(payload ...byte) error { return box.WriteFrame(w, cmd, payload) }
	u32 := func(v uint32) error { return reply(binary.LittleEndian.AppendUint32(nil, v)...) }

	switch cmd {
	case 2, 9: // ping, endSynchronization
		return reply()
	case 5: // firmware 2.1.16
		return reply(2, 1, 16, 0)
	case 30:
		return reply(2, 0, 0, 0, 0, 1)
	case 14:
		return reply(f.Battery, 0)
	case 4:
		return u32(0xFFFFFFFE)
	case 3:
		return u32(f.Free)
	case 31: // searchFile: [flag][name]; flag 1 asks for the hash
		if p[0] == 1 {
			f.Hashed++
		}
		return f.replyHeader(w, cmd, string(p[1:]), false)
	case 13: // getFile: [name]
		time.Sleep(f.GetFileDelay)
		return f.replyHeader(w, cmd, string(p), true)
	case 1:
		return f.upload(w, r, p)
	case 6:
		return reply(f.updatePlaylist(string(p)))
	default:
		return box.WriteFrame(w, 255, []byte{0})
	}
}

func (f *FakeBox) replyHeader(w io.Writer, cmd byte, name string, withData bool) error {
	data, ok := f.Files[name]
	if !ok {
		return box.WriteFrame(w, cmd, []byte{1})
	}
	sum := sha256.Sum256(data)
	p := append([]byte{0, byte(len(name))}, name...)
	p = binary.LittleEndian.AppendUint32(p, uint32(len(data)))
	p = append(p, sum[:]...)
	if err := box.WriteFrame(w, cmd, p); err != nil {
		return err
	}
	if withData {
		_, err := w.Write(data)
		return err
	}
	return nil
}

func (f *FakeBox) upload(w io.Writer, r io.Reader, p []byte) error {
	n := int(p[0])
	name := string(p[1 : 1+n])
	size := int(binary.LittleEndian.Uint32(p[1+n:]))
	var want [32]byte
	copy(want[:], p[5+n:])
	reply := func(status byte) error { return box.WriteFrame(w, 1, []byte{status}) }

	if old, ok := f.Files[name]; ok && sha256.Sum256(old) == want {
		return reply(1)
	}
	if uint32(size) > f.Free {
		return reply(box.UploadNoSpace)
	}
	if err := reply(0); err != nil {
		return err
	}
	data := make([]byte, size)
	if f.CutUploadAfter > 0 && size > f.CutUploadAfter {
		io.ReadFull(r, data[:f.CutUploadAfter])
		f.CutUploadAfter = 0
		return io.ErrUnexpectedEOF // Serve closes the connection
	}
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	if sha256.Sum256(data) != want {
		return reply(4)
	}
	f.Files[name] = data
	f.Free -= uint32(size)
	return reply(1)
}

func (f *FakeBox) updatePlaylist(name string) byte {
	data, ok := f.Files[name]
	if !ok {
		return 3
	}
	if status := checkSchema(data, f.RequireFields, f.TitleLimit); status != 0 {
		return status
	}
	tree, err := playlist.ParseBoxJSON(data)
	if err != nil {
		return 5
	}
	status := byte(0)
	tree.Walk(func(n, _ *playlist.Node) {
		switch {
		case status != 0:
		case f.Files[n.UUID+".jpg"] == nil:
			status = box.PlaylistImageMissing
		case n.Story && f.Files[n.UUID+".mp3"] == nil:
			status = box.PlaylistAudioMissing
		}
	})
	if status == 0 {
		f.Files["playlist.bin"] = playlist.Encode(tree)
	}
	return status
}

// checkSchema answers like the real box: MISSING_FIELD (9) when a node lacks a
// required key, TITLE_TOO_LARGE (13) when a title's JSON text (escapes
// included) exceeds titleLimit bytes, 0 otherwise. Nodes are checked in order.
func checkSchema(data []byte, required []string, titleLimit int) byte {
	var nodes []map[string]json.RawMessage
	if json.Unmarshal(data, &nodes) != nil {
		return 0 // ParseBoxJSON reports bad JSON
	}
	for _, n := range nodes {
		for _, key := range required {
			if _, ok := n[key]; !ok {
				return 9
			}
		}
		if title := n["title"]; len(title)-2 > titleLimit {
			return 13
		}
		if child, ok := n["child"]; ok {
			if status := checkSchema(child, required, titleLimit); status != 0 {
				return status
			}
		}
	}
	return 0
}

// SetBattery changes the battery level the box reports.
func (f *FakeBox) SetBattery(level byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Battery = level
}
