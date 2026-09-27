package box_test

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"merlin-connect/internal/box"
	"merlin-connect/internal/box/boxtest"
	"merlin-connect/internal/playlist"
)

func TestPingAndInfo(t *testing.T) {
	fake := boxtest.New()
	c := fake.Dial()
	defer c.Close()
	if err := c.Ping(); err != nil {
		t.Fatal(err)
	}
	info, err := c.Info()
	if err != nil {
		t.Fatal(err)
	}
	want := box.Info{Firmware: "2.1.16", MAC: "02:00:00:00:00:01", Battery: 80,
		TotalBytes: 0xFFFFFFFE, FreeBytes: 1 << 30}
	if info != want {
		t.Fatalf("Info = %+v, want %+v", info, want)
	}
}

func TestUploadStatAndGetFile(t *testing.T) {
	fake := boxtest.New()
	c := fake.Dial()
	defer c.Close()
	data := bytes.Repeat([]byte("merlin"), 50_000) // 300 KB: several chunks
	var sent []int64
	if err := c.Upload("a.mp3", data, func(n int64) { sent = append(sent, n) }); err != nil {
		t.Fatal(err)
	}
	if len(sent) < 3 || sent[len(sent)-1] != int64(len(data)) {
		t.Fatalf("progress = %v", sent)
	}
	st, err := c.Stat("a.mp3")
	if err != nil || st.Size != int64(len(data)) || fake.Hashed != 0 {
		t.Fatalf("Stat = %+v, %v (hashed %d)", st, err, fake.Hashed)
	}
	st, err = c.Hash("a.mp3", int64(len(data)))
	if err != nil || st.Size != int64(len(data)) || st.SHA256 != sha256.Sum256(data) || fake.Hashed != 1 {
		t.Fatalf("Hash = %+v, %v (hashed %d)", st, err, fake.Hashed)
	}
	got, err := c.GetFile("a.mp3")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("GetFile = %d bytes, %v", len(got), err)
	}
}

func TestUploadSkipsIdenticalFile(t *testing.T) {
	fake := boxtest.New()
	fake.Files["a.jpg"] = []byte("jpeg")
	c := fake.Dial()
	defer c.Close()
	var last int64
	if err := c.Upload("a.jpg", []byte("jpeg"), func(n int64) { last = n }); err != nil {
		t.Fatal(err)
	}
	if last != 4 {
		t.Fatalf("progress = %d, want 4", last)
	}
}

func TestMissingFile(t *testing.T) {
	c := boxtest.New().Dial()
	defer c.Close()
	if _, err := c.Stat("nope.mp3"); !errors.Is(err, box.ErrNotFound) {
		t.Fatalf("Stat err = %v", err)
	}
	if _, err := c.GetFile("nope.mp3"); !errors.Is(err, box.ErrNotFound) {
		t.Fatalf("GetFile err = %v", err)
	}
}

func TestUploadNoSpace(t *testing.T) {
	fake := boxtest.New()
	fake.Free = 3
	c := fake.Dial()
	defer c.Close()
	err := c.Upload("a.mp3", []byte("four"), nil)
	var se *box.StatusError
	if !errors.As(err, &se) || se.Code != box.UploadNoSpace {
		t.Fatalf("err = %v", err)
	}
	if se.Error() != "box: upload failed: NOT_ENOUGH_SPACE (2)" {
		t.Fatalf("message = %q", se.Error())
	}
}

func TestUpdatePlaylist(t *testing.T) {
	fake := boxtest.New()
	c := fake.Dial()
	defer c.Close()
	tree := &playlist.Node{Children: []*playlist.Node{{UUID: "s1", Title: "Le loup", Story: true}}}
	js, _ := tree.BoxJSON()
	if err := c.Upload("p.json", js, nil); err != nil {
		t.Fatal(err)
	}
	var se *box.StatusError
	if err := c.UpdatePlaylist("p.json"); !errors.As(err, &se) || se.Code != box.PlaylistImageMissing {
		t.Fatalf("want image missing, got %v", err)
	}
	fake.Files["s1.jpg"], fake.Files["s1.mp3"] = []byte("j"), []byte("m")
	if err := c.UpdatePlaylist("p.json"); err != nil {
		t.Fatal(err)
	}
	got, _ := fake.Tree()
	if got.Find("s1") == nil {
		t.Fatal("playlist not applied")
	}
	if err := c.EndSync(); err != nil {
		t.Fatal(err)
	}
}

func TestFakeBoxEnforcesTheRealSchema(t *testing.T) {
	fake := boxtest.New()
	c := fake.Dial()
	defer c.Close()
	fake.Files["s.jpg"], fake.Files["s.mp3"] = []byte("J"), []byte("M")
	cases := map[string]byte{
		`[{"uuid":"s","title":"Loup"}]`: 9, // MISSING_FIELD: no add_time/limit_time
		`[{"uuid":"s","title":"` + strings.Repeat("a", 59) + `\u0026","add_time":0,"limit_time":0}]`: 13, // TITLE_TOO_LARGE
		`[{"uuid":"s","title":"` + strings.Repeat("a", 63) + `","add_time":0,"limit_time":0}]`:       0,
		`[{"uuid":"s","title":"` + strings.Repeat("a", 64) + `","add_time":0,"limit_time":0}]`:       13, // real box limit: 63
		// Nodes are checked in order: a fitting title, then a missing field.
		`[{"uuid":"s","title":"ok","add_time":0,"limit_time":0},{"uuid":"s","title":"x"}]`: 9,
	}
	for js, want := range cases {
		c.Upload("p.json", []byte(js), nil)
		err := c.UpdatePlaylist("p.json")
		var se *box.StatusError
		if got := byte(0); errors.As(err, &se) {
			got = se.Code
			if got != want {
				t.Errorf("%.40s…: status %d, want %d", js, got, want)
			}
		} else if err != nil || want != 0 {
			t.Errorf("%.40s…: err %v, want status %d", js, err, want)
		}
	}
}

func TestClosedConnectionFails(t *testing.T) {
	c := boxtest.New().Dial()
	c.Close()
	if err := c.Ping(); err == nil {
		t.Fatal("want error on closed connection")
	}
}

func TestStatWithoutSize(t *testing.T) {
	// The real box's reply to a search without hash is unverified: a bare
	// "found" status must still count as found, size unknown.
	server, client := net.Pipe()
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		if _, _, err := box.ReadFrame(r); err == nil {
			box.WriteFrame(server, 31, []byte{0})
		}
	}()
	c := box.NewClient(client)
	defer c.Close()
	st, err := c.Stat("a.mp3")
	if err != nil || st.Size != -1 {
		t.Fatalf("Stat = %+v, %v", st, err)
	}
}

func TestHashWaitsLongerForBigFiles(t *testing.T) {
	// Real box, 2026-09-27: hashing a 22 MB story takes ~15 s, longer than the
	// usual reply timeout; the link was reported lost mid-sync.
	server, client := net.Pipe()
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		if _, _, err := box.ReadFrame(r); err != nil {
			return
		}
		time.Sleep(150 * time.Millisecond) // hashing
		p := append([]byte{0, 5}, "a.mp3"...)
		box.WriteFrame(server, 31, append(p, make([]byte, 36)...))
	}()
	c := box.NewClient(client)
	defer c.Close()
	c.Timeout = 50 * time.Millisecond
	if _, err := c.Hash("a.mp3", 2_000_000); err != nil {
		t.Fatalf("Hash of a 2 MB file within its wait: %v", err)
	}
}
