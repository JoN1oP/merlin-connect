// Package playlist models the box's content tree: it decodes the box's playlist.bin,
// encodes it (for tests and the fake box), and writes the JSON the box accepts in
// updatePlaylist.
package playlist

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxTitleBytes is the longest title updatePlaylist accepts, in bytes of JSON
// text (measured on the real box; titles written to the SD card reached 64).
const MaxTitleBytes = 63

// FavoritesTitle names the box's favorites folder. In the tree its children are
// references to the favorite stories; playlist.bin stores them as fav_order.
const FavoritesTitle = "Merlin_favorite"

// Record types stored in playlist.bin.
const (
	TypeRoot      = 1
	TypeFolder    = 2
	TypeStory     = 4
	TypeFavorites = 10
	TypeStoryImg  = 36
)

const (
	recordSize = 152
	uuidMax    = 64
	titleMax   = 66
)

// Record is one raw 152-byte entry of playlist.bin.
type Record struct {
	ID, ParentID, Order, Children, FavOrder, Type uint16
	LimitTime, AddTime                            uint32
	UUID, Title                                   string
}

// Node is one entry of the content tree. The root has an empty UUID.
type Node struct {
	UUID      string
	Title     string
	Story     bool
	AddTime   uint32 // unix seconds, when the box got it
	LimitTime uint32 // unix seconds, expiry of subscription content; 0 = none
	Type      uint16 // story record type read from the box (4 or 36); 0 for new stories
	Children  []*Node
}

// Records decodes playlist.bin into its raw records.
func Records(data []byte) ([]Record, error) {
	if len(data)%recordSize != 0 {
		return nil, fmt.Errorf("playlist: size %d is not a multiple of %d", len(data), recordSize)
	}
	records := make([]Record, 0, len(data)/recordSize)
	for off := 0; off < len(data); off += recordSize {
		b := data[off : off+recordSize]
		r := Record{
			ID:        binary.LittleEndian.Uint16(b[0:]),
			ParentID:  binary.LittleEndian.Uint16(b[2:]),
			Order:     binary.LittleEndian.Uint16(b[4:]),
			Children:  binary.LittleEndian.Uint16(b[6:]),
			FavOrder:  binary.LittleEndian.Uint16(b[8:]),
			Type:      binary.LittleEndian.Uint16(b[10:]),
			LimitTime: binary.LittleEndian.Uint32(b[12:]),
			AddTime:   binary.LittleEndian.Uint32(b[16:]),
		}
		var err error
		if r.UUID, err = field(b[20:], uuidMax); err != nil {
			return nil, fmt.Errorf("playlist: record %d uuid: %w", r.ID, err)
		}
		if r.Title, err = field(b[20+1+uuidMax:], titleMax); err != nil {
			return nil, fmt.Errorf("playlist: record %d title: %w", r.ID, err)
		}
		records = append(records, r)
	}
	return records, nil
}

func field(b []byte, max int) (string, error) {
	n := int(b[0])
	if n > max {
		return "", fmt.Errorf("length %d exceeds %d", n, max)
	}
	return string(b[1 : 1+n]), nil
}

// Decode builds the content tree from playlist.bin. Records whose parent is
// missing are dropped, like the box does when it cannot reach them.
func Decode(data []byte) (*Node, error) {
	records, err := Records(data)
	if err != nil {
		return nil, err
	}
	nodes := make(map[uint16]*Node, len(records))
	var root, favorites *Node
	var favs []Record
	for _, r := range records {
		n := &Node{UUID: r.UUID, Title: r.Title, Story: r.Type == TypeStory || r.Type == TypeStoryImg,
			AddTime: r.AddTime, LimitTime: r.LimitTime}
		if n.Story {
			n.Type = r.Type
		}
		switch {
		case r.Type == TypeRoot:
			root = &Node{}
			n = root
		case r.Type == TypeFavorites:
			favorites = n
		case n.Story && r.FavOrder > 0:
			favs = append(favs, r)
		}
		nodes[r.ID] = n
	}
	if root == nil {
		return nil, fmt.Errorf("playlist: no root record")
	}
	ordered := slices.Clone(records)
	slices.SortStableFunc(ordered, func(a, b Record) int {
		return int(a.Order) - int(b.Order)
	})
	for _, r := range ordered {
		parent, ok := nodes[r.ParentID]
		if r.Type == TypeRoot || !ok || parent.Story {
			continue
		}
		parent.Children = append(parent.Children, nodes[r.ID])
	}
	if favorites != nil {
		slices.SortStableFunc(favs, func(a, b Record) int { return int(a.FavOrder) - int(b.FavOrder) })
		seen := map[string]bool{} // a story filed in two folders is one favorite
		for _, r := range favs {
			if !seen[r.UUID] {
				seen[r.UUID] = true
				favorites.Children = append(favorites.Children, &Node{UUID: r.UUID, Title: r.Title, Story: true,
					AddTime: r.AddTime, LimitTime: r.LimitTime, Type: r.Type})
			}
		}
	}
	return root, nil
}

