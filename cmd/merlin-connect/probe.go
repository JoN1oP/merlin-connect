package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"merlin-connect/internal/box"
	"merlin-connect/internal/playlist"
	"merlin-connect/internal/wifi"
)

// probe checks the protocol against a real box (Phase 0 of the plan). Without
// flags it only reads. -write-test and -roundtrip write to the box.
func probe(args []string) int {
	flags := flag.NewFlagSet("probe", flag.ExitOnError)
	join := flags.Bool("join", true, "scan for and join the box network (off: you joined it yourself)")
	addr := flags.String("addr", box.DefaultAddr, "box address")
	out := flags.String("out", "probe-out", "directory for downloaded files")
	writeTest := flags.Bool("write-test", false, "upload a small file twice and read it back")
	roundtrip := flags.Bool("roundtrip", false, "re-apply the current playlist through JSON and compare")
	backupOK := flags.Bool("yes-i-have-a-backup", false, "required with -roundtrip")
	hold := flags.Duration("hold", 0, "keep pinging this long and report if the link drops")
	flags.Parse(args)
	if *roundtrip && !*backupOK {
		fmt.Println("-roundtrip rewrites the box playlist: add -yes-i-have-a-backup once you have a copy of the SD card.")
		return 2
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Println(err)
		return 1
	}
	p := &prober{out: *out, addr: *addr}
	if err := p.run(*join, *writeTest, *roundtrip, *hold); err != nil {
		fmt.Println("FAILED:", err)
		return 1
	}
	fmt.Println("All probe steps passed.")
	return 0
}

type prober struct {
	out, addr string
	client    *box.Client
}

func (p *prober) step(format string, args ...any) { fmt.Printf("\n== "+format+"\n", args...) }

func (p *prober) save(name string, data []byte) {
	path := filepath.Join(p.out, name)
	os.WriteFile(path, data, 0o644)
	fmt.Printf("   saved %s (%d bytes)\n", path, len(data))
}

func (p *prober) run(join, writeTest, roundtrip bool, hold time.Duration) error {
	ctx := context.Background()
	if join {
		p.step("Looking for the box network (hold the box button ~5 s)")
		joiner := wifi.New()
		ssid, err := findBox(ctx, joiner)
		if err != nil {
			return err
		}
		fmt.Println("   joining", ssid)
		if err := joiner.Join(ctx, ssid); err != nil {
			return err
		}
		defer joiner.Leave(ssid)
	}

	p.step("Connecting to %s", p.addr)
	var err error
	for range 10 {
		dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		p.client, err = box.Dial(dctx, p.addr)
		cancel()
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		return err
	}
	defer p.client.Close()
	defer p.client.EndSync()

	if err := p.client.Ping(); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	info, err := p.client.Info()
	if err != nil {
		return fmt.Errorf("info: %w", err)
	}
	fmt.Printf("   %+v\n", info)

	before, tree, err := p.readPlaylist("playlist.bin")
	if err != nil {
		return err
	}
	if err := p.readFiles(tree); err != nil {
		return err
	}
	if writeTest {
		if err := p.writeTest(); err != nil {
			return err
		}
	}
	if roundtrip {
		if err := p.roundtrip(before, tree); err != nil {
			return err
		}
	}
	if hold > 0 {
		p.hold(hold)
	}
	return nil
}

func findBox(ctx context.Context, joiner wifi.Joiner) (string, error) {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		aps, err := joiner.Scan(ctx)
		if err != nil {
			return "", err
		}
		if len(aps) > 0 {
			return aps[0].SSID, nil
		}
		time.Sleep(2 * time.Second)
	}
	return "", errors.New("no MERLIN_ network found")
}

