// Package app runs a session with the box: connecting, keeping the link alive,
// caching what the box holds, and syncing the library to it.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"merlin-connect/internal/box"
	"merlin-connect/internal/library"
	"merlin-connect/internal/playlist"
	"merlin-connect/internal/syncer"
	"merlin-connect/internal/wifi"
)

// State is the connection state shown in the header.
type State string

const (
	Offline   State = "offline"
	Waiting   State = "waiting" // looking for the box network
	Joining   State = "joining"
	Reading   State = "reading" // linked, reading the box content
	Connected State = "connected"
	Syncing   State = "syncing"
	Lost      State = "lost"
)

// Status is everything the header and footer show.
type Status struct {
	State    State            `json:"state"`
	SSID     string           `json:"ssid,omitempty"`
	Info     *box.Info        `json:"info,omitempty"`
	Progress *syncer.Progress `json:"progress,omitempty"`
	Pending  int              `json:"pending"`
	Seen     time.Time        `json:"seen,omitzero"`    // when the box content was last read
	Manual   bool             `json:"manual,omitempty"` // join the box WiFi by hand
	Error    string           `json:"error,omitempty"`
}

// Config wires a Session. Zero durations get defaults.
type Config struct {
	Library      *library.Library
	Joiner       wifi.Joiner
	Dial         func(ctx context.Context) (*box.Client, error)
	CacheDir     string
	ScanEvery    time.Duration // default 2s
	ScanTimeout  time.Duration // default 60s
	PingEvery    time.Duration // default 10s
	BatteryEvery time.Duration // default 1min
}

// Session owns the connection to the box. All methods are safe for concurrent use.
type Session struct {
	cfg Config

	mu       sync.Mutex // guards the fields below
	status   Status
	client   *box.Client
	joined   string             // SSID we joined and must leave
	stop     context.CancelFunc // ends the link: connect attempt and keepalive
	abort    context.CancelFunc // ends the running sync
	fresh    bool               // box content was read on the current link
	boxRaw   []byte             // last playlist.bin read from the box
	boxTree  *playlist.Node
	boxItems []library.Item  // manifest found on the box
	covers   map[string]bool // box covers in the cache, by UUID
	subs     map[chan struct{}]struct{}

	boxMu sync.Mutex // serializes use of client
}

// New creates a session and loads the cached box content for offline viewing.
func New(cfg Config) *Session {
	if cfg.ScanEvery == 0 {
		cfg.ScanEvery = 2 * time.Second
	}
	if cfg.ScanTimeout == 0 {
		cfg.ScanTimeout = 60 * time.Second
	}
	if cfg.PingEvery == 0 {
		cfg.PingEvery = 10 * time.Second
	}
	if cfg.BatteryEvery == 0 {
		cfg.BatteryEvery = time.Minute
	}
	s := &Session{cfg: cfg, status: Status{State: Offline}, covers: map[string]bool{}, subs: map[chan struct{}]struct{}{}}
	if raw, err := os.ReadFile(s.cachePath(syncer.BoxPlaylist)); err == nil {
		if tree, err := playlist.Decode(raw); err == nil {
			s.boxRaw, s.boxTree = raw, tree
			if info, err := os.Stat(s.cachePath(syncer.BoxPlaylist)); err == nil {
				s.status.Seen = info.ModTime()
			}
		}
	}
	if data, err := os.ReadFile(s.cachePath(syncer.ManifestName)); err == nil {
		if m, err := library.ParseManifest(data); err == nil {
			s.boxItems = m.Items
		}
	}
	if entries, err := os.ReadDir(s.coversDir()); err == nil {
		for _, e := range entries {
			if uuid, ok := strings.CutSuffix(e.Name(), ".jpg"); ok {
				s.covers[uuid] = true
			}
		}
	}
	s.status.Pending = s.pending()
	return s
}

func (s *Session) cachePath(name string) string { return filepath.Join(s.cfg.CacheDir, name) }

func (s *Session) coversDir() string { return s.cachePath("covers") }

// Status returns the current status.
func (s *Session) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Subscribe returns a channel signalled after every change (status, box
// content or library) and a function ending the subscription.
func (s *Session) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

// notify signals subscribers without blocking. Callers hold s.mu.
func (s *Session) notify() {
	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default: // a signal is already pending
		}
	}
}

// update applies fn to the status under the lock and notifies.
func (s *Session) update(fn func(st *Status)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.status)
	s.notify()
}