// Encode writes the tree as playlist.bin, numbering records depth-first. The
// favorites folder's references become fav_order on the stories, like the box.
func Encode(root *Node) []byte {
	favOrder := map[string]uint16{}
	for _, c := range root.Children {
		if c.Title == FavoritesTitle {
			for i, f := range c.Children {
				favOrder[f.UUID] = uint16(i + 1)
			}
		}
	}
	var out []byte
	next := uint16(1)
	var visit func(n *Node, parent, order uint16)
	visit = func(n *Node, parent, order uint16) {
		id := next
		next++
		r := Record{ID: id, ParentID: parent, Order: order, UUID: n.UUID, Title: n.Title,
			AddTime: n.AddTime, LimitTime: n.LimitTime}
		switch {
		case parent == 0:
			r.Type, r.Title = TypeRoot, "Root"
		case n.Story:
			r.Type, r.FavOrder = cmp.Or(n.Type, TypeStoryImg), favOrder[n.UUID]
		case n.Title == FavoritesTitle:
			r.Type = TypeFavorites
		default:
			r.Type = TypeFolder
		}
		if !n.Story {
			r.Children = uint16(len(n.Children))
		}
		out = appendRecord(out, r)
		if r.Type == TypeFavorites {
			return // references, not records
		}
		for i, c := range n.Children {
			visit(c, id, uint16(i))
		}
	}
	visit(root, 0, 0)
	return out
}

func appendRecord(out []byte, r Record) []byte {
	b := make([]byte, recordSize)
	binary.LittleEndian.PutUint16(b[0:], r.ID)
	binary.LittleEndian.PutUint16(b[2:], r.ParentID)
	binary.LittleEndian.PutUint16(b[4:], r.Order)
	binary.LittleEndian.PutUint16(b[6:], r.Children)
	binary.LittleEndian.PutUint16(b[8:], r.FavOrder)
	binary.LittleEndian.PutUint16(b[10:], r.Type)
	binary.LittleEndian.PutUint32(b[12:], r.LimitTime)
	binary.LittleEndian.PutUint32(b[16:], r.AddTime)
	b[20] = byte(copy(b[21:21+uuidMax], r.UUID))
	b[21+uuidMax] = byte(copy(b[22+uuidMax:22+uuidMax+titleMax], r.Title))
	return append(out, b...)
}

// Walk calls fn for every node below n (not n itself), parents before children.
func (n *Node) Walk(fn func(node, parent *Node)) {
	for _, c := range n.Children {
		fn(c, n)
		c.Walk(fn)
	}
}

// Find returns the node with the given UUID below n, or nil.
func (n *Node) Find(uuid string) *Node {
	var found *Node
	n.Walk(func(c, _ *Node) {
		if found == nil && c.UUID == uuid {
			found = c
		}
	})
	return found
}

// Count returns the number of nodes below n.
func (n *Node) Count() int {
	count := 0
	n.Walk(func(*Node, *Node) { count++ })
	return count
}

// Clone returns a deep copy of n.
func (n *Node) Clone() *Node {
	c := *n
	c.Children = nil
	for _, child := range n.Children {
		c.Children = append(c.Children, child.Clone())
	}
	return &c
}

// NormalizeTitle collapses spaces and control characters and cuts a title to
// MaxTitleBytes without splitting a character. Accents are kept: the box shows
// them (official titles like "Le rêve de Noé").
func NormalizeTitle(s string) string {
	title := strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
	for len(title) > MaxTitleBytes {
		_, size := utf8.DecodeLastRuneInString(title)
		title = strings.TrimSpace(title[:len(title)-size])
	}
	return title
}
