package library

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"merlin-connect/internal/media"
	"merlin-connect/internal/playlist"
)

var (
	ErrNotFound   = errors.New("library: no such item")
	ErrEmptyTitle = errors.New("library: title is empty")
	ErrBadParent  = errors.New("library: invalid parent")
)

// Library is the on-disk store: dir/library.json plus dir/media/<uuid>.mp3|jpg.
type Library struct {
	dir        string
	mu         sync.Mutex
	m          Manifest
	neverSaved bool // no library.json yet: a fresh install
	now        func() time.Time
}

// ImportResult summarizes an Import.
type ImportResult struct {
	Folders      int `json:"folders"`
	Stories      int `json:"stories"`
	Skipped      int `json:"skipped"` // stories already in that folder
	Placeholders int `json:"placeholders"`
}

// Open loads the library in dir, creating it if needed.
func Open(dir string) (*Library, error) {
	if err := os.MkdirAll(filepath.Join(dir, "media"), 0o755); err != nil {
		return nil, err
	}
	l := &Library{dir: dir, m: Manifest{Version: ManifestVersion}, now: time.Now}
	data, err := os.ReadFile(filepath.Join(dir, "library.json"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		l.neverSaved = true
		return l, nil
	case err != nil:
		return nil, err
	}
	if l.m, err = ParseManifest(data); err != nil {
		return nil, err
	}
	return l, nil
}

// Manifest returns a copy of the current manifest.
func (l *Library) Manifest() Manifest {
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.m
	m.Items = slices.Clone(l.m.Items)
	return m
}

// Item returns the item with the given UUID.
func (l *Library) Item(uuid string) (Item, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if i := l.index(uuid); i >= 0 {
		return l.m.Items[i], true
	}
	return Item{}, false
}

// NeverSaved reports a fresh install: no library was ever saved here. Unlike
// Empty, it stays false after the user deleted everything.
func (l *Library) NeverSaved() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.neverSaved
}

// Empty reports whether the library has no items.
func (l *Library) Empty() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.m.Items) == 0
}

// AudioPath and ImagePath locate an item's media files.
func (l *Library) AudioPath(uuid string) string { return l.mediaPath(uuid + ".mp3") }
func (l *Library) ImagePath(uuid string) string { return l.mediaPath(uuid + ".jpg") }

func (l *Library) mediaPath(name string) string { return filepath.Join(l.dir, "media", name) }

// AddFolder creates an empty folder. cover may be nil (placeholder).
func (l *Library) AddFolder(parent, title string, cover []byte) (Item, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	item, _, err := l.addFolder(parent, title, cover)
	if err != nil {
		return Item{}, err
	}
	return item, l.save()
}

// Import adds everything Scan found under parent, reading audio from fsys.
// Importing again merges: folders with the same title are reused and stories
// whose audio is already in that folder are skipped.
func (l *Library) Import(parent string, root *media.Folder, fsys fs.FS) (ImportResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var res ImportResult
	if err := l.checkParent(parent, ""); err != nil {
		return res, err
	}
	err := l.importFolder(parent, root, fsys, &res)
	if saveErr := l.save(); err == nil {
		err = saveErr
	}
	return res, err
}

func (l *Library) importFolder(parent string, f *media.Folder, fsys fs.FS, res *ImportResult) error {
	for _, sub := range f.Folders {
		uuid := l.childFolder(parent, sub.Name)
		if uuid == "" {
			item, placeholder, err := l.addFolder(parent, sub.Name, firstCover(sub))
			if err != nil {
				return err
			}
			uuid = item.UUID
			res.Folders++
			if placeholder {
				res.Placeholders++
			}
		}
		if err := l.importFolder(uuid, sub, fsys, res); err != nil {
			return err
		}
	}
	for _, s := range f.Stories {
		added, placeholder, err := l.addStory(parent, s, fsys)
		switch {
		case err != nil:
			return err
		case !added:
			res.Skipped++
		default:
			res.Stories++
			if placeholder {
				res.Placeholders++
			}
		}
	}
	return nil
}

