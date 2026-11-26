package transfer

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testID(n int) string { return fmt.Sprintf("01ARZ3NDEKTSV4RRFFQ69G%04d", n) }

func testOrigin(n int) Origin {
	return Origin{Lineage: "opaque/../lineage", Repo: testID(99), ID: testID(n)}
}

func testSnapshot() *Snapshot {
	return &Snapshot{
		Version: Version, Profile: Profile, Lineage: "local lineage", Repo: testID(99), Revision: "revision",
		Selection: Selection{Matters: []string{testID(1)}, Backlog: []string{testID(4), testID(5), testID(6)}},
		Nodes: []Node{
			{ID: testID(1), Origin: testOrigin(1), Kind: "matter", Matter: testID(1), Locator: "matter", Title: "Matter", Lifecycle: "planned"},
			{ID: testID(2), Origin: testOrigin(2), Kind: "stage", Parent: testID(1), Matter: testID(1), Locator: "stage-01", Title: "Stage", Lifecycle: "in-progress"},
			{ID: testID(3), Origin: testOrigin(3), Kind: "step", Parent: testID(2), Matter: testID(1), Locator: "step-01", Title: "Step", Lifecycle: "done"},
		},
		Backlog: []Entry{
			{ID: testID(4), Origin: testOrigin(4), Provenance: "found", State: "entered", Title: "Entered", OriginNode: testID(3)},
			{ID: testID(5), Origin: testOrigin(5), Provenance: "intake", State: "planned", Title: "Planned", Matter: testID(1)},
			{ID: testID(6), Origin: testOrigin(6), Provenance: "deferred", State: "declined", Title: "Declined", Reason: "Not needed"},
		},
		Edges: []Edge{{ID: testID(7), Origin: testOrigin(7), Blocked: testID(3), Blocker: testID(2)}},
	}
}

func addContent(s *Snapshot, n int, owner, kind string, order int, data []byte) {
	sum := sha256.Sum256(data)
	s.Contents = append(s.Contents, Content{
		ID: testID(n), Origin: testOrigin(n), Owner: owner, Kind: kind,
		Order: order, Length: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Data: data,
	})
}

func TestRoundTrip(t *testing.T) {
	s := testSnapshot()
	addContent(s, 10, testID(1), "brief", 0, []byte("unequal first blob"))
	addContent(s, 11, testID(3), "body", 0, []byte("different bytes"))
	addContent(s, 12, testID(4), "findings", 0, nil)
	addContent(s, 13, testID(4), "findings", 1, bytes.Repeat([]byte("large\x00blob\xff"), 1<<20))
	addContent(s, 14, testID(2), "workplan", 0, []byte("unequal first blob"))
	path := filepath.Join(t.TempDir(), "snapshot.zip")
	if err := Write(path, s); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private archive: %v %v", info, err)
	}
	z, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(z.File) != 5 {
		t.Fatalf("shared digest should use one file; got %d entries", len(z.File))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, content := range got.Contents {
		if !bytes.Equal(content.Data, s.Contents[i].Data) {
			t.Fatalf("content %s differs", content.ID)
		}
	}
	wantDigest, _ := Digest(s)
	gotDigest, err := Digest(got)
	if err != nil || gotDigest != wantDigest {
		t.Fatalf("digest mismatch: %s %s %v", wantDigest, gotDigest, err)
	}
	if err := Write(path, s); err == nil {
		t.Fatal("overwrote existing file")
	}
	link := filepath.Join(t.TempDir(), "link.zip")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := Write(link, s); err == nil {
		t.Fatal("followed existing symlink")
	}
	if _, err := Read(path); err != nil {
		t.Fatal("no-overwrite attempts damaged archive:", err)
	}
}