// LibraryChanged recomputes the pending count after an edit.
func (s *Session) LibraryChanged() {
	s.update(func(st *Status) { st.Pending = s.pending() })
}

// pending counts changes to send. Callers hold s.mu.
func (s *Session) pending() int {
	return syncer.Pending(s.boxTree, s.cfg.Library.Manifest().Items, s.boxItems)
}

// Connect starts looking for the box and connecting to it in the background.
func (s *Session) Connect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.status.State; st != Offline && st != Lost {
		return
	}
	ctx, stop := context.WithCancel(context.Background())
	s.stop, s.fresh = stop, false
	s.status = Status{State: Waiting, Pending: s.status.Pending, Seen: s.status.Seen}
	s.notify()
	go s.connect(ctx)
}

// Cancel stops a connection attempt or a running sync.
func (s *Session) Cancel() {
	s.mu.Lock()
	state, stop, abort := s.status.State, s.stop, s.abort
	s.mu.Unlock()
	switch {
	case state == Syncing && abort != nil:
		abort()
	case (state == Waiting || state == Joining) && stop != nil:
		stop()
	}
}

func (s *Session) connect(ctx context.Context) {
	client, err := s.reach(ctx)
	if err == nil {
		err = s.hello(ctx, client)
	}
	if err != nil {
		if client != nil {
			client.Close()
		}
		message := Message(err)
		if errors.Is(err, context.Canceled) {
			message = ""
		}
		s.drop(Offline, message)
		return
	}
	message := ""
	if err := s.refresh(); err != nil {
		// Sync stays refused (not fresh); tell the parent in plain words.
		message = Message(fmt.Errorf("%w: %w", syncer.ErrBoxUnread, err))
	} else {
		go s.fetchCovers(ctx, client)
	}
	s.update(func(st *Status) {
		if st.State == Reading { // not disconnected meanwhile
			st.State, st.Error = Connected, message
		}
	})
	go s.keepalive(ctx, client)
}

// reach returns a client once the box answers, joining its network if needed.
// A failed join is retried until the deadline: scan results can be stale, so
// the box may be listed before its network is really up.
func (s *Session) reach(ctx context.Context) (*box.Client, error) {
	deadline := time.Now().Add(s.cfg.ScanTimeout)
	var joinErr error
	for {
		if c, err := s.dial(ctx, time.Second); err == nil {
			return c, nil // already on the box network
		}
		aps, err := s.cfg.Joiner.Scan(ctx)
		switch {
		case errors.Is(err, wifi.ErrManualJoin):
			s.update(func(st *Status) { st.Manual = true })
		case err != nil:
			return nil, err
		}
		if len(aps) > 0 {
			c, err := s.join(ctx, aps[0].SSID)
			if err == nil || ctx.Err() != nil {
				return c, err
			}
			joinErr = err // stay "joining": the box is there, its network is not up yet
		}
		if time.Now().After(deadline) {
			if joinErr != nil {
				return nil, joinErr
			}
			return nil, errNotFound
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(s.cfg.ScanEvery):
		}
	}
}

