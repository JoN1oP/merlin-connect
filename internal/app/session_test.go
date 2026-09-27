package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"merlin-connect/internal/box"
	"merlin-connect/internal/box/boxtest"
	"merlin-connect/internal/library"
	"merlin-connect/internal/media"
	"merlin-connect/internal/playlist"
	"merlin-connect/internal/syncer"
	"merlin-connect/internal/wifi"
)

// fakeJoiner makes the box visible after `hiddenScans` scans.
type fakeJoiner struct {
	mu          sync.Mutex
	hiddenScans int
	failJoins   int // joins that fail first (stale scan result, box not up yet)
	joined      bool
	left        []string
}

func (j *fakeJoiner) Scan(context.Context) ([]wifi.AP, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.hiddenScans > 0 {
		j.hiddenScans--
		return nil, nil
	}
	return []wifi.AP{{SSID: "MERLIN_TEST", Signal: 80}}, nil
}

func (j *fakeJoiner) Join(context.Context, string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failJoins > 0 {
		j.failJoins--
		return errors.New("Error: The Wi-Fi network could not be found")
	}
	j.joined = true
	return nil
}

func (j *fakeJoiner) Leave(ssid string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.joined, j.left = false, append(j.left, ssid)
	return nil
}

func (j *fakeJoiner) isJoined() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.joined
}

type harness struct {
	fake   *boxtest.FakeBox
	joiner *fakeJoiner
	lib    *library.Library
	s      *Session
	mu     sync.Mutex
	server net.Conn // box side of the last connection
}

