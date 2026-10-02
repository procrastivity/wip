package wipdwire

// CommandSubmitV2Feature is selected independently on each command transport
// hop. It is also the schema of the separate, closed v2 submission map.
const CommandSubmitV2Feature = "wipd.command-submit/2"

// CommandSubmitV2 carries an opaque detached authorization. Nil means omitted
// proof on replay; a supplied proof is a nonempty byte string of at most 1 MiB.
// Neither this proof nor the submission version is part of command identity.
// CommandSubmit remains the unchanged closed v1 map.
type CommandSubmitV2 struct {
	Schema           string  `cbor:"schema"`
	CanonicalCommand []byte  `cbor:"canonical_command"`
	RequestHash      string  `cbor:"request_hash"`
	Deadline         *string `cbor:"deadline"`
	DetachedProof    []byte  `cbor:"detached_proof"`
}

// DecodeCommandSubmit decodes either closed envelope without interpreting the
// command or authorization. Callers must enforce session selection, canonical
// command identity, and operation-specific authorization before admission.
func DecodeCommandSubmit(payload []byte) (CommandSubmitV2, bool, error) {
	var out CommandSubmitV2
	var fields map[string]any
	if err := decoder.Unmarshal(payload, &fields); err != nil {
		return out, false, ErrInvalidRecord
	}
	version2 := fields["schema"] == CommandSubmitV2Feature
	keys := []string{"schema", "canonical_command", "request_hash", "deadline"}
	if version2 {
		keys = append(keys, "detached_proof")
	} else if fields["schema"] != "wipd.command-submit/1" {
		return out, false, ErrInvalidRecord
	}
	if !ExactMapKeys(fields, keys...) || DecodeCanonical(payload, &out, keys...) != nil ||
		len(out.CanonicalCommand) == 0 || out.RequestHash == "" {
		return CommandSubmitV2{}, false, ErrInvalidRecord
	}
	if _, ok := fields["canonical_command"].([]byte); !ok {
		return CommandSubmitV2{}, false, ErrInvalidRecord
	}
	if fields["deadline"] != nil {
		if _, ok := fields["deadline"].(string); !ok {
			return CommandSubmitV2{}, false, ErrInvalidRecord
		}
	}
	if version2 && fields["detached_proof"] != nil {
		proof, ok := fields["detached_proof"].([]byte)
		if !ok || len(proof) == 0 || len(proof) > 1<<20 {
			return CommandSubmitV2{}, false, ErrInvalidRecord
		}
	}
	return out, version2, nil
}
