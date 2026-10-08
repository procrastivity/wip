package wipdwire

import "testing"

func TestNamedBatchReadResponseUsesClosedM2QueryIdentity(t *testing.T) {
	response := NamedBatchReadResponse{
		Schema:     "wipd.read-response/1",
		Query:      NamedBatchReadIdentity{Name: "batch.read", Version: 1},
		FilterHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	payload, err := EncodeCanonical(response)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := DecodeCanonicalMap(payload,
		"schema", "query", "filter_hash", "snapshot", "provenance", "items", "next_page_token", "complete")
	if err != nil {
		t.Fatal(err)
	}
	query, ok := fields["query"].(map[string]any)
	if !ok || !ExactMapKeys(query, "name", "version") || query["name"] != "batch.read" || query["version"] != uint64(1) {
		t.Fatalf("response query identity = %#v; want exactly name/version", query)
	}
	if fields["filter_hash"] != response.FilterHash {
		t.Fatalf("response filter_hash = %#v; want %q", fields["filter_hash"], response.FilterHash)
	}
	decoded := NamedBatchReadResponse{}
	if err = DecodeNamedBatchReadResponse(payload, &decoded); err != nil || decoded.Query != response.Query {
		t.Fatalf("decode closed response query: response=%+v err=%v", decoded, err)
	}
}

func TestDecodeNamedBatchReadResponseRejectsFilterInsideQueryIdentity(t *testing.T) {
	payload, err := EncodeCanonical(map[string]any{
		"schema": "wipd.read-response/1",
		"query": map[string]any{
			"name": "batch.read", "version": uint64(1), "filter": map[string]any{"batch_id": "01M4F1XT4R3E00000000000001"},
		},
		"filter_hash": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"snapshot":    map[string]any{}, "provenance": map[string]any{}, "items": []any{}, "next_page_token": nil, "complete": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = DecodeNamedBatchReadResponse(payload, &NamedBatchReadResponse{}); err == nil {
		t.Fatal("accepted a filter nested in the closed response query identity")
	}
}
