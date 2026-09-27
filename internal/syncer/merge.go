// Package syncer computes what the box should hold and sends it there.
package syncer

import (
	"slices"

	"merlin-connect/internal/library"
	"merlin-connect/internal/playlist"
)

// Merge returns the tree the box should hold: boxTree without any custom item
// (from the local library or the box's manifest; favorites of custom stories
// still in the library are kept), with the library's items attached. Custom items follow the official children of their folder; items whose
// parent no longer exists go to the root. With prune, custom folders that have no
// story beneath them are left out, since the box would read them as stories.
func Merge(boxTree *playlist.Node, items, boxItems []library.Item, prune bool) *playlist.Node {
	custom := map[string]bool{}
	for _, it := range slices.Concat(items, boxItems) {
		custom[it.UUID] = true
	}
	local := map[string]bool{}
	for _, it := range items {
		local[it.UUID] = true
	}
	root := &playlist.Node{}
	if boxTree != nil {
		root = officialOnly(boxTree, custom, local)
	}

	folders := map[string]*playlist.Node{"": root}
	root.Walk(func(n, _ *playlist.Node) {
		if !n.Story {
			folders[n.UUID] = n
		}
	})
	nodes := make(map[string]*playlist.Node, len(items))
	for _, it := range items {
		n := &playlist.Node{UUID: it.UUID, Title: it.Title, Story: it.Kind == library.Story, AddTime: it.Added}
		nodes[it.UUID] = n
		if !n.Story {
			folders[it.UUID] = n
		}
	}
	sorted := slices.Clone(items)
	slices.SortStableFunc(sorted, func(a, b library.Item) int { return a.Position - b.Position })
	var orphans []*playlist.Node
	for _, it := range sorted {
		if parent := folders[it.Parent]; parent != nil {
			parent.Children = append(parent.Children, nodes[it.UUID])
		} else {
			orphans = append(orphans, nodes[it.UUID])
		}
	}
	root.Children = append(root.Children, orphans...)
	if prune {
		pruneEmpty(root, custom)
	}
	return root
}

// officialOnly clones n without the custom nodes (and what is below them),
// except favorite references to custom stories still in the library.
func officialOnly(n *playlist.Node, custom, local map[string]bool) *playlist.Node {
	c := *n // keep times and type: a direct playlist.bin write sends them back
	c.Children = nil
	favorites := n.Title == playlist.FavoritesTitle
	for _, child := range n.Children {
		if !custom[child.UUID] || favorites && local[child.UUID] {
			c.Children = append(c.Children, officialOnly(child, custom, local))
		}
	}
	return &c
}

// pruneEmpty drops custom folders without stories below them and reports
// whether n holds a story.
func pruneEmpty(n *playlist.Node, custom map[string]bool) bool {
	hasStory := false
	kept := n.Children[:0]
	for _, c := range n.Children {
		if c.Story {
			hasStory = true
			kept = append(kept, c)
			continue
		}
		sub := pruneEmpty(c, custom)
		if sub || !custom[c.UUID] {
			kept = append(kept, c)
		}
		hasStory = hasStory || sub
	}
	n.Children = kept
	return hasStory
}

// Pending counts the custom items whose state on the box differs from the
// library: new, changed, missing from the box tree, or deleted locally.
func Pending(boxTree *playlist.Node, items, boxItems []library.Item) int {
	onBox := map[string]bool{}
	if boxTree != nil {
		boxTree.Walk(func(n, _ *playlist.Node) { onBox[n.UUID] = true })
	}
	included := map[string]bool{}
	Merge(boxTree, items, boxItems, true).Walk(func(n, _ *playlist.Node) { included[n.UUID] = true })
	previous := map[string]library.Item{}
	for _, it := range boxItems {
		previous[it.UUID] = it
	}
	local := map[string]bool{}
	count := 0
	for _, it := range items {
		local[it.UUID] = true
		old, known := previous[it.UUID]
		if included[it.UUID] && (!onBox[it.UUID] || !known || !sameItem(old, it)) {
			count++
		}
	}
	for _, it := range boxItems {
		if !local[it.UUID] && onBox[it.UUID] {
			count++
		}
	}
	return count
}

func sameItem(a, b library.Item) bool {
	return a.Kind == b.Kind && a.Title == b.Title && a.Parent == b.Parent &&
		a.Position == b.Position && sameBlob(a.Audio, b.Audio) && sameBlob(a.Image, b.Image)
}

func sameBlob(a, b *library.Blob) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
