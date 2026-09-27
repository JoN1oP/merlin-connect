package app

import (
	"merlin-connect/internal/playlist"
	"merlin-connect/internal/syncer"
)

// Node is one card in the UI: box content merged with the library.
type Node struct {
	UUID     string  `json:"uuid"`
	Title    string  `json:"title"`
	Folder   bool    `json:"folder"`
	Custom   bool    `json:"custom"` // editable; official items are read-only
	OnBox    bool    `json:"onBox"`
	Version  string  `json:"v,omitempty"` // set when a cover exists; changes with it
	Children []*Node `json:"children,omitempty"`
}

// View returns the tree the UI shows: the last known box content with the
// library's items in place, including empty custom folders.
func (s *Session) View() *Node {
	items := s.cfg.Library.Manifest().Items
	version := map[string]string{}
	s.mu.Lock()
	boxTree, boxItems := s.boxTree, s.boxItems
	for uuid := range s.covers {
		version[uuid] = "box" // box covers never change: the UUID names them
	}
	s.mu.Unlock()

	custom := map[string]bool{}
	for _, it := range items {
		custom[it.UUID] = true
		if it.Image != nil {
			version[it.UUID] = it.Image.SHA256[:min(8, len(it.Image.SHA256))]
		}
	}
	onBox := map[string]bool{}
	if boxTree != nil {
		boxTree.Walk(func(n, _ *playlist.Node) { onBox[n.UUID] = true })
	}
	var convert func(n *playlist.Node) *Node
	convert = func(n *playlist.Node) *Node {
		v := &Node{UUID: n.UUID, Title: n.Title, Folder: !n.Story, Custom: custom[n.UUID],
			OnBox: onBox[n.UUID], Version: version[n.UUID]}
		for _, c := range n.Children {
			v.Children = append(v.Children, convert(c))
		}
		return v
	}
	return convert(syncer.Merge(boxTree, items, boxItems, false))
}