func (s *Session) join(ctx context.Context, ssid string) (*box.Client, error) {
	s.update(func(st *Status) { st.State, st.SSID = Joining, ssid })
	if err := s.cfg.Joiner.Join(ctx, ssid); err != nil {
		return nil, fmt.Errorf("Impossible de rejoindre %s : %w", ssid, err)
	}
	s.mu.Lock()
	s.joined = ssid
	s.mu.Unlock()
	var err error
	for range 10 { // DHCP can take a few seconds
		var c *box.Client
		if c, err = s.dial(ctx, 2*time.Second); err == nil {
			return c, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil, err
}

func (s *Session) dial(ctx context.Context, timeout time.Duration) (*box.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return s.cfg.Dial(ctx)
}

// hello checks the box answers and records its info; reading its content comes next.
func (s *Session) hello(ctx context.Context, c *box.Client) error {
	if err := c.Ping(); err != nil {
		return err
	}
	info, err := c.Info()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.client = c
	s.status.State, s.status.Info, s.status.Error = Reading, &info, ""
	s.notify()
	return nil
}

// refresh reads the box's playlist and manifest, and adopts the manifest when
// the local library is empty (fresh install).
func (s *Session) refresh() error {
	s.boxMu.Lock()
	defer s.boxMu.Unlock()
	c := s.currentClient()
	if c == nil {
		return errOffline
	}
	raw, err := c.GetFile(syncer.BoxPlaylist)
	if err != nil {
		return err
	}
	tree, err := playlist.Decode(raw)
	if err != nil {
		return err
	}
	var items []library.Item
	data, err := c.GetFile(syncer.ManifestName)
	switch {
	case err == nil:
		m, err := library.ParseManifest(data)
		if err != nil {
			return err
		}
		items = m.Items
		if s.cfg.Library.NeverSaved() && len(items) > 0 {
			if err := s.cfg.Library.Adopt(m, c.GetFile); err != nil {
				return err
			}
		}
	case !errors.Is(err, box.ErrNotFound):
		return err
	}
	s.store(raw, tree, items)
	return nil
}

// store records fresh box content, caches it, and recomputes pending.
func (s *Session) store(raw []byte, tree *playlist.Node, items []library.Item) {
	os.MkdirAll(s.cfg.CacheDir, 0o755)
	os.WriteFile(s.cachePath(syncer.BoxPlaylist), raw, 0o644)
	if m, err := (library.Manifest{Version: library.ManifestVersion, Items: items}).JSON(); err == nil {
		os.WriteFile(s.cachePath(syncer.ManifestName), m, 0o644)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.boxRaw, s.boxTree, s.boxItems, s.fresh = raw, tree, items, true
	s.status.Seen = time.Now()
	s.status.Pending = s.pending()
	s.notify()
}

func (s *Session) currentClient() *box.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

// keepalive pings the idle box so the link stays up, re-reads the battery now
// and then, and reports a lost link.
func (s *Session) keepalive(ctx context.Context, c *box.Client) {
	t := time.NewTicker(s.cfg.PingEvery)
	defer t.Stop()
	battery := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !s.boxMu.TryLock() {
			continue // a sync or download is using the link
		}
		err := c.Ping()
		var level int
		var charging bool
		read := err == nil && time.Since(battery) >= s.cfg.BatteryEvery
		if read {
			level, charging, err = c.Battery()
			battery = time.Now()
		}
		s.boxMu.Unlock()
		if err != nil && ctx.Err() == nil {
			s.drop(Lost, "Connexion avec Merlin perdue.")
			return
		}
		if read {
			s.update(func(st *Status) {
				if st.Info != nil {
					info := *st.Info // shared with earlier snapshots
					info.Battery, info.Charging = level, charging
					st.Info = &info
				}
			})
		}
	}
}

// drop ends the link, closes the client and leaves the box network.
func (s *Session) drop(state State, message string) {
	s.mu.Lock()
	c, stop, abort, ssid := s.client, s.stop, s.abort, s.joined
	s.client, s.stop, s.abort, s.joined, s.fresh = nil, nil, nil, "", false
	s.status.State, s.status.Info, s.status.Progress, s.status.Error = state, nil, nil, message
	if state == Offline {
		s.status.SSID = ""
	}
	s.notify()
	s.mu.Unlock()
	for _, cancel := range []context.CancelFunc{stop, abort} {
		if cancel != nil {
			cancel()
		}
	}
	if c != nil {
		c.Close()
	}
	if ssid != "" {
		s.cfg.Joiner.Leave(ssid)
	}
}

// Disconnect ends the transfer session cleanly and leaves the box network.
func (s *Session) Disconnect() {
	s.mu.Lock()
	stop, abort := s.stop, s.abort
	s.mu.Unlock()
	for _, cancel := range []context.CancelFunc{stop, abort} {
		if cancel != nil {
			cancel()
		}
	}
	s.boxMu.Lock() // waits for a running transfer to notice the cancel
	if c := s.currentClient(); c != nil {
		c.EndSync()
	}
	s.boxMu.Unlock()
	s.drop(Offline, "")
}

// Sync sends the library to the box in the background.
func (s *Session) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.State != Connected || s.client == nil {
		return errOffline
	}
	if !s.fresh { // never plan from a cache: it may be stale or from another box
		return syncer.ErrBoxUnread
	}
	if info := s.status.Info; info != nil && info.Battery < syncer.MinBattery && !info.Charging {
		return errLowBattery
	}
	plan, err := syncer.NewPlan(s.boxTree, s.cfg.Library, s.boxItems)
	if err != nil {
		return err
	}
	if s.boxRaw != nil {
		dir := s.cachePath("backups")
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("playlist-%d.bin", time.Now().Unix())), s.boxRaw, 0o644)
	}
	ctx, abort := context.WithCancel(context.Background())
	s.abort = abort
	s.status.State, s.status.Error = Syncing, ""
	s.status.Progress = &syncer.Progress{Phase: "files", Total: plan.Bytes()}
	s.notify()
	go s.runSync(ctx, s.client, plan)
	return nil
}

