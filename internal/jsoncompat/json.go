// Package jsoncompat preserves the legacy Python JSON bytes used in revision digests.
package jsoncompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf16"
)

// Marshal uses sorted keys, UTF-8 and the spaced separators used by json.dumps.
func Marshal(value any) []byte {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(value) != nil {
		return nil
	}
	raw := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	out := []byte{}
	quoted, escaped := false, false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if quoted && !escaped && c == '\\' && i+5 < len(raw) {
			escape := string(raw[i : i+6])
			if escape == `\u2028` || escape == `\u2029` {
				if escape == `\u2028` {
					out = append(out, []byte("\u2028")...)
				} else {
					out = append(out, []byte("\u2029")...)
				}
				i += 5
				continue
			}
		}
		out = append(out, c)
		if quoted {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
		} else if c == '"' {
			quoted = true
		} else if c == ',' || c == ':' {
			out = append(out, ' ')
		}
	}
	return out
}
func Compact(value any, ascii bool) []byte {
	var buffer bytes.Buffer
	if json.Compact(&buffer, Marshal(value)) != nil {
		return nil
	}
	if !ascii {
		return buffer.Bytes()
	}
	out := bytes.Buffer{}
	for _, r := range buffer.String() {
		if r < 127 {
			out.WriteRune(r)
		} else if r <= 0xffff {
			fmt.Fprintf(&out, `\u%04x`, r)
		} else {
			a, b := utf16.EncodeRune(r)
			fmt.Fprintf(&out, `\u%04x\u%04x`, a, b)
		}
	}
	return out.Bytes()
}