// readPlaylist downloads and describes the box playlist (open question 1).
func (p *prober) readPlaylist(saveAs string) ([]playlist.Record, *playlist.Node, error) {
	p.step("getFile(playlist.bin)")
	raw, err := p.client.GetFile("playlist.bin")
	if err != nil {
		return nil, nil, fmt.Errorf("getFile(playlist.bin): %w", err)
	}
	p.save(saveAs, raw)
	records, err := playlist.Records(raw)
	if err != nil {
		return nil, nil, err
	}
	tree, err := playlist.Decode(raw)
	if err != nil {
		return nil, nil, err
	}
	types := map[uint16]int{}
	for _, r := range records {
		types[r.Type]++
		if r.Type == playlist.TypeFavorites || r.FavOrder != 0 || r.LimitTime != 0 {
			fmt.Printf("   special record: %+v\n", r)
		}
	}
	fmt.Printf("   %d records by type: %v; %d reachable nodes\n", len(records), types, tree.Count())
	printTree(tree, 1)
	return records, tree, nil
}

func printTree(n *playlist.Node, depth int) {
	for _, c := range n.Children {
		kind := "folder"
		if c.Story {
			kind = "story"
		}
		fmt.Printf("   %s%s [%s %s]\n", strings.Repeat("  ", depth-1), c.Title, kind, c.UUID)
		printTree(c, depth+1)
	}
}

// readFiles checks searchFile and getFile on real and missing names.
func (p *prober) readFiles(tree *playlist.Node) error {
	var story *playlist.Node
	tree.Walk(func(n, _ *playlist.Node) {
		if story == nil && n.Story {
			story = n
		}
	})
	if story != nil {
		p.step("searchFile/getFile on %q", story.Title)
		st, err := p.client.Stat(story.UUID + ".mp3")
		fmt.Printf("   stat %s.mp3: size=%d err=%v (size -1: the box does not report it)\n", story.UUID, st.Size, err)
		st, err = p.client.Hash(story.UUID+".mp3", st.Size)
		fmt.Printf("   hash %s.mp3: size=%d sha=%x err=%v\n", story.UUID, st.Size, st.SHA256[:4], err)
		jpg, err := p.client.GetFile(story.UUID + ".jpg")
		if err != nil {
			return fmt.Errorf("getFile(cover): %w", err)
		}
		p.save("cover.jpg", jpg)
	}
	p.step("Missing files (expect not found)")
	_, err := p.client.Stat("merlin-connect-missing.mp3")
	fmt.Printf("   searchFile: %v\n", err)
	_, err = p.client.GetFile("merlin-connect-missing.mp3")
	fmt.Printf("   getFile: %v (ErrNotFound: %v)\n", err, errors.Is(err, box.ErrNotFound))
	_, err = p.client.GetFile("merlin-connect.json")
	fmt.Printf("   manifest: %v\n", err)
	return nil
}

// writeTest checks that an upload overwrites a file of the same name, and that a
// realistic multi-chunk upload is accepted and verified by the box.
func (p *prober) writeTest() error {
	p.step("Upload overwrite test (merlin-connect-probe.txt)")
	for _, content := range []string{"first", "second"} {
		if err := p.client.Upload("merlin-connect-probe.txt", []byte(content), nil); err != nil {
			return fmt.Errorf("upload %q: %w", content, err)
		}
	}
	got, err := p.client.GetFile("merlin-connect-probe.txt")
	if err != nil {
		return err
	}
	fmt.Printf("   read back %q (want \"second\")\n", got)
	if string(got) != "second" {
		return errors.New("upload did not overwrite")
	}

	p.step("Large upload test (merlin-connect-probe.bin, 3 MB)")
	big := make([]byte, 3<<20)
	for i := range big {
		big[i] = byte(i * 7)
	}
	start := time.Now()
	if err := p.client.Upload("merlin-connect-probe.bin", big, nil); err != nil {
		return fmt.Errorf("large upload: %w", err)
	}
	fmt.Printf("   uploaded %d bytes in %s\n", len(big), time.Since(start).Round(time.Millisecond))
	return nil
}

// schema is one candidate shape for the playlist JSON the box accepts. The
// backend's catalog items carry type, add_time and limit_time next to
// uuid/title/child, so the box likely wants some of them (open question 2).
type schema struct {
	name       string
	times      bool // add_time and limit_time on every node
	typ        bool // type: CATEGORY / SUBCATEGORY / CONTENT
	storyChild bool // "child": [] on stories too
}

