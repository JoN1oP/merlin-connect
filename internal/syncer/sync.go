package syncer

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"merlin-connect/internal/box"
	"merlin-connect/internal/library"
	"merlin-connect/internal/playlist"
)

// File names on the box.
const (
	ManifestName = "merlin-connect.json"
	BoxPlaylist  = "playlist.bin"
)

// Limits enforced by the official app before a sync.
const (
	MaxItems   = 400
	MinBattery = 15
)

var (
	ErrTooManyItems = fmt.Errorf("syncer: more than %d items", MaxItems)
	// ErrBoxUnread refuses a sync whose box content was not read: its official
	// part would be empty and the box would lose everything but custom items.
	ErrBoxUnread = errors.New("syncer: box content not read")
)

// Box is the part of box.Client a sync needs.
type Box interface {
	Stat(name string) (box.FileStat, error)
	Hash(name string, size int64) (box.FileStat, error)
	GetFile(name string) ([]byte, error)
	Upload(name string, data []byte, progress func(sent int64)) error
}

// File is a local media file the box must hold.
type File struct {
	Name   string // name on the box, e.g. "<uuid>.mp3"
	Path   string // local path
	SHA256 string
	Size   int64
	Label  string // item title, for progress display
	// Synced is the hash the box's manifest records for this file: the box
	// already holds this content if the name and size match.
	Synced string
}

// Plan is everything one sync sends.
type Plan struct {
	Tree     *playlist.Node
	Files    []File
	Manifest []byte
	Custom   []string // custom UUIDs the box must list afterwards
}

// Result is the box's playlist after a sync.
type Result struct {
	Raw  []byte // playlist.bin as read back from the box
	Tree *playlist.Node
}

// Progress reports a running sync.
type Progress struct {
	Phase string `json:"phase"` // "files", "playlist" or "verify"
	Label string `json:"label"`
	File  int    `json:"file"`  // 1-based index of the current file
	Files int    `json:"files"` // number of files in the plan
	Done  int64  `json:"done"`  // bytes on the box, uploaded or already there
	Sent  int64  `json:"sent"`  // bytes actually uploaded, for the speed
	Total int64  `json:"total"`
}

// NewPlan prepares a sync of lib onto a box holding boxTree and boxItems (the
// items of the manifest found on the box, if any).
func NewPlan(boxTree *playlist.Node, lib *library.Library, boxItems []library.Item) (Plan, error) {
	if boxTree == nil {
		return Plan{}, ErrBoxUnread
	}
	m := lib.Manifest()
	tree := Merge(boxTree, m.Items, boxItems, true)
	if tree.Count() > MaxItems {
		return Plan{}, ErrTooManyItems
	}
	items := map[string]library.Item{}
	for _, it := range m.Items {
		items[it.UUID] = it
	}
	synced := map[string]string{} // file name -> hash the box manifest records
	for _, it := range boxItems {
		synced[it.UUID+".jpg"] = it.Image.SHA256
		if it.Kind == library.Story {
			synced[it.UUID+".mp3"] = it.Audio.SHA256
		}
	}
	p := Plan{Tree: tree}
	tree.Walk(func(n, _ *playlist.Node) {
		it, ok := items[n.UUID]
		if !ok {
			return
		}
		p.Custom = append(p.Custom, it.UUID)
		p.Files = append(p.Files, File{Name: it.UUID + ".jpg", Path: lib.ImagePath(it.UUID),
			SHA256: it.Image.SHA256, Size: it.Image.Size, Label: it.Title, Synced: synced[it.UUID+".jpg"]})
		if it.Kind == library.Story {
			p.Files = append(p.Files, File{Name: it.UUID + ".mp3", Path: lib.AudioPath(it.UUID),
				SHA256: it.Audio.SHA256, Size: it.Audio.Size, Label: it.Title, Synced: synced[it.UUID+".mp3"]})
		}
	})
	var err error
	p.Manifest, err = m.JSON()
	return p, err
}

// Bytes is the total size of the plan's media files.
func (p Plan) Bytes() int64 {
	var n int64
	for _, f := range p.Files {
		n += f.Size
	}
	return n
}

// Run sends the plan: media files the box lacks, the manifest, then the playlist,
// and returns the box's resulting playlist. The playlist goes last so an interrupted
// sync never leaves it pointing at missing files; running again resumes, as files
// already stored are skipped.
func Run(ctx context.Context, b Box, p Plan, report func(Progress)) (Result, error) {
	total, done, sent := p.Bytes(), int64(0), int64(0)
	for i, f := range p.Files {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		at := Progress{Phase: "files", Label: f.Label, File: i + 1, Files: len(p.Files), Total: total}
		at.Done, at.Sent = done, sent
		report(at)
		stored, err := onBox(b, f)
		if err != nil {
			return Result{}, err
		}
		if !stored {
			data, err := os.ReadFile(f.Path)
			if err != nil {
				return Result{}, err
			}
			if err := b.Upload(f.Name, data, func(n int64) {
				at.Done, at.Sent = done+n, sent+n
				report(at)
			}); err != nil {
				return Result{}, fmt.Errorf("%s: %w", f.Label, err)
			}
			sent += f.Size
		}
		done += f.Size
	}

	report(Progress{Phase: "playlist", Files: len(p.Files), Done: total, Sent: sent, Total: total})
	if err := b.Upload(ManifestName, p.Manifest, nil); err != nil {
		return Result{}, err
	}
	// Written directly: the box's updatePlaylist refuses every playlist with
	// FAVORITES_NOT_FOUND.
	bin := playlist.Encode(p.Tree)
	if err := b.Upload(BoxPlaylist, bin, nil); err != nil {
		return Result{}, err
	}

	report(Progress{Phase: "verify", Files: len(p.Files), Done: total, Sent: sent, Total: total})
	raw, err := b.GetFile(BoxPlaylist)
	if err != nil {
		return Result{}, err
	}
	if !bytes.Equal(raw, bin) {
		return Result{}, errors.New("syncer: playlist.bin differs after writing it")
	}
	tree, err := playlist.Decode(raw)
	if err != nil {
		return Result{}, err
	}
	res := Result{Raw: raw, Tree: tree}
	for _, uuid := range p.Custom {
		if tree.Find(uuid) == nil {
			return res, fmt.Errorf("syncer: %s missing from the box after sync", uuid)
		}
	}
	return res, nil
}

// onBox reports whether the box already holds f. A name and size match is
// enough when the box's manifest records this content: the names are UUIDs of
// our own items. Otherwise the box hashes its copy, which is slow for audio.
func onBox(b Box, f File) (bool, error) {
	st, err := b.Stat(f.Name)
	switch {
	case errors.Is(err, box.ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	case st.Size >= 0 && st.Size != f.Size:
		return false, nil
	case st.Size == f.Size && f.Synced == f.SHA256:
		return true, nil
	}
	st, err = b.Hash(f.Name, max(st.Size, f.Size))
	if err != nil {
		return false, err
	}
	return hex.EncodeToString(st.SHA256[:]) == f.SHA256, nil
}