func TestDigestCanonicalMetadataOnly(t *testing.T) {
	s := testSnapshot()
	addContent(s, 10, testID(1), "brief", 0, []byte("bytes omitted in projector"))
	before, err := Digest(s)
	if err != nil {
		t.Fatal(err)
	}
	s.Nodes[0], s.Nodes[2] = s.Nodes[2], s.Nodes[0]
	s.Backlog[0], s.Backlog[2] = s.Backlog[2], s.Backlog[0]
	s.Selection.Backlog[0], s.Selection.Backlog[2] = s.Selection.Backlog[2], s.Selection.Backlog[0]
	s.Contents[0].Data = nil
	s.Exclusions = []string{}
	after, err := Digest(s)
	if err != nil || after != before {
		t.Fatalf("noncanonical metadata: %s %s %v", before, after, err)
	}
	if err := Validate(s); err == nil {
		t.Fatal("full validation accepted missing Data")
	}
	s.Nodes[0].Title += " changed"
	changed, _ := Digest(s)
	if changed == before {
		t.Fatal("semantic title change did not change digest")
	}
	s.Version++
	if _, err := Digest(s); err == nil {
		t.Fatal("digest accepted unsupported metadata")
	}
}

func TestInvalidMetadata(t *testing.T) {
	cases := map[string]func(*Snapshot){
		"version":                func(s *Snapshot) { s.Version++ },
		"profile":                func(s *Snapshot) { s.Profile = "history" },
		"repo":                   func(s *Snapshot) { s.Repo = "repo-name" },
		"lineage empty":          func(s *Snapshot) { s.Lineage = "" },
		"lineage control":        func(s *Snapshot) { s.Lineage = "bad\nlineage" },
		"lineage bound":          func(s *Snapshot) { s.Lineage = strings.Repeat("x", 1025) },
		"id":                     func(s *Snapshot) { s.Nodes[0].ID = "invalid" },
		"ULID overflow":          func(s *Snapshot) { s.Nodes[0].ID = "81ARZ3NDEKTSV4RRFFQ69G0001" },
		"origin id":              func(s *Snapshot) { s.Nodes[0].Origin.ID = "bad" },
		"origin repo":            func(s *Snapshot) { s.Nodes[0].Origin.Repo = "bad" },
		"origin lineage":         func(s *Snapshot) { s.Nodes[0].Origin.Lineage = "" },
		"duplicate node":         func(s *Snapshot) { s.Nodes = append(s.Nodes, s.Nodes[0]) },
		"duplicate origin":       func(s *Snapshot) { s.Nodes[2].Kind = "stage"; s.Nodes[2].Origin = s.Nodes[1].Origin },
		"global ID collision":    func(s *Snapshot) { s.Edges[0].ID = s.Nodes[0].ID },
		"node kind":              func(s *Snapshot) { s.Nodes[1].Kind = "task" },
		"lifecycle":              func(s *Snapshot) { s.Nodes[0].Lifecycle = "queued" },
		"cancel reason":          func(s *Snapshot) { s.Nodes[0].Lifecycle = "canceled" },
		"invalid UTF-8 title":    func(s *Snapshot) { s.Nodes[0].Title = "\xff" },
		"missing parent":         func(s *Snapshot) { s.Nodes[2].Parent = testID(90) },
		"missing Matter":         func(s *Snapshot) { s.Nodes[2].Matter = testID(90) },
		"nonroot Matter":         func(s *Snapshot) { s.Nodes[2].Matter = testID(2) },
		"Matter parent":          func(s *Snapshot) { s.Nodes[0].Parent = testID(2) },
		"hierarchy cycle":        func(s *Snapshot) { s.Nodes[1].Parent = testID(3) },
		"locator duplicate":      func(s *Snapshot) { s.Nodes[2].Locator = s.Nodes[1].Locator },
		"state":                  func(s *Snapshot) { s.Backlog[0].State = "delegated" },
		"provenance":             func(s *Snapshot) { s.Backlog[0].Provenance = "adhoc" },
		"missing origin node":    func(s *Snapshot) { s.Backlog[0].OriginNode = testID(90) },
		"planned missing Matter": func(s *Snapshot) { s.Backlog[1].Matter = "" },
		"planned nonroot Matter": func(s *Snapshot) { s.Backlog[1].Matter = testID(2) },
		"entered Matter":         func(s *Snapshot) { s.Backlog[0].Matter = testID(1) },
		"declined Matter":        func(s *Snapshot) { s.Backlog[2].Matter = testID(1) },
		"declined reason":        func(s *Snapshot) { s.Backlog[2].Reason = "" },
		"selection missing":      func(s *Snapshot) { s.Selection.Matters = []string{testID(90)} },
		"selection duplicate":    func(s *Snapshot) { s.Selection.Backlog = append(s.Selection.Backlog, testID(4)) },
		"edge missing endpoint":  func(s *Snapshot) { s.Edges[0].Blocker = testID(90) },
		"edge self":              func(s *Snapshot) { s.Edges[0].Blocker = s.Edges[0].Blocked },
		"edge duplicate": func(s *Snapshot) {
			e := s.Edges[0]
			e.ID = testID(8)
			e.Origin = testOrigin(8)
			s.Edges = append(s.Edges, e)
		},
		"edge cycle": func(s *Snapshot) {
			s.Edges = append(s.Edges, Edge{ID: testID(8), Origin: testOrigin(8), Blocked: testID(2), Blocker: testID(3)})
		},
		"content owner missing":       func(s *Snapshot) { s.Contents[0].Owner = testID(90) },
		"content create-once backlog": func(s *Snapshot) { s.Contents[0].Owner = testID(4) },
		"content kind":                func(s *Snapshot) { s.Contents[0].Kind = "credentials" },
		"negative length":             func(s *Snapshot) { s.Contents[0].Length = -1 },
		"bad hash":                    func(s *Snapshot) { s.Contents[0].SHA256 = strings.Repeat("A", 64) },
		"negative order":              func(s *Snapshot) { s.Contents[0].Order = -1 },
		"create-once duplicate":       func(s *Snapshot) { addContent(s, 11, testID(1), "brief", 1, []byte("other")) },
		"findings duplicate order":    func(s *Snapshot) { s.Contents[0].Kind = "findings"; addContent(s, 11, testID(1), "findings", 0, nil) },
		"shared hash length":          func(s *Snapshot) { addContent(s, 11, testID(2), "brief", 0, []byte("blob")); s.Contents[1].Length++ },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := testSnapshot()
			addContent(s, 10, testID(1), "brief", 0, []byte("blob"))
			mutate(s)
			if err := ValidateMetadata(s); err == nil {
				t.Fatal("accepted invalid metadata")
			}
		})
	}
	if err := Validate(nil); err == nil {
		t.Fatal("accepted nil")
	}
	// A Step directly under a Matter is explicitly supported.
	s := testSnapshot()
	s.Nodes[2].Parent = testID(1)
	if err := Validate(s); err != nil {
		t.Fatal(err)
	}
}