// childFolder returns the UUID of parent's custom folder titled title, or "".
func (l *Library) childFolder(parent, title string) string {
	title = playlist.NormalizeTitle(title)
	for _, it := range l.m.Items {
		if it.Parent == parent && it.Kind == Folder && it.Title == title {
			return it.UUID
		}
	}
	return ""
}

// firstCover is a folder's own cover, else its first story's, searching depth-first.
func firstCover(f *media.Folder) []byte {
	if f.Cover != nil {
		return f.Cover
	}
	for _, s := range f.Stories {
		if s.Cover != nil {
			return s.Cover
		}
	}
	for _, sub := range f.Folders {
		if c := firstCover(sub); c != nil {
			return c
		}
	}
	return nil
}

func (l *Library) addFolder(parent, title string, cover []byte) (Item, bool, error) {
	if err := l.checkParent(parent, ""); err != nil {
		return Item{}, false, err
	}
	item := Item{UUID: newUUID(), Kind: Folder, Title: playlist.NormalizeTitle(title), Parent: parent,
		Added: uint32(l.now().Unix())}
	if item.Title == "" {
		return Item{}, false, ErrEmptyTitle
	}
	image, placeholder := coverOrPlaceholder(cover, item.Title)
	var err error
	if item.Image, err = l.writeMedia(item.UUID+".jpg", image); err != nil {
		return Item{}, false, err
	}
	l.append(&item)
	return item, placeholder, nil
}

// addStory stores a story unless the same audio is already in parent. It
// reports whether it added the story and whether its cover is a placeholder.
func (l *Library) addStory(parent string, s *media.Story, fsys fs.FS) (added, placeholder bool, err error) {
	item := Item{UUID: newUUID(), Kind: Story, Title: playlist.NormalizeTitle(s.Title), Parent: parent,
		Added: uint32(l.now().Unix())}
	if item.Title == "" {
		item.Title = "Histoire"
	}
	audio, err := fsys.Open(s.Audio)
	if err != nil {
		return false, false, err
	}
	defer audio.Close()
	if item.Audio, err = l.writeMediaFrom(item.UUID+".mp3", audio); err != nil {
		return false, false, err
	}
	if l.hasAudio(parent, item.Audio.SHA256) {
		os.Remove(l.AudioPath(item.UUID))
		return false, false, nil
	}
	image, placeholder := coverOrPlaceholder(s.Cover, item.Title)
	if item.Image, err = l.writeMedia(item.UUID+".jpg", image); err != nil {
		return false, false, err
	}
	l.append(&item)
	return true, placeholder, nil
}

func (l *Library) hasAudio(parent, sha string) bool {
	return slices.ContainsFunc(l.m.Items, func(it Item) bool {
		return it.Parent == parent && it.Audio != nil && it.Audio.SHA256 == sha
	})
}

func coverOrPlaceholder(cover []byte, title string) ([]byte, bool) {
	if cover != nil {
		if jpg, err := media.NormalizeCover(cover); err == nil {
			return jpg, false
		}
	}
	return media.Placeholder(title), true
}

// append places item last among its custom siblings.
func (l *Library) append(item *Item) {
	item.Position = len(l.children(item.Parent))
	l.m.Items = append(l.m.Items, *item)
}

// Rename changes an item's title.
func (l *Library) Rename(uuid, title string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := l.index(uuid)
	if i < 0 {
		return ErrNotFound
	}
	if title = playlist.NormalizeTitle(title); title == "" {
		return ErrEmptyTitle
	}
	l.m.Items[i].Title = title
	return l.save()
}

// SetCover replaces an item's image with the given image file contents.
func (l *Library) SetCover(uuid string, image []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := l.index(uuid)
	if i < 0 {
		return ErrNotFound
	}
	jpg, err := media.NormalizeCover(image)
	if err != nil {
		return err
	}
	if l.m.Items[i].Image, err = l.writeMedia(uuid+".jpg", jpg); err != nil {
		return err
	}
	return l.save()
}

