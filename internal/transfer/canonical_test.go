package transfer

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeJSONSurrogates(t *testing.T) {
	for _, value := range []string{
		`\ud800`, `\udbff`, `\udc00`, `\udfff`,
		`\ud800x`, `\ud800\u0041`, `\ud800\ud800`,
		`\udc00\ud800`, `\ud800\\udc00`, `\ud800\udc00\udfff`,
	} {
		t.Run(value, func(t *testing.T) {
			for _, input := range []string{
				`{"lineage":"` + value + `"}`,
				`{"nodes":[{"origin":{"lineage":"` + value + `"}}]}`,
			} {
				var s Snapshot
				if err := DecodeJSON([]byte(input), &s); err == nil || !strings.Contains(err.Error(), "surrogate") {
					t.Fatalf("accepted surrogate in %s: %v", input, err)
				}
			}
			var dynamic any
			if err := DecodeJSON([]byte(`{"`+value+`":0}`), &dynamic); err == nil {
				t.Fatal("accepted surrogate in object key")
			}
		})
	}
	for input, want := range map[string]string{
		`\ud800\udc00`: "𐀀", `\uDBFF\uDFFF`: "\U0010ffff",
		`\uD83D\uDE00`: "😀", `\ufffd`: "�", "�": "�",
		`\\ud800`: `\ud800`, `\\udfff`: `\udfff`,
		`\"\ud800\udc00\\`: "\"𐀀\\",
	} {
		t.Run(input, func(t *testing.T) {
			var s Snapshot
			if err := DecodeJSON([]byte(`{"lineage":"`+input+`"}`), &s); err != nil || s.Lineage != want {
				t.Fatalf("decoded %q, want %q: %v", s.Lineage, want, err)
			}
		})
	}
}

func TestReadSurrogateLineages(t *testing.T) {
	manifest, err := json.Marshal(testSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"lineage":"local lineage"`, `"lineage":"opaque/../lineage"`} {
		for _, value := range []string{`\ud800`, `\udfff`, `\ud83d\ude00`, `\ufffd`, "�"} {
			t.Run(field+value, func(t *testing.T) {
				data := bytes.Replace(manifest, []byte(field), []byte(`"lineage":"`+value+`"`), 1)
				_, err := Read(writeTestZip(t, []zipEntry{{name: "manifest.json", data: data}}))
				bad := value == `\ud800` || value == `\udfff`
				if bad && (err == nil || !strings.Contains(err.Error(), "surrogate")) || !bad && err != nil {
					t.Fatalf("Read error = %v; invalid surrogate = %v", err, bad)
				}
			})
		}
	}
}

func TestCanonicalContentOrders(t *testing.T) {
	s := testSnapshot()
	maxOrder := int(^uint(0) >> 1)
	addContent(s, 14, testID(4), "findings", maxOrder, []byte("last"))
	addContent(s, 12, testID(1), "brief", maxOrder, []byte("brief"))
	addContent(s, 10, testID(1), "findings", 40, []byte("second"))
	addContent(s, 13, testID(4), "findings", 7, []byte("first"))
	addContent(s, 11, testID(1), "findings", 20, []byte("first"))
	addContent(s, 15, testID(1), "body", 99, []byte("body"))
	addContent(s, 16, testID(1), "workplan", 50, []byte("workplan"))
	before := append([]Content{}, s.Contents...)
	beforeJSON, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(s); err != nil {
		t.Fatalf("nonnegative gapped/max orders must remain accepted: %v", err)
	}
	canonical := CanonicalContents(s.Contents)
	orders := []int{1, 0, 0, 0, 1, 0, 0}
	for i, c := range canonical {
		if c.ID != testID(10+i) || c.Order != orders[i] {
			t.Fatalf("canonical content %d = %s/%d, want %s/%d", i, c.ID, c.Order, testID(10+i), orders[i])
		}
	}
	if !reflect.DeepEqual(canonical, CanonicalContents(canonical)) {
		t.Fatal("canonicalization is not idempotent")
	}
	dense := *s
	dense.Contents = canonical
	wantDigest, err := Digest(&dense)
	if err != nil {
		t.Fatal(err)
	}
	gotDigest, err := Digest(s)
	if err != nil || gotDigest != wantDigest {
		t.Fatalf("gapped/dense digests differ: %s / %s: %v", gotDigest, wantDigest, err)
	}
	path := filepath.Join(t.TempDir(), "canonical.zip")
	if err := Write(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Contents, canonical) {
		t.Fatalf("roundtrip contents not canonical: %#v", got.Contents)
	}
	if digest, err := Digest(got); err != nil || digest != wantDigest {
		t.Fatalf("roundtrip digest = %s: %v", digest, err)
	}
	afterJSON, err := json.Marshal(s)
	if err != nil || !bytes.Equal(beforeJSON, afterJSON) || !reflect.DeepEqual(before, s.Contents) {
		t.Fatal("canonicalization, Digest, or Write mutated input")
	}
	dense.Contents[0].Order, dense.Contents[1].Order = dense.Contents[1].Order, dense.Contents[0].Order
	if digest, err := Digest(&dense); err != nil || digest == wantDigest {
		t.Fatalf("changed finding order did not change digest: %s: %v", digest, err)
	}
	if empty := CanonicalContents(nil); empty == nil || len(empty) != 0 {
		t.Fatal("nil contents must canonicalize to non-nil empty slice")
	}
}
