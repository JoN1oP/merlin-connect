package playlist

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// jsonNode is the box's playlist JSON shape (confirmed on a real box: every
// node needs add_time and limit_time). A node with "child" (even empty) is a
// folder, a node without it is a story.
type jsonNode struct {
	UUID      string      `json:"uuid"`
	Title     string      `json:"title"`
	AddTime   uint32      `json:"add_time"`
	LimitTime uint32      `json:"limit_time"`
	Child     *[]jsonNode `json:"child,omitempty"`
}

// BoxJSON renders the tree as the JSON array the box accepts in updatePlaylist.
// Titles are written raw: the box counts escapes like \u0026 against the
// 64-byte title limit.
func (n *Node) BoxJSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(toJSON(n.Children)); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func toJSON(nodes []*Node) []jsonNode {
	out := make([]jsonNode, 0, len(nodes))
	for _, n := range nodes {
		j := jsonNode{UUID: n.UUID, Title: n.Title, AddTime: n.AddTime, LimitTime: n.LimitTime}
		if !n.Story {
			children := toJSON(n.Children)
			j.Child = &children
		}
		out = append(out, j)
	}
	return out
}

// ParseBoxJSON reads a playlist JSON back into a tree (used by the fake box).
func ParseBoxJSON(data []byte) (*Node, error) {
	var nodes []jsonNode
	if err := json.Unmarshal(data, &nodes); err != nil {
		return nil, fmt.Errorf("playlist: %w", err)
	}
	return &Node{Children: fromJSON(nodes)}, nil
}

func fromJSON(nodes []jsonNode) []*Node {
	var out []*Node
	for _, j := range nodes {
		n := &Node{UUID: j.UUID, Title: j.Title, Story: j.Child == nil, AddTime: j.AddTime, LimitTime: j.LimitTime}
		if j.Child != nil {
			n.Children = fromJSON(*j.Child)
		}
		out = append(out, n)
	}
	return out
}