var schemas = []schema{
	{name: "uuid, title, child"},
	{name: "+ add_time, limit_time", times: true},
	{name: "+ add_time, limit_time, type", times: true, typ: true},
	{name: "+ add_time, limit_time, type, child on stories", times: true, typ: true, storyChild: true},
}

// schemaJSON renders the tree's children in the given schema.
func schemaJSON(nodes []*playlist.Node, s schema, depth int) []map[string]any {
	out := []map[string]any{}
	for _, n := range nodes {
		m := map[string]any{"uuid": n.UUID, "title": n.Title}
		if s.times {
			m["add_time"], m["limit_time"] = n.AddTime, n.LimitTime
		}
		if s.typ {
			m["type"] = map[bool]string{true: "CONTENT", false: "SUBCATEGORY"}[n.Story]
			if !n.Story && depth == 0 {
				m["type"] = "CATEGORY"
			}
		}
		if !n.Story || s.storyChild {
			m["child"] = schemaJSON(n.Children, s, depth+1)
		}
		out = append(out, m)
	}
	return out
}

// roundtrip re-applies the current playlist through JSON, trying each schema
// until the box stops answering MISSING_FIELD, then compares the records
// before and after. Every attempt is a full copy of the current playlist, so an
// accepted one rewrites the box with what it already holds.
func (p *prober) roundtrip(before []playlist.Record, tree *playlist.Node) error {
	p.step("Assets on the box (read-only)")
	missing, err := missingAssets(p.client, tree)
	if err != nil {
		return err
	}
	fmt.Printf("   missing files: %d\n", len(missing))
	for _, m := range missing {
		fmt.Println("   MISSING", m)
	}

	limit, err := p.titleLimit(tree)
	if err != nil {
		return err
	}
	tree = tree.Clone()
	fmt.Printf("   titles shortened to fit: %d\n", shortenTitles(tree, limit))
	for i := range before { // compare against the intended titles
		before[i].Title = cut(before[i].Title, limit)
	}

	p.step("Round trip: current playlist -> JSON -> updatePlaylist")
	const name = "playlist-merlin-connect-probe.json"
	for i, s := range schemas {
		js := marshalRaw(schemaJSON(tree.Children, s, 0))
		p.save(fmt.Sprintf("roundtrip-%d.json", i+1), js)
		if err = p.client.Upload(name, js, nil); err != nil {
			return fmt.Errorf("upload json: %w", err)
		}
		err = p.client.UpdatePlaylist(name)
		fmt.Printf("   schema %d (%s): %v\n", i+1, s.name, resultOf(err))
		var status *box.StatusError
		if !errors.As(err, &status) || status.Code != 9 { // not MISSING_FIELD: stop here
			break
		}
	}
	if err != nil {
		return fmt.Errorf("updatePlaylist: %w", err)
	}
	after, _, err := p.readPlaylist("playlist-after.bin")
	if err != nil {
		return err
	}
	diffs := diffRecords(before, after)
	for _, d := range diffs {
		fmt.Println("   DIFF", d)
	}
	fmt.Printf("   %d differences (add_time ignored)\n", len(diffs))
	if len(diffs) > 0 {
		return fmt.Errorf("the round trip changed %d records (see DIFF lines)", len(diffs))
	}
	return nil
}

// missingAssets lists the media files updatePlaylist would look for and not
// find: <uuid>.jpg for every node, <uuid>.mp3 for every story. Each UUID is
// checked once, in tree order.
func missingAssets(c *box.Client, tree *playlist.Node) ([]string, error) {
	var missing []string
	seen := map[string]bool{}
	var err error
	tree.Walk(func(n, _ *playlist.Node) {
		if err != nil || seen[n.UUID] {
			return
		}
		seen[n.UUID] = true
		kind, names := "folder", []string{n.UUID + ".jpg"}
		if n.Story {
			kind, names = "story", append(names, n.UUID+".mp3")
		}
		for _, name := range names {
			_, statErr := c.Stat(name)
			switch {
			case errors.Is(statErr, box.ErrNotFound):
				missing = append(missing, fmt.Sprintf("%s (%s %s)", name, kind, n.Title))
			case statErr != nil:
				err = statErr
			}
		}
	})
	return missing, err
}

