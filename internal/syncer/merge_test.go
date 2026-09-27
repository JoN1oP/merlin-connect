package syncer

import (
	"strings"
	"testing"

	"merlin-connect/internal/library"
	"merlin-connect/internal/playlist"
)

// official is a box tree: Histoires/{s1,s2}, a custom story "c-old" left by a
// previous sync, and the favorites folder.
func official() *playlist.Node {
	return &playlist.Node{Children: []*playlist.Node{
		{UUID: "hist", Title: "Histoires", Children: []*playlist.Node{
			{UUID: "s1", Title: "Au lit", Story: true},
			{UUID: "s2", Title: "Docteur", Story: true},
		}},
		{UUID: "c-old", Title: "Ancienne", Story: true},
		{UUID: "fav", Title: "Merlin_favorite"},
	}}
}

// outline renders a tree as "Title(child,child)" for compact assertions.
func outline(n *playlist.Node) string {
	var parts []string
	for _, c := range n.Children {
		s := c.Title
		if !c.Story {
			s += "(" + outline(c) + ")"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ",")
}

var items = []library.Item{
	{UUID: "c1", Kind: library.Story, Title: "Loup", Parent: "hist", Position: 0},
	{UUID: "cf", Kind: library.Folder, Title: "Contes", Parent: "", Position: 1},
	{UUID: "c2", Kind: library.Story, Title: "Pirates", Parent: "cf", Position: 0},
	{UUID: "c0", Kind: library.Story, Title: "Dodo", Parent: "", Position: 0},
	{UUID: "empty", Kind: library.Folder, Title: "Vide", Parent: "", Position: 2},
	{UUID: "lost", Kind: library.Story, Title: "Perdu", Parent: "gone", Position: 0},
}

func TestMerge(t *testing.T) {
	boxItems := []library.Item{{UUID: "c-old", Kind: library.Story}}
	got := outline(Merge(official(), items, boxItems, false))
	want := "Histoires(Au lit,Docteur,Loup),Merlin_favorite(),Dodo,Contes(Pirates),Vide(),Perdu"
	if got != want {
		t.Fatalf("Merge =\n%s\nwant\n%s", got, want)
	}
	pruned := outline(Merge(official(), items, boxItems, true))
	if strings.Contains(pruned, "Vide") || !strings.Contains(pruned, "Merlin_favorite()") {
		t.Fatalf("pruned = %s (drop empty custom folders only)", pruned)
	}
}

func TestMergeKeepsCustomFavorites(t *testing.T) {
	box := official()
	fav := box.Children[2]
	fav.Children = []*playlist.Node{
		{UUID: "s1", Title: "Au lit", Story: true},
		{UUID: "c1", Title: "Loup", Story: true},        // a custom story marked favorite on the box
		{UUID: "c-old", Title: "Ancienne", Story: true}, // deleted locally since
	}
	got := outline(Merge(box, items, []library.Item{{UUID: "c-old"}}, true))
	if !strings.Contains(got, "Merlin_favorite(Au lit,Loup)") || !strings.Contains(got, "Histoires(Au lit,Docteur,Loup)") {
		t.Fatalf("Merge = %s", got)
	}
}

func TestMergeGivesCustomItemsTheirAddedDate(t *testing.T) {
	story := library.Item{UUID: "c", Kind: library.Story, Title: "Loup", Added: 1790000000}
	if got := Merge(official(), []library.Item{story}, nil, true).Find("c"); got.AddTime != 1790000000 || got.LimitTime != 0 {
		t.Fatalf("node = %+v", got)
	}
}

func TestMergeKeepsOfficialDetails(t *testing.T) {
	box := official()
	s1 := box.Find("s1")
	s1.Type, s1.AddTime, s1.LimitTime = playlist.TypeStory, 1784037560, 1814392800
	got := Merge(box, items, nil, true).Find("s1")
	if got.Type != s1.Type || got.AddTime != s1.AddTime || got.LimitTime != s1.LimitTime {
		t.Fatalf("s1 = %+v, want %+v", got, s1)
	}
}

func TestMergeWithoutBoxTree(t *testing.T) {
	if got := outline(Merge(nil, items[3:4], nil, true)); got != "Dodo" {
		t.Fatalf("got %s", got)
	}
}

func TestPending(t *testing.T) {
	synced := Merge(official(), items, nil, true)
	if n := Pending(synced, items, items); n != 0 {
		t.Fatalf("in sync: pending = %d", n)
	}
	renamed := append([]library.Item(nil), items...)
	renamed[0].Title = "Grand loup"
	if n := Pending(synced, renamed, items); n != 1 {
		t.Fatalf("renamed: pending = %d", n)
	}
	// An official sync wiped the custom items: all 5 non-empty ones are pending.
	if n := Pending(official(), items, items); n != 5 {
		t.Fatalf("wiped: pending = %d", n)
	}
	// c-old was deleted locally but is still on the box.
	if n := Pending(official(), nil, []library.Item{{UUID: "c-old"}}); n != 1 {
		t.Fatalf("deleted: pending = %d", n)
	}
	if n := Pending(nil, items, nil); n != 5 {
		t.Fatalf("never connected: pending = %d", n)
	}
}
