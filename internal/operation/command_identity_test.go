package operation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

const (
	testCommandID     = "01K5V8JQFM6Q3Q0XZ6F1Z7A2BC"
	testDomainID      = "01K5V8K1Q5VX6Y0J8C9W3M4N5P"
	testEnvironmentID = "01K5V8K8A4J2N7R9T0V3X6Y8ZB"
	testRepoID        = "01K5V8KGD3F6H9J2M4N7Q0R5TW"
)

func TestCanonicalCommandGoldenVector(t *testing.T) {
	command := canonicalIdentityCommand()
	first, err := command.CanonicalBytes()
	if err != nil {
		t.Fatalf("CanonicalBytes() error = %v", err)
	}
	second, err := command.CanonicalBytes()
	if err != nil {
		t.Fatalf("second CanonicalBytes() error = %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("the same command produced different canonical bytes")
	}

	const wantHex = "ad656163746f726c726f6c653a6275696c64657265626c6f62738065636c61696df665696e707574a2657469746c6577436166c3a92070726f746f636f6c206964656e74697479717265717565737465645f6c6f6361746f727170726f746f636f6c2d6964656e7469747966736368656d616e776970642e636f6d6d616e642f3167636f6e74657874a3677265706f5f6964781a30314b3556384b474433463648394a324d344e3751305235545768636c6f6e655f6964f66b776f726b747265655f6964f66861637465645f6174781e323032362d30392d32325431373a33313a34322e3132333435363738395a69617574686f72697479a269646f6d61696e5f6964781a30314b3556384b31513556583659304a38433957334d344e35506e65787065637465645f65706f636807696f7065726174696f6ea2646e616d656d6d61747465722e6372656174656776657273696f6e016a636f6d6d616e645f6964781a30314b3556384a51464d3651335130585a3646315a37413242436b656e7669726f6e6d656e74a2626964781a30314b3556384b3841344a324e37523954305633583659385a426873657175656e6365182a74636175736174696f6e5f636f6d6d616e645f6964f676636f7272656c6174696f6e5f636f6d6d616e645f6964781a30314b3556384a51464d3651335130585a3646315a3741324243"
	if got := hex.EncodeToString(first); got != wantHex {
		t.Fatalf("canonical CBOR = %s, want %s", got, wantHex)
	}

	const wantHash = "sha256:ad15faaa76992d045529ab28b7bd9ddd63799fa851641c131b881e141e6a9503"
	gotHash, err := command.RequestHash()
	if err != nil {
		t.Fatalf("RequestHash() error = %v", err)
	}
	if gotHash != wantHash {
		t.Fatalf("request_hash = %s, want %s", gotHash, wantHash)
	}
	independent := sha256.Sum256(append([]byte("wipd/request-hash/v1\x00"), first...))
	if want := "sha256:" + hex.EncodeToString(independent[:]); gotHash != want {
		t.Fatalf("request_hash = %s, independent computation = %s", gotHash, want)
	}
	if err := VerifyRequestHash(command, gotHash); err != nil {
		t.Fatalf("VerifyRequestHash() error = %v", err)
	}
}

func TestDecodeCanonicalCommandPreservesAsymmetricTypedRequest(t *testing.T) {
	want := canonicalIdentityCommand()
	encoded, err := want.CanonicalBytes()
	if err != nil {
		t.Fatalf("CanonicalBytes() error = %v", err)
	}
	got, err := DecodeCanonicalCommand(encoded)
	if err != nil {
		t.Fatalf("DecodeCanonicalCommand() error = %v", err)
	}
	if got.ID != want.ID || got.AuthorityDomainID != want.AuthorityDomainID ||
		got.ExpectedAuthorityEpoch != want.ExpectedAuthorityEpoch ||
		got.EnvironmentID != want.EnvironmentID || got.EnvironmentSequence != want.EnvironmentSequence {
		t.Fatalf("decoded command identity = %+v, want %+v", got, want)
	}
	if got.Request.Operation != want.Request.Operation || got.Request.Actor != want.Request.Actor ||
		got.Request.Context != want.Request.Context {
		t.Fatalf("decoded semantic envelope = %+v, want %+v", got.Request, want.Request)
	}
	gotInput, ok := got.Request.Input.(MatterCreateInput)
	if !ok {
		t.Fatalf("decoded input type = %T, want MatterCreateInput", got.Request.Input)
	}
	wantInput := MatterCreateInput{Title: "Café protocol identity", Locator: "protocol-identity"}
	if gotInput != wantInput {
		t.Fatalf("decoded input = %+v, want asymmetric input %+v", gotInput, wantInput)
	}
	gotHash, err := got.RequestHash()
	if err != nil {
		t.Fatalf("decoded RequestHash() error = %v", err)
	}
	if gotHash != "sha256:ad15faaa76992d045529ab28b7bd9ddd63799fa851641c131b881e141e6a9503" {
		t.Fatalf("decoded request hash = %s, want the published M2 golden", gotHash)
	}
}

func TestDecodeCanonicalCommandRejectsOpenOrAlternateEncodings(t *testing.T) {
	base := canonicalIdentityCommand()
	value, err := base.canonicalValue()
	if err != nil {
		t.Fatalf("canonicalValue() error = %v", err)
	}
	unknown := make(canonicalMap, len(value)+1)
	for key, item := range value {
		unknown[key] = item
	}
	unknown["unknown"] = true
	missing := make(canonicalMap, len(value)-1)
	for key, item := range value {
		if key != "actor" {
			missing[key] = item
		}
	}
	unknownBytes, err := marshalCanonical(unknown)
	if err != nil {
		t.Fatal(err)
	}
	missingBytes, err := marshalCanonical(missing)
	if err != nil {
		t.Fatal(err)
	}

	canonical, err := base.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	// The value 7 for expected_epoch is changed from its preferred one-byte
	// form to the valid but non-shortest two-byte form.
	nonShortest := bytes.Replace(canonical,
		[]byte{0x6e, 'e', 'x', 'p', 'e', 'c', 't', 'e', 'd', '_', 'e', 'p', 'o', 'c', 'h', 0x07},
		[]byte{0x6e, 'e', 'x', 'p', 'e', 'c', 't', 'e', 'd', '_', 'e', 'p', 'o', 'c', 'h', 0x18, 0x07}, 1)
	if bytes.Equal(nonShortest, canonical) {
		t.Fatal("test mutation did not find the expected_epoch golden encoding")
	}

	for name, data := range map[string][]byte{
		"unknown field":    unknownBytes,
		"missing field":    missingBytes,
		"non-shortest int": nonShortest,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeCanonicalCommand(data); err == nil {
				t.Fatal("DecodeCanonicalCommand() accepted a noncanonical command")
			}
		})
	}
}

