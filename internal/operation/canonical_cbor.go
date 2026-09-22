package operation

import (
	"encoding/binary"
	"fmt"
	"sort"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// canonicalMap and canonicalArray are the closed value model used by the
// command identity encoder. Keeping this model smaller than Go's value model
// makes unsupported values (notably floats and raw bytes) impossible to encode
// accidentally.
type (
	canonicalMap   map[string]any
	canonicalArray []any
)

func marshalCanonical(value any) ([]byte, error) {
	var encoded []byte
	if err := appendCanonical(&encoded, value); err != nil {
		return nil, err
	}
	return encoded, nil
}

func appendCanonical(dst *[]byte, value any) error {
	switch value := value.(type) {
	case nil:
		*dst = append(*dst, 0xf6)
	case bool:
		if value {
			*dst = append(*dst, 0xf5)
		} else {
			*dst = append(*dst, 0xf4)
		}
	case string:
		if err := validateCanonicalText(value); err != nil {
			return err
		}
		appendCBORHead(dst, 3, uint64(len(value)))
		*dst = append(*dst, value...)
	case uint64:
		appendCBORHead(dst, 0, value)
	case int64:
		if value >= 0 {
			appendCBORHead(dst, 0, uint64(value))
		} else {
			appendCBORHead(dst, 1, uint64(-(value + 1)))
		}
	case canonicalArray:
		appendCBORHead(dst, 4, uint64(len(value)))
		for i, element := range value {
			if err := appendCanonical(dst, element); err != nil {
				return fmt.Errorf("array element %d: %w", i, err)
			}
		}
	case canonicalMap:
		return appendCanonicalMap(dst, value)
	default:
		return fmt.Errorf("canonical CBOR does not support %T", value)
	}
	return nil
}

func appendCanonicalMap(dst *[]byte, value canonicalMap) error {
	type entry struct {
		key     string
		encoded []byte
	}
	entries := make([]entry, 0, len(value))
	for key := range value {
		if err := validateCanonicalText(key); err != nil {
			return fmt.Errorf("map key %q: %w", key, err)
		}
		encoded, err := marshalCanonical(key)
		if err != nil {
			return err
		}
		entries = append(entries, entry{key: key, encoded: encoded})
	}
	sort.Slice(entries, func(i, j int) bool {
		if len(entries[i].encoded) != len(entries[j].encoded) {
			return len(entries[i].encoded) < len(entries[j].encoded)
		}
		return string(entries[i].encoded) < string(entries[j].encoded)
	})

	appendCBORHead(dst, 5, uint64(len(entries)))
	for _, entry := range entries {
		*dst = append(*dst, entry.encoded...)
		if err := appendCanonical(dst, value[entry.key]); err != nil {
			return fmt.Errorf("map value %q: %w", entry.key, err)
		}
	}
	return nil
}

func appendCBORHead(dst *[]byte, major byte, value uint64) {
	prefix := major << 5
	switch {
	case value < 24:
		*dst = append(*dst, prefix|byte(value))
	case value <= 0xff:
		*dst = append(*dst, prefix|24, byte(value))
	case value <= 0xffff:
		*dst = append(*dst, prefix|25, 0, 0)
		binary.BigEndian.PutUint16((*dst)[len(*dst)-2:], uint16(value))
	case value <= 0xffffffff:
		*dst = append(*dst, prefix|26, 0, 0, 0, 0)
		binary.BigEndian.PutUint32((*dst)[len(*dst)-4:], uint32(value))
	default:
		*dst = append(*dst, prefix|27, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64((*dst)[len(*dst)-8:], value)
	}
}

func validateCanonicalText(value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("text is not valid UTF-8")
	}
	if !norm.NFC.IsNormalString(value) {
		return fmt.Errorf("text is not Unicode NFC")
	}
	return nil
}
