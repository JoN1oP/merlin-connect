package syncer

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"merlin-connect/internal/box/boxtest"
	"merlin-connect/internal/library"
	"merlin-connect/internal/media"
	"merlin-connect/internal/playlist"
)

// setup returns a fake box holding the official tree (with media) and a library
// with a story in "Histoires" and a folder "Contes" holding a big story.
func setup(t *testing.T) (*boxtest.FakeBox, *library.Library) {
	t.Helper()
	fake := boxtest.New()
	tree := official()
	tree.Children = []*playlist.Node{tree.Children[0], tree.Children[2]} // drop c-old
	fake.SetTree(tree)
	for _, id := range []string{"hist", "s1", "s2", "fav"} {
		fake.Files[id+".jpg"] = []byte("jpg")
	}
	fake.Files["s1.mp3"], fake.Files["s2.mp3"] = []byte("mp3"), []byte("mp3")

	lib, err := library.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	loup := fstest.MapFS{"Loup.mp3": {Data: []byte("loup audio")}}
	contes := fstest.MapFS{"Contes/Pirates.mp3": {Data: bytes.Repeat([]byte("p"), 250_000)}}
	// In order: tests expect Loup to be the first item.
	for _, in := range []struct {
		parent string
		fsys   fstest.MapFS
	}{{"hist", loup}, {"", contes}} {
		root, err := media.Scan(in.fsys)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lib.Import(in.parent, root, in.fsys); err != nil {
			t.Fatal(err)
		}
	}
	return fake, lib
}

func run(t *testing.T, fake *boxtest.FakeBox, lib *library.Library, boxItems []library.Item) (*playlist.Node, error) {
	t.Helper()
	c := fake.Dial()
	defer c.Close()
	data, err := c.GetFile(BoxPlaylist)
	if err != nil {
		t.Fatal(err)
	}
	boxTree, err := playlist.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan(boxTree, lib, boxItems)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), c, plan, func(Progress) {})
	if err == nil && !bytes.Equal(res.Raw, fake.Files[BoxPlaylist]) {
		t.Fatal("Result.Raw differs from the box's playlist.bin")
	}
	return res.Tree, err
}

func uploads(fake *boxtest.FakeBox) int {
	return bytes.Count(fake.Commands, []byte{1})
}

func TestSyncThenResyncThenRestore(t *testing.T) {
	fake, lib := setup(t)
	tree, err := run(t, fake, lib, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outline(tree); got != "Histoires(Au lit,Docteur,Loup),Merlin_favorite(),Contes(Pirates)" {
		t.Fatalf("box tree = %s", got)
	}
	m, err := library.ParseManifest(fake.Files[ManifestName])
	if err != nil || len(m.Items) != 3 {
		t.Fatalf("box manifest = %+v, %v", m, err)
	}
	if n := Pending(tree, lib.Manifest().Items, m.Items); n != 0 {
		t.Fatalf("pending after sync = %d", n)
	}

	// Nothing changed: only the manifest and playlist uploads happen.
	fake.Commands = nil
	if _, err := run(t, fake, lib, m.Items); err != nil {
		t.Fatal(err)
	}
	if n := uploads(fake); n != 2 || fake.Hashed != 0 {
		t.Fatalf("resync made %d upload calls and %d hashes, want 2 and 0", n, fake.Hashed)
	}

	// An official sync replaced the playlist; media files stayed on the card.
	wiped := official()
	wiped.Children = []*playlist.Node{wiped.Children[0], wiped.Children[2]}
	fake.SetTree(wiped)
	fake.Commands = nil
	tree, err = run(t, fake, lib, m.Items)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Find(lib.Manifest().Items[0].UUID) == nil || uploads(fake) != 2 {
		t.Fatalf("restore: tree %s, %d uploads", outline(tree), uploads(fake))
	}
}

func TestFilesAlreadyOnTheBoxAreNotResent(t *testing.T) {
	fake, lib := setup(t)
	if _, err := run(t, fake, lib, nil); err != nil {
		t.Fatal(err)
	}
	// An official sync dropped our items and the manifest, but not the media.
	wiped := official()
	wiped.Children = []*playlist.Node{wiped.Children[0], wiped.Children[2]}
	fake.SetTree(wiped)
	delete(fake.Files, ManifestName)
	loup := lib.Manifest().Items[0].UUID + ".mp3"
	same := bytes.Clone(fake.Files[loup])
	same[0] ^= 1 // same size, other content: must be sent again
	fake.Files[loup] = same

	fake.Commands, fake.Hashed = nil, 0
	var last Progress
	c := fake.Dial()
	defer c.Close()
	plan, err := NewPlan(wiped, lib, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), c, plan, func(p Progress) {
		if p.Phase == "files" {
			last = p
		}
	}); err != nil {
		t.Fatal(err)
	}
	// Without the manifest every file is hashed once; only Loup's audio is sent.
	if uploads(fake) != 3 || fake.Hashed != 5 {
		t.Fatalf("%d uploads, %d hashes; want 3 and 5", uploads(fake), fake.Hashed)
	}
	if last.File != 5 || last.Files != 5 || last.Sent != int64(len(same)) {
		t.Fatalf("last progress = %+v", last)
	}
}

func TestInterruptedSyncResumes(t *testing.T) {
	fake, lib := setup(t)
	fake.CutUploadAfter = 100_000 // only the 250 KB story is big enough to be cut
	before, _ := fake.File(BoxPlaylist)
	if _, err := run(t, fake, lib, nil); err == nil {
		t.Fatal("want error from the cut connection")
	}
	if after, _ := fake.File(BoxPlaylist); !bytes.Equal(after, before) {
		t.Fatal("playlist must not be written after a failed file upload")
	}
	if _, err := run(t, fake, lib, nil); err != nil {
		t.Fatalf("resume: %v", err)
	}
}

func TestPlanLimits(t *testing.T) {
	_, lib := setup(t)
	big := &playlist.Node{}
	for range MaxItems {
		big.Children = append(big.Children, &playlist.Node{UUID: "x", Title: "x", Story: true})
	}
	if _, err := NewPlan(big, lib, nil); !errors.Is(err, ErrTooManyItems) {
		t.Fatalf("err = %v", err)
	}
	plan, err := NewPlan(&playlist.Node{}, lib, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Loup (jpg+mp3), Contes (jpg), Pirates (jpg+mp3).
	if len(plan.Files) != 5 || plan.Bytes() < 250_000 {
		t.Fatalf("files = %d, bytes = %d", len(plan.Files), plan.Bytes())
	}
}

func TestPlanRefusesUnknownBoxContent(t *testing.T) {
	_, lib := setup(t)
	// Without the box's playlist the official part would be empty and the sync
	// would replace everything on the box with the custom items.
	if _, err := NewPlan(nil, lib, nil); !errors.Is(err, ErrBoxUnread) {
		t.Fatalf("err = %v, want ErrBoxUnread", err)
	}
}

func TestCancelledSync(t *testing.T) {
	fake, lib := setup(t)
	c := fake.Dial()
	defer c.Close()
	plan, _ := NewPlan(&playlist.Node{}, lib, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, c, plan, func(Progress) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