// Move puts an item under parent at position among its custom siblings
// (clamped). Moving a folder into itself or its descendants is refused.
func (l *Library) Move(uuid, parent string, position int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := l.index(uuid)
	if i < 0 {
		return ErrNotFound
	}
	if err := l.checkParent(parent, uuid); err != nil {
		return err
	}
	old := l.m.Items[i].Parent
	siblings := slices.DeleteFunc(l.children(parent), func(s string) bool { return s == uuid })
	position = max(0, min(position, len(siblings)))
	siblings = slices.Insert(siblings, position, uuid)
	l.m.Items[i].Parent = parent
	l.renumber(siblings)
	if old != parent {
		l.renumber(l.children(old))
	}
	return l.save()
}

// Delete removes an item, its descendants and their media files.
func (l *Library) Delete(uuid string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := l.index(uuid)
	if i < 0 {
		return ErrNotFound
	}
	parent := l.m.Items[i].Parent
	doomed := map[string]bool{uuid: true}
	for changed := true; changed; {
		changed = false
		for _, it := range l.m.Items {
			if doomed[it.Parent] && !doomed[it.UUID] {
				doomed[it.UUID], changed = true, true
			}
		}
	}
	l.m.Items = slices.DeleteFunc(l.m.Items, func(it Item) bool { return doomed[it.UUID] })
	for id := range doomed {
		os.Remove(l.AudioPath(id))
		os.Remove(l.ImagePath(id))
	}
	l.renumber(l.children(parent))
	return l.save()
}

// Adopt replaces the (empty) library with a manifest found on the box, fetching
// each media file through fetch. Downloads run without the lock, so the UI keeps
// reading the library meanwhile; the manifest switches over at the end.
func (l *Library) Adopt(m Manifest, fetch func(name string) ([]byte, error)) error {
	for _, it := range m.Items {
		for _, name := range mediaNames(it) {
			data, err := fetch(name)
			if err != nil {
				return fmt.Errorf("library: fetching %s: %w", name, err)
			}
			if _, err := l.writeMedia(name, data); err != nil {
				return err
			}
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.m = Manifest{Version: ManifestVersion, Items: slices.Clone(m.Items)}
	return l.save()
}

func mediaNames(it Item) []string {
	names := []string{it.UUID + ".jpg"}
	if it.Kind == Story {
		names = append(names, it.UUID+".mp3")
	}
	return names
}

// checkParent accepts "" (root), official UUIDs, and custom folders that are not
// moving (itself or a descendant of it).
func (l *Library) checkParent(parent, moving string) error {
	for p := parent; p != ""; {
		i := l.index(p)
		if i < 0 {
			return nil // official folder
		}
		if l.m.Items[i].Kind != Folder || p == moving {
			return ErrBadParent
		}
		p = l.m.Items[i].Parent
	}
	return nil
}

// children returns the UUIDs of parent's custom children ordered by Position.
func (l *Library) children(parent string) []string {
	var kids []Item
	for _, it := range l.m.Items {
		if it.Parent == parent {
			kids = append(kids, it)
		}
	}
	slices.SortStableFunc(kids, func(a, b Item) int { return a.Position - b.Position })
	ids := make([]string, len(kids))
	for i, it := range kids {
		ids[i] = it.UUID
	}
	return ids
}

func (l *Library) renumber(ids []string) {
	for pos, id := range ids {
		l.m.Items[l.index(id)].Position = pos
	}
}

func (l *Library) index(uuid string) int {
	return slices.IndexFunc(l.m.Items, func(it Item) bool { return it.UUID == uuid })
}

func (l *Library) writeMedia(name string, data []byte) (*Blob, error) {
	if err := writeAtomic(l.mediaPath(name), data); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return &Blob{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}, nil
}

func (l *Library) writeMediaFrom(name string, r io.Reader) (*Blob, error) {
	tmp, err := os.CreateTemp(filepath.Join(l.dir, "media"), ".tmp-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), r)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), l.mediaPath(name)); err != nil {
		return nil, err
	}
	return &Blob{SHA256: hex.EncodeToString(h.Sum(nil)), Size: size}, nil
}

func (l *Library) save() error {
	l.m.Version, l.m.Updated = ManifestVersion, l.now().UTC()
	data, err := l.m.JSON()
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(l.dir, "library.json"), data); err != nil {
		return err
	}
	l.neverSaved = false
	return nil
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// newUUID returns a random (version 4) UUID.
func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0F | 0x40
	b[8] = b[8]&0x3F | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
