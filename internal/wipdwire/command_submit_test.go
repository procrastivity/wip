package wipdwire

import (
	"bytes"
	"testing"
)

func TestCommandSubmitVersionsKeepProofDetachedAndV1Closed(t *testing.T) {
	command := []byte{0xa0}
	proof := []byte{0x00, 0xff, 0x80, 0x01}
	v1, err := EncodeCanonical(CommandSubmit{Schema: "wipd.command-submit/1", CanonicalCommand: command, RequestHash: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	got, version2, err := DecodeCommandSubmit(v1)
	if err != nil || version2 || !bytes.Equal(got.CanonicalCommand, command) || got.DetachedProof != nil {
		t.Fatalf("v1 decode: %+v, v2=%t, err=%v", got, version2, err)
	}
	v2, err := EncodeCanonical(CommandSubmitV2{Schema: CommandSubmitV2Feature, CanonicalCommand: command, RequestHash: "hash", DetachedProof: proof})
	if err != nil {
		t.Fatal(err)
	}
	got, version2, err = DecodeCommandSubmit(v2)
	if err != nil || !version2 || !bytes.Equal(got.CanonicalCommand, command) || !bytes.Equal(got.DetachedProof, proof) {
		t.Fatalf("v2 decode: %+v, v2=%t, err=%v", got, version2, err)
	}
	fields, err := DecodeCanonicalMap(v1, "schema", "canonical_command", "request_hash", "deadline")
	if err != nil {
		t.Fatal(err)
	}
	fields["detached_proof"] = proof
	bad, _ := EncodeCanonical(fields)
	if _, _, err = DecodeCommandSubmit(bad); err == nil {
		t.Fatal("v1 accepted an extension field")
	}
	for _, malformed := range []any{[]byte{}, "proof", uint64(1), bytes.Repeat([]byte{1}, (1<<20)+1)} {
		fields["schema"] = CommandSubmitV2Feature
		fields["detached_proof"] = malformed
		bad, _ = EncodeCanonical(fields)
		if _, _, err = DecodeCommandSubmit(bad); err == nil {
			t.Fatalf("v2 accepted malformed proof %T", malformed)
		}
	}
	fields["detached_proof"] = nil
	bad, _ = EncodeCanonical(fields)
	if got, version2, err = DecodeCommandSubmit(bad); err != nil || !version2 || got.DetachedProof != nil {
		t.Fatalf("v2 omitted replay proof: %+v %t %v", got, version2, err)
	}
	fields["detached_proof"] = bytes.Repeat([]byte{0x81}, 1<<20)
	boundary, err := EncodeCanonical(fields)
	if err != nil {
		t.Fatal(err)
	}
	if got, version2, err = DecodeCommandSubmit(boundary); err != nil || !version2 || len(got.DetachedProof) != 1<<20 {
		t.Fatalf("v2 exact authorization size boundary: length=%d v2=%t err=%v", len(got.DetachedProof), version2, err)
	}
	if _, err = EncodeFrame(Frame{RequestID: "01KZ7XHAQT1S46NYPN1PW1DX70", Kind: "command.submit", Payload: boundary}); err == nil {
		t.Fatal("authorization size limit bypassed the smaller frame limit")
	}
}