type zipEntry struct {
	name  string
	data  []byte
	mode  os.FileMode
	flags uint16
}

func writeTestZip(t *testing.T, entries []zipEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for _, entry := range entries {
		h := &zip.FileHeader{Name: entry.name, Method: zip.Store, Flags: entry.flags}
		if entry.mode != 0 {
			h.SetMode(entry.mode)
		}
		w, err := z.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRejectBadArchives(t *testing.T) {
	s := testSnapshot()
	addContent(s, 10, testID(1), "brief", 0, []byte("blob"))
	manifest, _ := json.Marshal(s)
	base := []zipEntry{{name: "manifest.json", data: manifest}, {name: "content/" + s.Contents[0].SHA256, data: []byte("blob")}}
	cases := map[string][]zipEntry{
		"missing manifest":       base[1:],
		"missing content":        base[:1],
		"corrupt hash":           {base[0], {name: base[1].name, data: []byte("oops")}},
		"wrong length":           {base[0], {name: base[1].name, data: []byte("short")}},
		"unknown manifest field": {{name: "manifest.json", data: bytes.Replace(manifest, []byte(`"version":1`), []byte(`"version":1,"credential":"secret"`), 1)}, base[1]},
		"duplicate manifest key": {{name: "manifest.json", data: bytes.Replace(manifest, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)}, base[1]},
	}
	for name, entry := range map[string]zipEntry{
		"duplicate manifest":  base[0],
		"duplicate content":   base[1],
		"traversal":           {name: "../outside"},
		"absolute":            {name: "/manifest.json"},
		"windows absolute":    {name: `C:\manifest.json`},
		"backslash":           {name: `content\..\outside`},
		"directory":           {name: "content/", mode: os.ModeDir | 0o700},
		"unknown file":        {name: "config.json"},
		"unreferenced digest": {name: "content/" + strings.Repeat("a", 64)},
		"symlink":             {name: "content/" + strings.Repeat("b", 64), mode: os.ModeSymlink | 0o777},
		"device":              {name: "content/" + strings.Repeat("c", 64), mode: os.ModeDevice | 0o600},
		"pipe":                {name: "content/" + strings.Repeat("d", 64), mode: os.ModeNamedPipe | 0o600},
		"encrypted":           {name: "content/" + strings.Repeat("e", 64), flags: 1},
	} {
		cases[name] = append(append([]zipEntry{}, base...), entry)
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Read(writeTestZip(t, entries)); err == nil {
				t.Fatal("accepted malicious/corrupt archive")
			}
		})
	}
}