func TestCanonicalCBORUsesDeterministicKeyAndScalarEncoding(t *testing.T) {
	left := canonicalMap{"aa": uint64(24), "b": nil, "truth": true, "minus": int64(-25)}
	right := canonicalMap{"minus": int64(-25), "truth": true, "b": nil, "aa": uint64(24)}
	want := "a46162f66261611818656d696e75733818657472757468f5"
	for name, value := range map[string]canonicalMap{"left": left, "right": right} {
		encoded, err := marshalCanonical(value)
		if err != nil {
			t.Fatalf("%s: marshalCanonical() error = %v", name, err)
		}
		if got := hex.EncodeToString(encoded); got != want {
			t.Fatalf("%s encoding = %s, want %s", name, got, want)
		}
	}
}

func TestEveryCommandIdentityFieldChangesRequestHash(t *testing.T) {
	base := canonicalIdentityCommand()
	baseHash, err := base.RequestHash()
	if err != nil {
		t.Fatalf("base RequestHash() error = %v", err)
	}
	otherID := "01K5V8M2C4D6F8G0H2J4K6M8NP"
	tests := []struct {
		name string
		edit func(*Command)
	}{
		{"command ID", func(c *Command) { c.ID, c.CorrelationCommandID = otherID, otherID }},
		{"domain", func(c *Command) { c.AuthorityDomainID = otherID }},
		{"authority epoch", func(c *Command) { c.ExpectedAuthorityEpoch++ }},
		{"Environment", func(c *Command) { c.EnvironmentID = otherID }},
		{"Environment sequence", func(c *Command) { c.EnvironmentSequence++ }},
		{"acted time", func(c *Command) { c.ActedAt = "2026-09-22T17:31:43.123456789Z" }},
		{"actor", func(c *Command) { c.Request.Actor = "human" }},
		{"causation", func(c *Command) { c.CausationCommandID = otherID }},
		{"correlation", func(c *Command) { c.CausationCommandID, c.CorrelationCommandID = otherID, testDomainID }},
		{"operation version", func(c *Command) { c.Request.Operation.Version = 2 }},
		{"Repo context", func(c *Command) { c.Request.Context.Repo = otherID }},
		{"Clone context", func(c *Command) { c.Request.Context.Clone = otherID }},
		{"Worktree context", func(c *Command) { c.Request.Context.Worktree = otherID }},
		{"input title", func(c *Command) {
			input := c.Request.Input.(MatterCreateInput)
			input.Title += "!"
			c.Request.Input = input
		}},
		{"input locator", func(c *Command) {
			input := c.Request.Input.(MatterCreateInput)
			input.Locator += "-2"
			c.Request.Input = input
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.edit(&changed)
			got, err := changed.RequestHash()
			if test.name == "operation version" {
				if err == nil || !strings.Contains(err.Error(), "no canonical identity schema") {
					t.Fatalf("unsupported version error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("changed RequestHash() error = %v", err)
			}
			if got == baseHash {
				t.Fatalf("changed field retained request_hash %s", got)
			}
		})
	}
}

func TestCanonicalCommandNormalizesEmptyNamedCollections(t *testing.T) {
	empty := canonicalIdentityCommand()
	nilBlobs := canonicalIdentityCommand()
	nilBlobs.Request.Blobs = nil

	emptyHash, err := empty.RequestHash()
	if err != nil {
		t.Fatalf("empty blobs RequestHash() error = %v", err)
	}
	nilHash, err := nilBlobs.RequestHash()
	if err != nil {
		t.Fatalf("nil blobs RequestHash() error = %v", err)
	}
	if emptyHash != nilHash {
		t.Fatalf("explicit and Go-nil empty blob sets differ: %s != %s", emptyHash, nilHash)
	}
}

func TestCanonicalCommandRejectsNonCanonicalOrAmbiguousValues(t *testing.T) {
	base := canonicalIdentityCommand()
	tests := []struct {
		name string
		edit func(*Command)
		want string
	}{
		{"lowercase identity", func(c *Command) { c.ID = strings.ToLower(c.ID) }, "canonical ULID"},
		{"zero authority epoch", func(c *Command) { c.ExpectedAuthorityEpoch = 0 }, "authority epoch"},
		{"zero Environment sequence", func(c *Command) { c.EnvironmentSequence = 0 }, "sequence"},
		{"offset timestamp", func(c *Command) { c.ActedAt = "2026-09-22T12:31:42.123456789-05:00" }, "canonical UTC"},
		{"self causation", func(c *Command) { c.CausationCommandID = c.ID }, "cause itself"},
		{"wrong origin correlation", func(c *Command) { c.CorrelationCommandID = testDomainID }, "correlate to itself"},
		{"decomposed Unicode", func(c *Command) {
			input := c.Request.Input.(MatterCreateInput)
			input.Title = "Cafe\u0301"
			c.Request.Input = input
		}, "Unicode NFC"},
		{"invalid UTF-8", func(c *Command) {
			input := c.Request.Input.(MatterCreateInput)
			input.Title = string([]byte{0xff})
			c.Request.Input = input
		}, "valid UTF-8"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := base
			test.edit(&command)
			_, err := command.CanonicalBytes()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CanonicalBytes() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestAuthorityRecomputationRejectsMalformedAndChangedHashes(t *testing.T) {
	command := canonicalIdentityCommand()
	hash, err := command.RequestHash()
	if err != nil {
		t.Fatalf("RequestHash() error = %v", err)
	}
	for _, asserted := range []string{
		"",
		strings.ToUpper(hash),
		"sha256:" + strings.Repeat("0", 64),
	} {
		if err := VerifyRequestHash(command, asserted); err == nil {
			t.Fatalf("VerifyRequestHash(%q) succeeded", asserted)
		}
	}

	changed := command
	input := changed.Request.Input.(MatterCreateInput)
	input.Title = "Changed intent under the same command ID"
	changed.Request.Input = input
	if err := VerifyRequestHash(changed, hash); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("changed same-ID command verification error = %v, want mismatch", err)
	}
}

func TestBlobDigestAndReferenceRules(t *testing.T) {
	if got, want := BlobDigest([]byte("abc")), "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"; got != want {
		t.Fatalf("BlobDigest(abc) = %s, want %s", got, want)
	}

	digest := BlobDigest([]byte("body"))
	first, err := canonicalBlobs([]BlobInput{
		{Name: "zeta", Digest: digest, Size: 4},
		{Name: "alpha", Digest: digest, Size: 4},
	})
	if err != nil {
		t.Fatalf("canonicalBlobs() error = %v", err)
	}
	second, err := canonicalBlobs([]BlobInput{
		{Name: "alpha", Digest: digest, Size: 4},
		{Name: "zeta", Digest: digest, Size: 4},
	})
	if err != nil {
		t.Fatalf("reordered canonicalBlobs() error = %v", err)
	}
	left, _ := marshalCanonical(first)
	right, _ := marshalCanonical(second)
	if string(left) != string(right) {
		t.Fatal("named blob set depends on caller order")
	}

	bad := [][]BlobInput{
		{{Name: "body", Digest: strings.ToUpper(digest), Size: 4}},
		{{Name: "body", Digest: digest, Size: -1}},
		{{Name: "body", Digest: digest, Size: 4}, {Name: "body", Digest: digest, Size: 4}},
	}
	for _, blobs := range bad {
		if _, err := canonicalBlobs(blobs); err == nil {
			t.Fatalf("canonicalBlobs(%+v) succeeded", blobs)
		}
	}
}

func canonicalIdentityCommand() Command {
	return Command{
		ID:                     testCommandID,
		AuthorityDomainID:      testDomainID,
		ExpectedAuthorityEpoch: 7,
		EnvironmentID:          testEnvironmentID,
		EnvironmentSequence:    42,
		ActedAt:                "2026-09-22T17:31:42.123456789Z",
		CorrelationCommandID:   testCommandID,
		Request: Request{
			Operation: MatterCreateV1.Metadata().Operation,
			Actor:     "role:builder",
			Context:   Context{Repo: testRepoID},
			Input: MatterCreateInput{
				Title:   "Café protocol identity",
				Locator: "protocol-identity",
			},
			Blobs: []BlobInput{},
		},
	}
}