// titleLimit measures the longest title the box accepts without changing
// anything: each test playlist holds a real story titled with n bytes followed
// by a node missing its dates, so the box always refuses it, with
// TITLE_TOO_LARGE while n is too long and MISSING_FIELD once n fits.
func (p *prober) titleLimit(tree *playlist.Node) (int, error) {
	p.step("Title length limit (the box refuses every test playlist)")
	var story *playlist.Node
	tree.Walk(func(n, _ *playlist.Node) {
		if story == nil && n.Story {
			story = n
		}
	})
	if story == nil {
		return 0, errors.New("no story on the box to test titles with")
	}
	const name = "playlist-merlin-connect-probe.json"
	for n := playlist.MaxTitleBytes; n >= 16; n-- {
		js := marshalRaw([]map[string]any{
			{"uuid": story.UUID, "title": strings.Repeat("a", n), "add_time": 0, "limit_time": 0},
			{"uuid": story.UUID, "title": "x"},
		})
		if err := p.client.Upload(name, js, nil); err != nil {
			return 0, fmt.Errorf("upload json: %w", err)
		}
		err := p.client.UpdatePlaylist(name)
		var status *box.StatusError
		switch {
		case errors.As(err, &status) && status.Code == 13: // TITLE_TOO_LARGE
			continue
		case errors.As(err, &status) && status.Code == 9: // MISSING_FIELD: n fits
			fmt.Printf("   longest accepted title: %d bytes\n", n)
			return n, nil
		default:
			return 0, fmt.Errorf("title test with %d bytes: unexpected answer %v", n, resultOf(err))
		}
	}
	return 0, errors.New("the box refused every title length")
}

// shortenTitles cuts every title longer than limit and returns how many.
func shortenTitles(n *playlist.Node, limit int) int {
	count := 0
	n.Walk(func(c, _ *playlist.Node) {
		if short := cut(c.Title, limit); short != c.Title {
			c.Title = short
			count++
		}
	})
	return count
}

func cut(title string, limit int) string {
	if len(title) <= limit {
		return title
	}
	for limit > 0 && !utf8.RuneStart(title[limit]) { // never split a character
		limit--
	}
	return strings.TrimSpace(title[:limit])
}

// marshalRaw encodes v without escaping & < > (the box counts the escapes).
func marshalRaw(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func resultOf(err error) string {
	if err == nil {
		return "ACCEPTED"
	}
	return err.Error()
}

// diffRecords compares two playlists by UUID, ignoring ids and add_time.
func diffRecords(before, after []playlist.Record) []string {
	type key struct{ uuid, title string }
	describe := func(records []playlist.Record) map[key]string {
		uuids := map[uint16]string{}
		for _, r := range records {
			uuids[r.ID] = r.UUID
		}
		out := map[key]string{}
		for _, r := range records {
			out[key{r.UUID, r.Title}] = fmt.Sprintf("parent=%q order=%d type=%d children=%d fav=%d limit=%d",
				uuids[r.ParentID], r.Order, r.Type, r.Children, r.FavOrder, r.LimitTime)
		}
		return out
	}
	a, b := describe(before), describe(after)
	var diffs []string
	for k, va := range a {
		if vb, ok := b[k]; !ok {
			diffs = append(diffs, fmt.Sprintf("%q removed (%s)", k.title, va))
		} else if va != vb {
			diffs = append(diffs, fmt.Sprintf("%q: %s -> %s", k.title, va, vb))
		}
	}
	for k, vb := range b {
		if _, ok := a[k]; !ok {
			diffs = append(diffs, fmt.Sprintf("%q added (%s)", k.title, vb))
		}
	}
	slices.Sort(diffs)
	return diffs
}

// hold pings every 10 s to learn whether the box closes its transfer window
// while a client is connected (open question 14).
func (p *prober) hold(d time.Duration) {
	p.step("Holding the link for %s", d)
	start := time.Now()
	for time.Since(start) < d {
		time.Sleep(10 * time.Second)
		if err := p.client.Ping(); err != nil {
			fmt.Printf("   link dropped after %s: %v\n", time.Since(start).Round(time.Second), err)
			return
		}
	}
	fmt.Println("   link held for", d)
}
