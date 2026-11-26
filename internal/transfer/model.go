// Package transfer describes the versioned offline snapshot format. It has no
// store, configuration, credential or network dependencies.
package transfer

// Version is the supported manifest format version.
const Version = 1

// Profile identifies the closed, selected-work snapshot schema.
const Profile = "snapshot-v1"

// Origin survives local ID remapping and subsequent exports.
type Origin struct {
	Lineage string `json:"lineage"`
	Repo    string `json:"repo"`
	ID      string `json:"id"`
}

// Selection identifies the included Matter roots and backlog entries.
type Selection struct {
	Matters []string `json:"matters"`
	Backlog []string `json:"backlog"`
}

// Node is a Matter, Stage, or Step in the selected hierarchy.
type Node struct {
	ID        string `json:"id"`
	Origin    Origin `json:"origin"`
	Kind      string `json:"kind"`
	Parent    string `json:"parent,omitempty"`
	Matter    string `json:"matter"`
	Locator   string `json:"locator"`
	Title     string `json:"title"`
	Lifecycle string `json:"lifecycle"`
	SortKey   int64  `json:"sort_key"`
	Reason    string `json:"reason,omitempty"`
}

// Entry is an entered, planned, or declined backlog item.
type Entry struct {
	ID         string `json:"id"`
	Origin     Origin `json:"origin"`
	Provenance string `json:"provenance"`
	State      string `json:"state"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	OriginNode string `json:"origin_node,omitempty"`
	Matter     string `json:"matter,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// Edge records a directed blocked-by dependency between nodes.
type Edge struct {
	ID      string `json:"id"`
	Origin  Origin `json:"origin"`
	Blocked string `json:"blocked"`
	Blocker string `json:"blocker"`
}

// Content describes one immutable prose segment and its digest-addressed bytes.
type Content struct {
	ID     string `json:"id"`
	Origin Origin `json:"origin"`
	Owner  string `json:"owner"`
	Kind   string `json:"kind"`
	Order  int    `json:"order"`
	Length int64  `json:"length"`
	SHA256 string `json:"sha256"`
	// Data is supplied by the archive's digest-addressed files, not JSON.
	Data []byte `json:"-"`
}

// Snapshot is the versioned offline manifest with optional in-memory blob data.
type Snapshot struct {
	Version    int       `json:"version"`
	Profile    string    `json:"profile"`
	Lineage    string    `json:"lineage"`
	Repo       string    `json:"repo"`
	Revision   string    `json:"revision"`
	Selection  Selection `json:"selection"`
	Nodes      []Node    `json:"nodes"`
	Backlog    []Entry   `json:"backlog"`
	Edges      []Edge    `json:"edges"`
	Contents   []Content `json:"contents"`
	Exclusions []string  `json:"exclusions"`
}

// Inventory includes terminal entries and unsupported-work diagnostics.
type Inventory struct {
	Repo    string   `json:"repo"`
	Matters []Node   `json:"matters"`
	Backlog []Entry  `json:"backlog"`
	Notes   []string `json:"notes"`
}
