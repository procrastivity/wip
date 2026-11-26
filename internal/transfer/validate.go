package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

func validID(id string) bool {
	if len(id) != 26 || id[0] > '7' {
		return false
	}
	for i := range id {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", rune(id[i])) {
			return false
		}
	}
	return true
}

// Lineage is an opaque label, never a filesystem path. The bound applies to
// this identity label only, not user work, prose, or archive sizes.
func validLineage(s string) bool {
	return len(s) > 0 && len(s) <= 1024 && utf8.ValidString(s) &&
		strings.TrimSpace(s) != "" && strings.IndexFunc(s, unicode.IsControl) < 0
}

func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validText(values ...string) bool {
	for _, value := range values {
		if !utf8.ValidString(value) {
			return false
		}
	}
	return true
}

// ValidateMetadata checks the complete closed snapshot graph, without requiring
// content bytes. It is suitable for metadata-only event projection.
func ValidateMetadata(s *Snapshot) error {
	if s == nil {
		return fmt.Errorf("transfer: nil snapshot")
	}
	if s.Version != Version || s.Profile != Profile {
		return fmt.Errorf("transfer: unsupported version/profile %d/%q", s.Version, s.Profile)
	}
	if !validLineage(s.Lineage) || !validID(s.Repo) {
		return fmt.Errorf("transfer: invalid source lineage or repo")
	}
	if !validText(s.Revision) || !validText(s.Exclusions...) {
		return fmt.Errorf("transfer: invalid UTF-8 metadata")
	}
	ids := make(map[string]bool)
	type originKey struct{ kind, lineage, repo, id string }
	origins := make(map[originKey]bool)
	identity := func(id, kind string, o Origin) error {
		if !validID(id) || !validID(o.ID) || !validID(o.Repo) || !validLineage(o.Lineage) {
			return fmt.Errorf("transfer: invalid %s identity/origin %q", kind, id)
		}
		if ids[id] {
			return fmt.Errorf("transfer: duplicate global ID %s", id)
		}
		ids[id] = true
		key := originKey{kind, o.Lineage, o.Repo, o.ID}
		if origins[key] {
			return fmt.Errorf("transfer: duplicate %s origin for %s", kind, id)
		}
		origins[key] = true
		return nil
	}
	nodes := make(map[string]Node)
	locators := make(map[[2]string]bool)
	for _, n := range s.Nodes {
		if err := identity(n.ID, n.Kind, n.Origin); err != nil {
			return err
		}
		switch n.Kind {
		case "matter", "stage", "step":
		default:
			return fmt.Errorf("transfer: unsupported node kind %q", n.Kind)
		}
		switch n.Lifecycle {
		case "planned", "in-progress", "done", "canceled", "paused":
		default:
			return fmt.Errorf("transfer: invalid lifecycle on %s", n.ID)
		}
		if n.Lifecycle == "canceled" && strings.TrimSpace(n.Reason) == "" {
			return fmt.Errorf("transfer: canceled node %s needs a reason", n.ID)
		}
		if !validText(n.Title, n.Locator, n.Reason) || strings.TrimSpace(n.Title) == "" || strings.TrimSpace(n.Locator) == "" {
			return fmt.Errorf("transfer: node %s needs title and locator", n.ID)
		}
		key := [2]string{n.Matter, n.Locator}
		if locators[key] {
			return fmt.Errorf("transfer: duplicate locator %q in Matter %s", n.Locator, n.Matter)
		}
		locators[key] = true
		nodes[n.ID] = n
	}
	for _, n := range s.Nodes {
		root, ok := nodes[n.Matter]
		if !ok || root.Kind != "matter" {
			return fmt.Errorf("transfer: missing Matter root for %s", n.ID)
		}
		if n.Kind == "matter" {
			if n.Parent != "" || n.Matter != n.ID {
				return fmt.Errorf("transfer: invalid Matter hierarchy for %s", n.ID)
			}
			continue
		}
		p, ok := nodes[n.Parent]
		if !ok || p.Matter != n.Matter || (p.Kind != "matter" && (n.Kind != "step" || p.Kind != "stage")) {
			return fmt.Errorf("transfer: invalid parent hierarchy for %s", n.ID)
		}
	}
	entries := make(map[string]Entry)
	for _, e := range s.Backlog {
		if err := identity(e.ID, "backlog", e.Origin); err != nil {
			return err
		}
		if e.Provenance != "intake" && e.Provenance != "found" && e.Provenance != "deferred" {
			return fmt.Errorf("transfer: invalid backlog provenance on %s", e.ID)
		}
		if !validText(e.Title, e.Detail, e.Reason) || strings.TrimSpace(e.Title) == "" {
			return fmt.Errorf("transfer: backlog %s needs a title", e.ID)
		}
		if e.OriginNode != "" {
			if _, ok := nodes[e.OriginNode]; !ok {
				return fmt.Errorf("transfer: missing backlog origin node for %s", e.ID)
			}
		}
		switch e.State {
		case "planned":
			if n, ok := nodes[e.Matter]; !ok || n.Kind != "matter" {
				return fmt.Errorf("transfer: planned backlog %s needs a Matter root", e.ID)
			}
		case "entered", "declined":
			if e.Matter != "" {
				return fmt.Errorf("transfer: unplanned backlog %s has a Matter", e.ID)
			}
		default:
			return fmt.Errorf("transfer: unsupported backlog state on %s", e.ID)
		}
		if e.State == "declined" && strings.TrimSpace(e.Reason) == "" {
			return fmt.Errorf("transfer: declined backlog %s needs a reason", e.ID)
		}
		entries[e.ID] = e
	}
	for _, selection := range []struct {
		ids  []string
		node bool
	}{{s.Selection.Matters, true}, {s.Selection.Backlog, false}} {
		seen := make(map[string]bool)
		for _, id := range selection.ids {
			n, nok := nodes[id]
			_, eok := entries[id]
			if seen[id] || selection.node && (!nok || n.Kind != "matter") || !selection.node && !eok {
				return fmt.Errorf("transfer: duplicate or unresolved selection %s", id)
			}
			seen[id] = true
		}
	}
	adj := make(map[string][]string)
	indegree := make(map[string]int)
	pairs := make(map[[2]string]bool)
	for _, e := range s.Edges {
		if err := identity(e.ID, "edge", e.Origin); err != nil {
			return err
		}
		_, a := nodes[e.Blocked]
		_, b := nodes[e.Blocker]
		key := [2]string{e.Blocked, e.Blocker}
		if !a || !b || e.Blocked == e.Blocker || pairs[key] {
			return fmt.Errorf("transfer: invalid or duplicate dependency %s", e.ID)
		}
		pairs[key] = true
		adj[e.Blocker] = append(adj[e.Blocker], e.Blocked)
		indegree[e.Blocked]++
	}
	// Iterative topological traversal avoids recursion on untrusted long graphs.
	queue := make([]string, 0, len(nodes))
	for id := range nodes {
		if indegree[id] == 0 {
			queue = append(queue, id)
		}
	}
	for i := 0; i < len(queue); i++ {
		for _, id := range adj[queue[i]] {
			indegree[id]--
			if indegree[id] == 0 {
				queue = append(queue, id)
			}
		}
	}
	if len(queue) != len(nodes) {
		return fmt.Errorf("transfer: dependency cycle")
	}
	type contentKey struct {
		owner, kind string
		order       int
	}
	contentKeys := make(map[contentKey]bool)
	lengths := make(map[string]int64)
	for _, c := range s.Contents {
		if err := identity(c.ID, "content", c.Origin); err != nil {
			return err
		}
		_, node := nodes[c.Owner]
		_, entry := entries[c.Owner]
		if !node && !entry || !node && c.Kind != "findings" {
			return fmt.Errorf("transfer: invalid content owner for %s", c.ID)
		}
		switch c.Kind {
		case "brief", "workplan", "body", "findings":
		default:
			return fmt.Errorf("transfer: invalid content kind on %s", c.ID)
		}
		if c.Length < 0 || !validHash(c.SHA256) || c.Order < 0 {
			return fmt.Errorf("transfer: invalid content metadata on %s", c.ID)
		}
		order := c.Order
		if c.Kind != "findings" {
			order = 0 // create-once regardless of supplied presentation order
		}
		key := contentKey{c.Owner, c.Kind, order}
		if contentKeys[key] {
			return fmt.Errorf("transfer: duplicate content kind/order for %s", c.ID)
		}
		contentKeys[key] = true
		if length, ok := lengths[c.SHA256]; ok && length != c.Length {
			return fmt.Errorf("transfer: inconsistent length for shared content %s", c.SHA256)
		}
		lengths[c.SHA256] = c.Length
	}
	return nil
}

// Validate checks metadata and the actual bytes of every content object. Nil
// Data represents zero bytes and is valid only for the empty-content digest.
func Validate(s *Snapshot) error {
	if err := ValidateMetadata(s); err != nil {
		return err
	}
	for _, c := range s.Contents {
		sum := sha256.Sum256(c.Data)
		if int64(len(c.Data)) != c.Length || hex.EncodeToString(sum[:]) != c.SHA256 {
			return fmt.Errorf("transfer: content integrity failure for %s", c.ID)
		}
	}
	return nil
}
