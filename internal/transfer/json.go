package transfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// DecodeJSON rejects duplicate object keys (including nested objects), unknown
// struct fields, case aliases of field names, invalid UTF-8, unpaired UTF-16
// surrogate escapes, and trailing JSON.
// The destination should use typed structs rather than arbitrary maps when the
// caller needs a closed schema. As with json.Unmarshal, decoding may partially
// update the destination on failure; callers must discard it on error.
func DecodeJSON(data []byte, dst any) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("transfer: invalid UTF-8 JSON")
	}
	// Apply encoding/json's syntax and nesting checks before recursive token
	// inspection. Token alone does not apply the parser's nesting-depth guard.
	if !json.Valid(data) {
		return fmt.Errorf("transfer: invalid JSON syntax or nesting")
	}
	if err := checkSurrogates(data); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := jsonValue(d)
	if err != nil {
		return fmt.Errorf("transfer: JSON: %w", err)
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("transfer: trailing JSON: %v", err)
	}
	if err := checkFields(v, reflect.TypeOf(dst)); err != nil {
		return err
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}

// checkSurrogates runs only on syntactically valid JSON, so every backslash is
// in a string and each Unicode escape has exactly four hexadecimal digits.
// Skip other escapes as a unit so literal backslash-u text is not interpreted.
func checkSurrogates(data []byte) error {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if data[i] != 'u' {
			continue
		}
		value, _ := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		i += 4
		switch {
		case value >= 0xd800 && value <= 0xdbff:
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return fmt.Errorf("transfer: unpaired UTF-16 high surrogate")
			}
			low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return fmt.Errorf("transfer: unpaired UTF-16 high surrogate")
			}
			i += 6
		case value >= 0xdc00 && value <= 0xdfff:
			return fmt.Errorf("transfer: unpaired UTF-16 low surrogate")
		}
	}
	return nil
}

func jsonValue(d *json.Decoder) (any, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t {
	case json.Delim('{'):
		m := make(map[string]any)
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, err
			}
			key, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("non-string key")
			}
			if _, ok := m[key]; ok {
				return nil, fmt.Errorf("duplicate key %q", key)
			}
			v, err := jsonValue(d)
			if err != nil {
				return nil, err
			}
			m[key] = v
		}
		_, err := d.Token()
		return m, err
	case json.Delim('['):
		var a []any
		for d.More() {
			v, err := jsonValue(d)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		_, err := d.Token()
		return a, err
	default:
		if _, delim := t.(json.Delim); delim {
			return nil, fmt.Errorf("unexpected delimiter %v", t)
		}
		return t, nil
	}
}

func checkFields(v any, typ reflect.Type) error {
	if typ == nil {
		return nil // encoding/json will report an invalid destination
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch value := v.(type) {
	case map[string]any:
		if typ.Kind() == reflect.Struct {
			fields := make(map[string]reflect.Type)
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				if !f.IsExported() {
					continue
				}
				name := strings.Split(f.Tag.Get("json"), ",")[0]
				if name == "-" {
					continue
				}
				if name == "" {
					name = f.Name
				}
				fields[name] = f.Type
			}
			for key, item := range value {
				ft, ok := fields[key]
				if !ok {
					return fmt.Errorf("transfer: unknown JSON field %q", key)
				}
				if err := checkFields(item, ft); err != nil {
					return err
				}
			}
		} else if typ.Kind() == reflect.Map {
			for _, item := range value {
				if err := checkFields(item, typ.Elem()); err != nil {
					return err
				}
			}
		}
	case []any:
		if typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
			for _, item := range value {
				if err := checkFields(item, typ.Elem()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// CanonicalContents copies content values, normalizes create-once Order to zero
// and findings Order to zero-based relative ranks per owner, then sorts by ID.
// It expects validated metadata; findings orders must be distinct per owner.
// Input values and Data bytes are not modified; Data slices remain shared.
// Nil input produces an empty non-nil slice for canonical JSON metadata.
func CanonicalContents(contents []Content) []Content {
	c := append([]Content{}, contents...)
	sort.Slice(c, func(i, j int) bool {
		if c[i].Owner != c[j].Owner {
			return c[i].Owner < c[j].Owner
		}
		return c[i].Order < c[j].Order
	})
	ranks := make(map[string]int)
	for i := range c {
		if c[i].Kind == "findings" {
			c[i].Order = ranks[c[i].Owner]
			ranks[c[i].Owner]++
		} else {
			c[i].Order = 0
		}
	}
	sort.Slice(c, func(i, j int) bool { return c[i].ID < c[j].ID })
	return c
}

// canonicalMetadata copies and sorts set-like collections. Relative finding
// order and node SortKey remain semantic; input slice ordering does not.
// Normalizing empty slices makes JSON null and [] semantically identical.
func canonicalMetadata(s *Snapshot) Snapshot {
	c := *s
	c.Selection.Matters = append([]string{}, s.Selection.Matters...)
	c.Selection.Backlog = append([]string{}, s.Selection.Backlog...)
	c.Nodes = append([]Node{}, s.Nodes...)
	c.Backlog = append([]Entry{}, s.Backlog...)
	c.Edges = append([]Edge{}, s.Edges...)
	c.Contents = CanonicalContents(s.Contents)
	c.Exclusions = append([]string{}, s.Exclusions...)
	sort.Strings(c.Selection.Matters)
	sort.Strings(c.Selection.Backlog)
	sort.Slice(c.Nodes, func(i, j int) bool { return c.Nodes[i].ID < c.Nodes[j].ID })
	sort.Slice(c.Backlog, func(i, j int) bool { return c.Backlog[i].ID < c.Backlog[j].ID })
	sort.Slice(c.Edges, func(i, j int) bool { return c.Edges[i].ID < c.Edges[j].ID })
	sort.Strings(c.Exclusions)
	return c
}

// Digest hashes canonical typed JSON metadata, including content hashes but not
// Data. It validates metadata only, so metadata-only projectors can use it.
func Digest(s *Snapshot) (string, error) {
	if err := ValidateMetadata(s); err != nil {
		return "", err
	}
	data, err := json.Marshal(canonicalMetadata(s))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
