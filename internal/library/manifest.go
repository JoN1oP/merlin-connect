// Package library stores the custom items (the manifest) and their media on disk.
package library

import (
	"encoding/json"
	"fmt"
	"time"
)

// ManifestVersion is the manifest format this build reads and writes.
const ManifestVersion = 1

// Kind tells folders from stories.
type Kind string

const (
	Folder Kind = "folder"
	Story  Kind = "story"
)

// Blob identifies a media file by content.
type Blob struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Item is one custom folder or story. Parent is "" for the box root, or the UUID
// of any folder, official or custom. Custom items come after the official items of
// the same folder, ordered by Position.
type Item struct {
	UUID     string `json:"uuid"`
	Kind     Kind   `json:"kind"`
	Title    string `json:"title"`
	Parent   string `json:"parent"`
	Position int    `json:"position"`
	Added    uint32 `json:"added,omitempty"` // unix seconds; the box's add_time
	Audio    *Blob  `json:"audio,omitempty"`
	Image    *Blob  `json:"image,omitempty"`
}

// Manifest lists every custom item. It is saved locally as library.json and on
// the box as merlin-connect.json.
type Manifest struct {
	Version int       `json:"version"`
	Updated time.Time `json:"updated"`
	Items   []Item    `json:"items"`
}

// ParseManifest decodes a manifest and checks its version.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("library: bad manifest: %w", err)
	}
	if m.Version != ManifestVersion {
		return Manifest{}, fmt.Errorf("library: unsupported manifest version %d", m.Version)
	}
	return m, nil
}

// JSON encodes the manifest.
func (m Manifest) JSON() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}
