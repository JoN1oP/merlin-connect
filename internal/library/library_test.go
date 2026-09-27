package library

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"os"
	"reflect"
	"regexp"
	"testing"
	"testing/fstest"
	"time"

	"merlin-connect/internal/media"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 300, 200))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func open(t *testing.T) *Library {
	t.Helper()
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func titles(l *Library, parent string) []string {
	var out []string
	for _, id := range l.children(parent) {
		out = append(out, l.m.Items[l.index(id)].Title)
	}
	return out
}

func TestImportBuildsItemsAndMedia(t *testing.T) {
	l := open(t)
	cover := pngBytes(t)
	fsys := fstest.MapFS{
		"Contes/a.mp3":      {Data: []byte("audio-a")},
		"Contes/a.png":      {Data: cover},
		"Contes/b.mp3":      {Data: []byte("audio-b")},
		"Contes/Sous/c.mp3": {Data: []byte("audio-c")},
		"d.mp3":             {Data: []byte("audio-d")},
	}
	root, err := media.Scan(fsys)
	if err != nil {
		t.Fatal(err)
	}
	res, err := l.Import("official-folder", root, fsys)
	if err != nil {
		t.Fatal(err)
	}
	// Contes gets a's cover; b, c, d and "Sous" (no cover anywhere) get placeholders.
	if res != (ImportResult{Folders: 2, Stories: 4, Placeholders: 4}) {
		t.Fatalf("result = %+v", res)
	}
	if got := titles(l, "official-folder"); len(got) != 2 || got[0] != "Contes" || got[1] != "d" {
		t.Fatalf("top level = %v", got)
	}
	contes := l.m.Items[l.index(l.children("official-folder")[0])]
	if got := titles(l, contes.UUID); len(got) != 3 || got[0] != "Sous" || got[1] != "a" || got[2] != "b" {
		t.Fatalf("Contes children = %v", got)
	}
	a := l.m.Items[l.index(l.children(contes.UUID)[1])]
	data, err := os.ReadFile(l.AudioPath(a.UUID))
	if err != nil || string(data) != "audio-a" || a.Audio.Size != 7 {
		t.Fatalf("audio = %q, %v, %+v", data, err, a.Audio)
	}
	if _, err := os.Stat(l.ImagePath(a.UUID)); err != nil || a.Image == nil {
		t.Fatal("image not written")
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(a.UUID) {
		t.Fatalf("uuid = %s", a.UUID)
	}
}

func TestReimportSkipsExistingStories(t *testing.T) {
	l := open(t)
	fsys := fstest.MapFS{"Contes/a.mp3": {Data: []byte("audio-a")}}
	root, _ := media.Scan(fsys)
	if _, err := l.Import("", root, fsys); err != nil {
		t.Fatal(err)
	}
	fsys["Contes/b.mp3"] = &fstest.MapFile{Data: []byte("audio-b")}
	root, _ = media.Scan(fsys)
	res, err := l.Import("", root, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if res.Folders != 0 || res.Stories != 1 || res.Skipped != 1 {
		t.Fatalf("result = %+v", res)
	}
	if got := titles(l, ""); len(got) != 1 {
		t.Fatalf("root = %v, want one Contes folder", got)
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir)
	f, err := l.AddFolder("", "  Mes contes  ", nil)
	if err != nil || f.Title != "Mes contes" || f.Added == 0 {
		t.Fatalf("AddFolder = %+v, %v", f, err)
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := again.Manifest()
	if len(m.Items) != 1 || !reflect.DeepEqual(m.Items[0], f) || m.Version != ManifestVersion {
		t.Fatalf("reloaded = %+v", m)
	}
}

func TestRenameAndSetCover(t *testing.T) {
	l := open(t)
	f, _ := l.AddFolder("", "Avant", nil)
	if err := l.Rename(f.UUID, "  "); !errors.Is(err, ErrEmptyTitle) {
		t.Fatalf("err = %v", err)
	}
	if err := l.Rename(f.UUID, "Après"); err != nil || l.Manifest().Items[0].Title != "Après" {
		t.Fatalf("rename: %v", err)
	}
	before := *l.Manifest().Items[0].Image
	if err := l.SetCover(f.UUID, pngBytes(t)); err != nil {
		t.Fatal(err)
	}
	if *l.Manifest().Items[0].Image == before {
		t.Fatal("image blob unchanged")
	}
	if err := l.SetCover(f.UUID, []byte("junk")); err == nil {
		t.Fatal("want error for junk image")
	}
	if it, ok := l.Item(f.UUID); !ok || it.Title != "Après" {
		t.Fatalf("Item = %+v, %v", it, ok)
	}
	if _, ok := l.Item("nope"); ok {
		t.Fatal("Item found a missing uuid")
	}
	if err := l.Rename("nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestMoveReordersAndRefusesCycles(t *testing.T) {
	l := open(t)
	a, _ := l.AddFolder("", "A", nil)
	b, _ := l.AddFolder("", "B", nil)
	c, _ := l.AddFolder("", "C", nil)
	if err := l.Move(c.UUID, "", 0); err != nil {
		t.Fatal(err)
	}
	if got := titles(l, ""); got[0] != "C" || got[1] != "A" || got[2] != "B" {
		t.Fatalf("order = %v", got)
	}
	if err := l.Move(b.UUID, a.UUID, 99); err != nil {
		t.Fatal(err)
	}
	if got := titles(l, ""); len(got) != 2 || got[0] != "C" || got[1] != "A" {
		t.Fatalf("root = %v", got)
	}
	if err := l.Move(a.UUID, b.UUID, 0); !errors.Is(err, ErrBadParent) {
		t.Fatalf("moving into own child: %v", err)
	}
	if err := l.Move(a.UUID, a.UUID, 0); !errors.Is(err, ErrBadParent) {
		t.Fatalf("moving into itself: %v", err)
	}
}

func TestDeleteIsRecursive(t *testing.T) {
	l := open(t)
	cover := pngBytes(t)
	fsys := fstest.MapFS{"F/s.mp3": {Data: []byte("x")}, "F/s.png": {Data: cover}, "t.mp3": {Data: []byte("y")}}
	root, _ := media.Scan(fsys)
	if _, err := l.Import("", root, fsys); err != nil {
		t.Fatal(err)
	}
	folder := l.children("")[0]
	story := l.children(folder)[0]
	if err := l.Delete(folder); err != nil {
		t.Fatal(err)
	}
	if got := titles(l, ""); len(got) != 1 || got[0] != "t" {
		t.Fatalf("left = %v", got)
	}
	if l.Manifest().Items[0].Position != 0 {
		t.Fatal("siblings not renumbered")
	}
	if _, err := os.Stat(l.AudioPath(story)); !os.IsNotExist(err) {
		t.Fatal("child media not removed")
	}
}

func TestAdopt(t *testing.T) {
	l := open(t)
	m := Manifest{Version: ManifestVersion, Items: []Item{
		{UUID: "s1", Kind: Story, Title: "Loup", Audio: &Blob{Size: 1}, Image: &Blob{Size: 1}},
	}}
	fetched := map[string][]byte{"s1.mp3": []byte("M"), "s1.jpg": []byte("J")}
	err := l.Adopt(m, func(name string) ([]byte, error) { return fetched[name], nil })
	if err != nil {
		t.Fatal(err)
	}
	if l.Empty() {
		t.Fatal("library still empty")
	}
	if data, _ := os.ReadFile(l.AudioPath("s1")); string(data) != "M" {
		t.Fatalf("audio = %q", data)
	}
}

func TestNeverSaved(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir)
	if !l.NeverSaved() {
		t.Fatal("a new library is never saved")
	}
	f, _ := l.AddFolder("", "F", nil)
	l.Delete(f.UUID)
	if l.NeverSaved() {
		t.Fatal("deleting everything must not look like a fresh install")
	}
	again, _ := Open(dir)
	if again.NeverSaved() || !again.Empty() {
		t.Fatal("reopened empty library must not look fresh")
	}
}

func TestAdoptDoesNotBlockReaders(t *testing.T) {
	l := open(t)
	m := Manifest{Version: ManifestVersion, Items: []Item{{UUID: "s1", Kind: Story, Title: "Loup"}}}
	release := make(chan struct{})
	done := make(chan error)
	go func() {
		done <- l.Adopt(m, func(string) ([]byte, error) { <-release; return []byte("x"), nil })
	}()
	read := make(chan struct{})
	go func() { l.Manifest(); close(read) }()
	select {
	case <-read:
	case <-time.After(time.Second):
		t.Fatal("Manifest blocked while Adopt downloads media")
	}
	close(release)
	if err := <-done; err != nil || l.Empty() {
		t.Fatalf("adopt: %v, empty=%v", err, l.Empty())
	}
}

func TestParseManifestRejectsOtherVersions(t *testing.T) {
	if _, err := ParseManifest([]byte(`{"version":2,"items":[]}`)); err == nil {
		t.Fatal("want error")
	}
	if _, err := ParseManifest([]byte(`nope`)); err == nil {
		t.Fatal("want error")
	}
}
