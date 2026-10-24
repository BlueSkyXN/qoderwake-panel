package jsoncompat

import (
	"strings"
	"unicode/utf8"
)

// DecodeUTF8 preserves Python decode(errors="replace") behavior for damaged log bytes.
func DecodeUTF8(raw []byte) string {
	if utf8.Valid(raw) {
		return string(raw)
	}
	var b strings.Builder
	for len(raw) > 0 {
		r, n := utf8.DecodeRune(raw)
		if r != utf8.RuneError || n != 1 {
			b.WriteRune(r)
			raw = raw[n:]
			continue
		}
		count := 1
		expected := 0
		first := raw[0]
		switch {
		case first >= 0xc2 && first <= 0xdf:
			expected = 2
		case first >= 0xe0 && first <= 0xef:
			expected = 3
		case first >= 0xf0 && first <= 0xf4:
			expected = 4
		}
		for count < expected && count < len(raw) {
			next := raw[count]
			if next < 0x80 || next > 0xbf {
				break
			}
			if count == 1 && ((first == 0xe0 && next < 0xa0) || (first == 0xed && next >= 0xa0) || (first == 0xf0 && next < 0x90) || (first == 0xf4 && next > 0x8f)) {
				break
			}
			count++
		}
		b.WriteRune(utf8.RuneError)
		raw = raw[count:]
	}
	return b.String()
}