func (s *Session) runSync(ctx context.Context, c *box.Client, plan syncer.Plan) {
	s.boxMu.Lock()
	res, err := syncer.Run(ctx, c, plan, func(p syncer.Progress) {
		s.update(func(st *Status) {
			if st.State == Syncing {
				st.Progress = &p
			}
		})
	})
	linkDown := err != nil && !errors.Is(err, context.Canceled) && c.Ping() != nil
	var info *box.Info
	if err == nil {
		if fresh, err := c.Info(); err == nil { // free space changed
			info = &fresh
		}
	}
	s.boxMu.Unlock()
	if linkDown {
		s.drop(Lost, Message(err))
		return
	}
	if err == nil {
		m, _ := library.ParseManifest(plan.Manifest)
		s.store(res.Raw, res.Tree, m.Items)
	}
	s.update(func(st *Status) {
		if st.State == Syncing { // not disconnected meanwhile
			st.State, st.Progress, st.Error = Connected, nil, Message(err)
			if info != nil {
				st.Info = info
			}
		}
	})
}

// ErrNoCover means no image is available for an item right now.
var ErrNoCover = errors.New("app: no cover available")

// validID matches the UUIDs used as file names; anything else (e.g. "../x")
// never reaches the file system or the box.
var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Cover returns an item's JPEG: from the library, or the covers fetched from the box.
func (s *Session) Cover(uuid string) ([]byte, error) {
	if !validID.MatchString(uuid) {
		return nil, ErrNoCover
	}
	if data, err := os.ReadFile(s.cfg.Library.ImagePath(uuid)); err == nil {
		return data, nil
	}
	if data, err := os.ReadFile(filepath.Join(s.coversDir(), uuid+".jpg")); err == nil {
		return data, nil
	}
	return nil, ErrNoCover
}

// coverShowEvery spaces the refreshes of the UI while covers arrive.
const coverShowEvery = time.Second

// fetchCovers downloads the box covers missing from the cache, one at a time,
// so a sync or the keepalive can take the link between two files.
func (s *Session) fetchCovers(ctx context.Context, c *box.Client) {
	os.MkdirAll(s.coversDir(), 0o755)
	shown, unshown := time.Now(), false
	for _, uuid := range s.missingCovers() {
		s.boxMu.Lock()
		if ctx.Err() != nil {
			s.boxMu.Unlock()
			break
		}
		data, err := c.GetFile(uuid + ".jpg")
		s.boxMu.Unlock()
		if errors.Is(err, box.ErrNotFound) {
			continue
		}
		if err != nil {
			break // a lost link is the keepalive's to report
		}
		if os.WriteFile(filepath.Join(s.coversDir(), uuid+".jpg"), data, 0o644) != nil {
			break
		}
		s.mu.Lock()
		s.covers[uuid], unshown = true, true
		if time.Since(shown) >= coverShowEvery {
			s.notify()
			shown, unshown = time.Now(), false
		}
		s.mu.Unlock()
	}
	if unshown {
		s.mu.Lock()
		s.notify()
		s.mu.Unlock()
	}
}

// missingCovers lists the box items with no cover in the library or the
// cache, top levels first: they are the ones on screen.
func (s *Session) missingCovers() []string {
	own := map[string]bool{}
	for _, it := range s.cfg.Library.Manifest().Items {
		own[it.UUID] = it.Image != nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.boxTree == nil {
		return nil
	}
	var missing []string
	for level := s.boxTree.Children; len(level) > 0; {
		var next []*playlist.Node
		for _, n := range level {
			if validID.MatchString(n.UUID) && !s.covers[n.UUID] && !own[n.UUID] {
				missing = append(missing, n.UUID)
			}
			next = append(next, n.Children...)
		}
		level = next
	}
	return missing
}

// Close disconnects if needed; call it on shutdown.
func (s *Session) Close() {
	if s.Status().State != Offline {
		s.Disconnect()
	}
}