func TestDecodeJSON(t *testing.T) {
	for _, input := range []string{
		`{"version":1,"version":2}`, `{"version":1,"\u0076ersion":2}`,
		`{"selection":{"matters":[],"matters":[]}}`,
		`{"nodes":[{"origin":{"id":"a","id":"b"}}]}`,
		`{"unknown":1}`, `{"selection":{"unknown":1}}`, `{"nodes":[{"unknown":1}]}`,
		`{"Version":1}`, `{"version":1,"VERSION":2}`, `{"contents":[{"Data":"hidden"}]}`,
		`{} {}`, `{} garbage`, ``, `{"version":`, `{"version":1,}`, `{"nodes":[}]}`,
		"{\"lineage\":\"\xff\"}",
	} {
		t.Run(input, func(t *testing.T) {
			var s Snapshot
			if err := DecodeJSON([]byte(input), &s); err == nil {
				t.Fatal("accepted invalid JSON")
			}
		})
	}
	var dst struct {
		Nested []struct {
			Value int `json:"value"`
		} `json:"nested"`
	}
	if err := DecodeJSON([]byte(`{"nested":[{"value":9007199254740993}]} `), &dst); err != nil {
		t.Fatal(err)
	}
	if dst.Nested[0].Value != 9007199254740993 {
		t.Fatal("lost integer precision")
	}
	var dynamic any
	if err := DecodeJSON([]byte(`{"a":[{"b":{"x":1,"x":2}}]}`), &dynamic); err == nil {
		t.Fatal("accepted duplicate keys inside arbitrary nested values")
	}
	deep := strings.Repeat("[", 10001) + "0" + strings.Repeat("]", 10001)
	if err := DecodeJSON([]byte(deep), &dynamic); err == nil {
		t.Fatal("bypassed encoding/json nesting guard")
	}
}

func TestRejectZIPChecksumCorruption(t *testing.T) {
	s := testSnapshot()
	blob := []byte("unique ZIP checksum test payload")
	addContent(s, 10, testID(1), "brief", 0, blob)
	manifest, _ := json.Marshal(s)
	path := writeTestZip(t, []zipEntry{{name: "manifest.json", data: manifest}, {name: "content/" + s.Contents[0].SHA256, data: blob}})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	offset := bytes.Index(data, blob)
	if offset < 0 {
		t.Fatal("test fixture blob not found")
	}
	data[offset] ^= 0xff
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("accepted corrupted ZIP checksum")
	}
}

func TestEmptySnapshot(t *testing.T) {
	s := &Snapshot{Version: Version, Profile: Profile, Lineage: "empty", Repo: testID(99)}
	path := filepath.Join(t.TempDir(), "empty.zip")
	if err := Write(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidDataDoesNotCreateFile(t *testing.T) {
	s := testSnapshot()
	addContent(s, 10, testID(1), "brief", 0, []byte("blob"))
	for _, bad := range [][]byte{nil, []byte("bad length"), []byte("oops")} {
		s.Contents[0].Data = bad
		path := filepath.Join(t.TempDir(), "no-file.zip")
		if err := Write(path, s); err == nil {
			t.Fatal("accepted corrupt data")
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("invalid write left file: %v", err)
		}
	}
}
