package canonical

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// JCS returns a canonical-JSON serialization of v suitable for hashing.
// Object keys are sorted, no insignificant whitespace is emitted, and
// strings round-trip through encoding/json. The output is stable across
// processes and Go versions, which is the property signatures and
// attestation envelopes depend on.
//
// Scope: this implementation covers the JSON shapes qvr actually hashes
// (strings, integers, floats stored as float64, bools, nil, slices,
// objects). Full RFC 8785 number-formatting (ES6 Number.prototype.toString
// edge cases) is not implemented — qvr's artifacts don't carry arbitrary
// floats. Extend here if a future artifact requires it.
func JCS(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, generic); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case json.Number:
		if i, err := t.Int64(); err == nil {
			buf.WriteString(strconv.FormatInt(i, 10))
			return nil
		}
		f, err := t.Float64()
		if err != nil {
			return fmt.Errorf("parse number %q: %w", t.String(), err)
		}
		buf.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
	case string:
		s, err := json.Marshal(t)
		if err != nil {
			return err
		}
		buf.Write(s)
	case []any:
		buf.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			ks, err := json.Marshal(k)
			if err != nil {
				return err
			}
			buf.Write(ks)
			buf.WriteByte(':')
			if err := writeCanonical(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("canonical: unsupported type %T", v)
	}
	return nil
}
