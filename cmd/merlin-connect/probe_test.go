package main

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"merlin-connect/internal/box/boxtest"
	"merlin-connect/internal/playlist"
)

func TestProbeAgainstFakeBox(t *testing.T) {
	fake := boxtest.New()
	fake.SetTree(&playlist.Node{Children: []*playlist.Node{
		{UUID: "f", Title: "Histoires", Children: []*playlist.Node{{UUID: "s", Title: "Loup", Story: true}}},
	}})
	fake.Files["f.jpg"], fake.Files["s.jpg"], fake.Files["s.mp3"] = []byte("J"), []byte("J"), []byte("M")

	out := t.TempDir()
	code := probe([]string{"-join=false", "-addr", serve(t, fake), "-out", out,
		"-write-test", "-roundtrip", "-yes-i-have-a-backup"})
	if code != 0 {
		t.Fatalf("probe exit code %d", code)
	}
	for _, name := range []string{"playlist.bin", "cover.jpg", "roundtrip-1.json", "playlist-after.bin"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("missing %s", name)
		}
	}
	if big, ok := fake.File("merlin-connect-probe.bin"); !ok || len(big) < 1<<20 {
		t.Errorf("the write test must upload a realistic (multi-chunk) file, got %d bytes", len(big))
	}
}

// serve exposes fake on a local TCP port and returns its address.
func serve(t *testing.T, fake *boxtest.FakeBox) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go fake.Serve(conn)
		}
	}()
	return ln.Addr().String()
}

func TestProbeFailsOnRoundtripLoss(t *testing.T) {
	fake := boxtest.New()
	tree := &playlist.Node{Children: []*playlist.Node{{UUID: "s", Title: "Loup", Story: true}}}
	fake.SetTree(tree)
	fake.Files["s.jpg"], fake.Files["s.mp3"] = []byte("J"), []byte("M")
	// A favorite marker the JSON cannot express: the round trip loses it.
	raw := fake.Files["playlist.bin"]
	raw[152+8] = 1
	addr := serve(t, fake)
	code := probe([]string{"-join=false", "-addr", addr, "-out", t.TempDir(), "-roundtrip", "-yes-i-have-a-backup"})
	if code == 0 {
		t.Fatal("probe passed although the round trip lost a favorite")
	}
}

func TestProbeSearchesForTheAcceptedSchema(t *testing.T) {
	fake := boxtest.New()
	fake.SetTree(&playlist.Node{Children: []*playlist.Node{
		{UUID: "f", Title: "Histoires", Children: []*playlist.Node{{UUID: "s", Title: "Loup", Story: true}}},
	}})
	fake.Files["f.jpg"], fake.Files["s.jpg"], fake.Files["s.mp3"] = []byte("J"), []byte("J"), []byte("M")
	fake.RequireFields = []string{"uuid", "title", "add_time", "limit_time", "type"}
	code := probe([]string{"-join=false", "-addr", serve(t, fake), "-out", t.TempDir(), "-roundtrip", "-yes-i-have-a-backup"})
	if code != 0 {
		t.Fatalf("probe exit code %d", code)
	}
	sent, _ := fake.File("playlist-merlin-connect-probe.json")
	for _, field := range []string{`"type":"CATEGORY"`, `"type":"CONTENT"`, `"add_time":`, `"limit_time":`} {
		if !strings.Contains(string(sent), field) {
			t.Errorf("accepted JSON lacks %s: %s", field, sent)
		}
	}
}

func TestProbeMeasuresTitleLimitAndShortensLongTitles(t *testing.T) {
	fake := boxtest.New()
	fake.TitleLimit = 60
	long := strings.Repeat("Jolly Phonics ", 5)[:64] // 64 bytes: came from the SD card
	fake.SetTree(&playlist.Node{Children: []*playlist.Node{
		{UUID: "f", Title: "Histoires", Children: []*playlist.Node{{UUID: "s", Title: long, Story: true}}},
	}})
	fake.Files["f.jpg"], fake.Files["s.jpg"], fake.Files["s.mp3"] = []byte("J"), []byte("J"), []byte("M")
	code := probe([]string{"-join=false", "-addr", serve(t, fake), "-out", t.TempDir(), "-roundtrip", "-yes-i-have-a-backup"})
	if code != 0 {
		t.Fatalf("probe exit code %d", code)
	}
	tree, _ := fake.Tree()
	if got := tree.Find("s").Title; got != long[:60] {
		t.Fatalf("title on the box = %q, want it cut to 60 bytes", got)
	}
}

func TestMissingAssets(t *testing.T) {
	fake := boxtest.New()
	fake.Files["s1.jpg"], fake.Files["s1.mp3"], fake.Files["s2.jpg"] = []byte("J"), []byte("M"), []byte("J")
	tree := &playlist.Node{Children: []*playlist.Node{
		{UUID: "f", Title: "Folder", Children: []*playlist.Node{ // f.jpg missing
			{UUID: "s1", Title: "Complete", Story: true},
			{UUID: "s2", Title: "No audio", Story: true},
		}},
		{UUID: "fav", Title: playlist.FavoritesTitle, Children: []*playlist.Node{ // fav.jpg missing
			{UUID: "s1", Title: "Complete", Story: true}, // listed twice: checked once
		}},
	}}
	c := fake.Dial()
	defer c.Close()
	got, err := missingAssets(c, tree)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"f.jpg (folder Folder)", "s2.mp3 (story No audio)", "fav.jpg (folder Merlin_favorite)"}
	if !slices.Equal(got, want) {
		t.Fatalf("missing = %q, want %q", got, want)
	}
}

func TestProbeRoundtripNeedsBackupFlag(t *testing.T) {
	if code := probe([]string{"-join=false", "-roundtrip"}); code != 2 {
		t.Fatalf("exit code %d, want 2", code)
	}
}

func TestDiffRecords(t *testing.T) {
	tree := &playlist.Node{Children: []*playlist.Node{
		{UUID: "f", Title: "F", Children: []*playlist.Node{{UUID: "a", Title: "A", Story: true}}},
		{UUID: "b", Title: "B", Story: true},
	}}
	before, _ := playlist.Records(playlist.Encode(tree))
	if d := diffRecords(before, before); len(d) != 0 {
		t.Fatalf("identical: %v", d)
	}
	tree.Children = tree.Children[:1]
	tree.Children[0].Children = append(tree.Children[0].Children, &playlist.Node{UUID: "c", Title: "C", Story: true})
	after, _ := playlist.Records(playlist.Encode(tree))
	d := diffRecords(before, after)
	if len(d) != 4 { // B removed, C added, child counts of F and Root changed
		t.Fatalf("diffs = %v", d)
	}
}