func newHarness(t *testing.T, joiner wifi.Joiner) *harness {
	t.Helper()
	h := &harness{fake: boxtest.New()}
	h.fake.SetTree(&playlist.Node{Children: []*playlist.Node{
		{UUID: "hist", Title: "Histoires", Children: []*playlist.Node{{UUID: "s1", Title: "Au lit", Story: true}}},
	}})
	h.fake.Files["hist.jpg"], h.fake.Files["s1.jpg"], h.fake.Files["s1.mp3"] = []byte("J"), []byte("J"), []byte("M")
	var err error
	if h.lib, err = library.Open(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if joiner == nil {
		h.joiner = &fakeJoiner{hiddenScans: 1}
		joiner = h.joiner
	}
	h.s = New(Config{
		Library:      h.lib,
		Joiner:       joiner,
		CacheDir:     t.TempDir(),
		ScanEvery:    5 * time.Millisecond,
		ScanTimeout:  200 * time.Millisecond,
		PingEvery:    10 * time.Millisecond,
		BatteryEvery: 20 * time.Millisecond,
		Dial:         h.dial,
	})
	t.Cleanup(h.s.Close)
	return h
}

// dial reaches the fake box only once the fake joiner has joined its network
// (or always, when the test uses the manual joiner).
func (h *harness) dial(context.Context) (*box.Client, error) {
	if h.joiner != nil && !h.joiner.isJoined() {
		return nil, errors.New("unreachable")
	}
	server, client := net.Pipe()
	h.mu.Lock()
	h.server = server
	h.mu.Unlock()
	go h.fake.Serve(server)
	return box.NewClient(client), nil
}

// waitFor waits until the session reaches state.
func waitFor(t *testing.T, s *Session, state State) Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st := s.Status(); st.State == state && (state != Connected || !st.Seen.IsZero()) {
			return st
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("state = %+v, want %s", s.Status(), state)
	return Status{}
}

func addStory(t *testing.T, lib *library.Library, parent string) {
	t.Helper()
	fsys := fstest.MapFS{"Loup.mp3": {Data: []byte("loup")}}
	root, _ := media.Scan(fsys)
	if _, err := lib.Import(parent, root, fsys); err != nil {
		t.Fatal(err)
	}
}

func TestConnectSyncDisconnect(t *testing.T) {
	h := newHarness(t, nil)
	updates, stop := h.s.Subscribe()
	defer stop()
	h.s.Connect()
	st := waitFor(t, h.s, Connected)
	if st.SSID != "MERLIN_TEST" || st.Info == nil || st.Info.Firmware != "2.1.16" {
		t.Fatalf("status = %+v", st)
	}
	select {
	case <-updates:
	default:
		t.Fatal("no update signalled")
	}
	if v := h.s.View(); len(v.Children) != 1 || v.Children[0].Custom || !v.Children[0].OnBox {
		t.Fatalf("view = %+v", v.Children)
	}

	addStory(t, h.lib, "hist")
	h.s.LibraryChanged()
	if st := h.s.Status(); st.Pending != 1 {
		t.Fatalf("pending = %d", st.Pending)
	}
	if err := h.s.Sync(); err != nil {
		t.Fatal(err)
	}
	// Sync switches to Syncing before returning, so Connected now means done.
	st = waitFor(t, h.s, Connected)
	if st.Pending != 0 || st.Error != "" {
		t.Fatalf("after sync = %+v", st)
	}
	if st.Info.FreeBytes >= 1<<30 {
		t.Fatalf("free space not refreshed after sync: %d", st.Info.FreeBytes)
	}
	loup := h.s.View().Children[0].Children[1]
	if !loup.Custom || !loup.OnBox || loup.Title != "Loup" {
		t.Fatalf("custom story = %+v", loup)
	}

	h.s.Disconnect()
	if st := h.s.Status(); st.State != Offline || st.SSID != "" {
		t.Fatalf("after disconnect = %+v", st)
	}
	if !slices.Contains(h.fake.Commands, 9) || !slices.Equal(h.joiner.left, []string{"MERLIN_TEST"}) {
		t.Fatalf("endSync/leave missing: cmds %v, left %v", h.fake.Commands, h.joiner.left)
	}
	time.Sleep(30 * time.Millisecond) // a stale keepalive would flip the state to Lost
	if st := h.s.Status(); st.State != Offline {
		t.Fatalf("state changed after disconnect: %+v", st)
	}
}

func TestJoinRetriesAfterStaleScan(t *testing.T) {
	// Real box, 2026-09-26: NetworkManager listed MERLIN_ from a stale scan and
	// the first joins failed until the box's network was really up.
	h := newHarness(t, nil)
	h.joiner.failJoins = 2
	h.s.Connect()
	waitFor(t, h.s, Connected)
}

func TestConnectShowsEachStep(t *testing.T) {
	// Real box, 2026-09-27: the header fell back to "hold the button" between
	// join attempts, and said "connected" while the content was still loading.
	h := newHarness(t, nil)
	h.joiner.failJoins = 2
	h.fake.GetFileDelay = 20 * time.Millisecond
	ch, stop := h.s.Subscribe()
	defer stop()
	var states []State
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range ch {
			st := h.s.Status().State
			if len(states) == 0 || states[len(states)-1] != st {
				states = append(states, st)
			}
			if st == Connected {
				return
			}
		}
	}()
	h.s.Connect()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("never connected")
	}
	// The recorder may miss the brief "waiting" at the start, nothing after it.
	if got := strings.TrimPrefix(fmt.Sprint(states), "[waiting "); got != "joining reading connected]" {
		t.Fatalf("states = %v", states)
	}
}

