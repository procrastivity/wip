package wipdremote

import (
	"bytes"
	"testing"

	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestDecodeAttachTerminalPinsBothExactReceiptBytes(t *testing.T) {
	receipt, signed := []byte{0xa1, 0x61, 0x78, 0x01}, []byte{0xa1, 0x61, 0x79, 0x02}
	payload, err := wipdwire.EncodeCanonical(wipdwire.SignedCommandTerminal{
		Schema: "wipd.signed-command-terminal/1", CommandID: "01KZ7XHAQT1S46NYPN1PW1DX3E",
		RequestHash: "sha256:" + string(bytes.Repeat([]byte("a"), 64)), Receipt: receipt, SignedReceipt: signed,
	})
	if err != nil {
		t.Fatal(err)
	}
	gotReceipt, gotSigned, err := decodeAttachTerminal(payload, "01KZ7XHAQT1S46NYPN1PW1DX3E", "sha256:"+string(bytes.Repeat([]byte("a"), 64)))
	if err != nil || !bytes.Equal(gotReceipt, receipt) || !bytes.Equal(gotSigned, signed) {
		t.Fatalf("decoded attach evidence = %x / %x, %v", gotReceipt, gotSigned, err)
	}
	if _, _, err = decodeAttachTerminal(payload, "01KZ7XHAQT1S46NYPN1PW1DX3D", "sha256:"+string(bytes.Repeat([]byte("a"), 64))); err == nil {
		t.Fatal("different command ID reused signed terminal")
	}
	if _, _, err = decodeAttachTerminal(payload, "01KZ7XHAQT1S46NYPN1PW1DX3E", "sha256:"+string(bytes.Repeat([]byte("b"), 64))); err == nil {
		t.Fatal("different request hash reused signed terminal")
	}
}