func TestBatteryIsRefreshed(t *testing.T) {
	h := newHarness(t, nil)
	h.s.Connect()
	if st := waitFor(t, h.s, Connected); st.Info.Battery != 80 {
		t.Fatalf("battery = %d", st.Info.Battery)
	}
	h.fake.SetBattery(71)
	deadline := time.Now().Add(2 * time.Second)
	for h.s.Status().Info.Battery != 71 {
		if time.Now().After(deadline) {
			t.Fatalf("battery still %d", h.s.Status().Info.Battery)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestLostConnection(t *testing.T) {
	h := newHarness(t, nil)
	h.s.Connect()
	waitFor(t, h.s, Connected)
	h.mu.Lock()
	h.server.Close()
	h.mu.Unlock()
	st := waitFor(t, h.s, Lost)
	if st.Error == "" {
		t.Fatal("lost without a message")
	}
	h.s.Connect() // reconnect works from Lost
	waitFor(t, h.s, Connected)
}

func TestBoxNotFound(t *testing.T) {
	h := newHarness(t, nil)
	h.joiner.hiddenScans = 1000
	h.s.Connect()
	st := waitFor(t, h.s, Offline)
	if st.Error != errNotFound.Error() {
		t.Fatalf("error = %q", st.Error)
	}
}

func TestCancelConnect(t *testing.T) {
	h := newHarness(t, nil)
	h.joiner.hiddenScans = 1000
	h.s.Connect()
	h.s.Cancel()
	if st := waitFor(t, h.s, Offline); st.Error != "" {
		t.Fatalf("cancel shows an error: %q", st.Error)
	}
}

func TestManualJoin(t *testing.T) {
	h := newHarness(t, wifi.Manual{}) // dial always succeeds: the user joined by hand
	h.s.Connect()
	if st := waitFor(t, h.s, Connected); st.SSID != "" {
		t.Fatalf("status = %+v", st)
	}
}

func TestManualJoinHint(t *testing.T) {
	lib, _ := library.Open(t.TempDir())
	s := New(Config{
		Library: lib, Joiner: wifi.Manual{}, CacheDir: t.TempDir(),
		ScanEvery: 5 * time.Millisecond, ScanTimeout: time.Second,
		Dial: func(context.Context) (*box.Client, error) { return nil, errors.New("unreachable") },
	})
	s.Connect()
	defer s.Cancel()
	deadline := time.Now().Add(time.Second)
	for !s.Status().Manual && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if st := s.Status(); st.State != Waiting || !st.Manual {
		t.Fatalf("status = %+v, want waiting with the manual-join hint", st)
	}
}

func TestAdoptOnFreshInstall(t *testing.T) {
	h := newHarness(t, nil)
	m := library.Manifest{Version: library.ManifestVersion, Items: []library.Item{
		{UUID: "c1", Kind: library.Story, Title: "Custom", Parent: "hist"},
	}}
	data, _ := m.JSON()
	h.fake.Files[syncer.ManifestName], h.fake.Files["c1.mp3"], h.fake.Files["c1.jpg"] = data, []byte("M"), []byte("J")
	h.s.Connect()
	waitFor(t, h.s, Connected)
	if h.lib.Empty() {
		t.Fatal("box manifest not adopted")
	}
	if cover, err := h.s.Cover("c1"); err != nil || string(cover) != "J" {
		t.Fatalf("cover = %q, %v", cover, err)
	}
}

func TestCoversFetchedInBackground(t *testing.T) {
	h := newHarness(t, nil)
	tree, _ := h.fake.Tree()
	tree.Children[0].Children = append(tree.Children[0].Children, &playlist.Node{UUID: "s2", Title: "Sans image", Story: true})
	h.fake.SetTree(tree)
	h.s.Connect()
	waitFor(t, h.s, Connected)
	for _, uuid := range []string{"hist", "s1"} {
		waitCover(t, h.s, uuid)
		if cover, err := h.s.Cover(uuid); err != nil || string(cover) != "J" {
			t.Fatalf("cover %s = %q, %v", uuid, cover, err)
		}
	}
	if n := findNode(h.s.View(), "s2"); n == nil || n.Version != "" {
		t.Fatalf("s2 = %+v, want no cover version", n)
	}
	if _, err := h.s.Cover("s2"); !errors.Is(err, ErrNoCover) {
		t.Fatalf("missing cover: %v", err)
	}
}

// waitCover waits until the view shows a cover for uuid.
func waitCover(t *testing.T, s *Session, uuid string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n := findNode(s.View(), uuid); n != nil && n.Version != "" {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no cover for %s", uuid)
}

func findNode(n *Node, uuid string) *Node {
	if n.UUID == uuid {
		return n
	}
	for _, c := range n.Children {
		if found := findNode(c, uuid); found != nil {
			return found
		}
	}
	return nil
}

func TestSyncRefusedWhenBoxContentUnread(t *testing.T) {
	h := newHarness(t, nil)
	// An unreadable manifest makes the refresh fail after connecting.
	h.fake.Files[syncer.ManifestName] = []byte(`{"version":2}`)
	addStory(t, h.lib, "")
	h.s.Connect()
	deadline := time.Now().Add(2 * time.Second)
	for h.s.Status().Error == "" && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if st := h.s.Status(); st.State != Connected || st.Error != Message(syncer.ErrBoxUnread) {
		t.Fatalf("status = %+v, want the French unread message", st)
	}
	if err := h.s.Sync(); !errors.Is(err, syncer.ErrBoxUnread) {
		t.Fatalf("Sync err = %v, want ErrBoxUnread", err)
	}
	if tree, _ := h.fake.Tree(); tree.Find("s1") == nil {
		t.Fatal("official content was removed from the box")
	}
}

func TestDeletedStoriesAreNotReadopted(t *testing.T) {
	h := newHarness(t, nil)
	addStory(t, h.lib, "")
	h.s.Connect()
	waitFor(t, h.s, Connected)
	if err := h.s.Sync(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, h.s, Connected)
	h.s.Disconnect()
	h.lib.Delete(h.lib.Manifest().Items[0].UUID)
	h.s.Connect()
	st := waitFor(t, h.s, Connected)
	if !h.lib.Empty() || st.Pending != 1 {
		t.Fatalf("library empty=%v pending=%d: the deleted story came back", h.lib.Empty(), st.Pending)
	}
}

func TestLinkLostDuringSync(t *testing.T) {
	h := newHarness(t, nil)
	fsys := fstest.MapFS{"Long.mp3": {Data: make([]byte, 300_000)}}
	root, _ := media.Scan(fsys)
	h.lib.Import("", root, fsys)
	h.s.Connect()
	waitFor(t, h.s, Connected)
	h.fake.CutUploadAfter = 100_000
	if err := h.s.Sync(); err != nil {
		t.Fatal(err)
	}
	st := waitFor(t, h.s, Lost)
	if st.Error != "Connexion avec Merlin perdue." {
		t.Fatalf("error = %q", st.Error)
	}
	if tree, _ := h.fake.Tree(); tree.Count() != 2 {
		t.Fatal("the box playlist changed during a failed sync")
	}
}

func TestOfflineViewFromCache(t *testing.T) {
	h := newHarness(t, nil)
	h.s.Connect()
	waitFor(t, h.s, Connected)
	waitCover(t, h.s, "s1")
	h.s.Disconnect()
	again := New(Config{Library: h.lib, Joiner: h.joiner, CacheDir: h.s.cfg.CacheDir, Dial: h.dial})
	if v := again.View(); len(v.Children) != 1 || again.Status().Seen.IsZero() {
		t.Fatalf("cached view = %+v", v)
	}
	if n := findNode(again.View(), "s1"); n == nil || n.Version == "" {
		t.Fatalf("s1 = %+v, want its cached cover", n)
	}
	if cover, err := again.Cover("s1"); err != nil || string(cover) != "J" {
		t.Fatalf("cached cover = %q, %v", cover, err)
	}
}

func TestSyncRefusedWhenOfflineOrLowBattery(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.Sync(); !errors.Is(err, errOffline) {
		t.Fatalf("offline: %v", err)
	}
	h.fake.Battery = 10
	h.s.Connect()
	waitFor(t, h.s, Connected)
	if err := h.s.Sync(); !errors.Is(err, errLowBattery) {
		t.Fatalf("low battery: %v", err)
	}
}
