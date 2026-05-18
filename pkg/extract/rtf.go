// RTF text extraction. A real RTF parser is overkill for plaintext —
// the spec is huge but the subset we need is small:
//
//   - drop control words (\foo123, \foo, with optional numeric arg)
//   - drop groups of font/style/info tables ({\fonttbl…}, {\stylesheet…},
//     {\info…}, {\*\…}) entirely — they are never visible text
//   - decode \'XX hex byte escapes (Windows-1252 by default)
//   - decode \uNNNN? unicode escapes (the trailing replacement char is
//     ignored, per spec)
//   - turn \par / \line / \page into newlines
//   - keep regular text bytes
//
// This is ~120 lines and handles the resumes / business docs we see in
// practice. Heavy RTF (tables, embedded objects) loses formatting,
// which is fine — we only emit plain text anyway.
package extract

import (
	"strings"
	"unicode/utf8"
)

type rtfExtractor struct{}

func init() { register(FormatRTF, rtfExtractor{}) }

// destinations that contain no visible text — entire group is skipped.
var rtfSkipDestinations = map[string]struct{}{
	"fonttbl": {}, "filetbl": {}, "colortbl": {}, "stylesheet": {},
	"listtable": {}, "listoverridetable": {}, "rsidtbl": {},
	"info": {}, "header": {}, "footer": {}, "pict": {}, "object": {},
	"datafield": {}, "themedata": {}, "colorschememapping": {},
	"latentstyles": {}, "datastore": {},
}

func (rtfExtractor) Extract(data []byte) (Result, error) {
	src := string(data)
	var out strings.Builder
	depth := 0
	// skipUntilDepth tracks group depth at which we entered an
	// ignored destination; -1 means "not skipping".
	skipUntilDepth := -1

	i := 0
	for i < len(src) {
		c := src[i]
		switch c {
		case '{':
			depth++
			i++
		case '}':
			depth--
			if skipUntilDepth >= 0 && depth < skipUntilDepth {
				skipUntilDepth = -1
			}
			i++
		case '\\':
			// control word, control symbol, or hex / unicode escape
			if i+1 >= len(src) {
				i++
				continue
			}
			next := src[i+1]
			switch {
			case next == '\'':
				// hex byte escape \'HH
				if i+3 < len(src) && skipUntilDepth < 0 {
					b := fromHex(src[i+2], src[i+3])
					// Windows-1252 codepoints 0x80..0x9F are NOT
					// Latin-1; map the common ones explicitly.
					if r := cp1252ToRune(b); r >= 0 {
						out.WriteRune(r)
					}
				}
				i += 4
			case next == 'u':
				// \uNNNN unicode escape; the optional trailing
				// replacement char is one byte we skip.
				j := i + 2
				neg := false
				if j < len(src) && src[j] == '-' {
					neg = true
					j++
				}
				k := j
				for k < len(src) && src[k] >= '0' && src[k] <= '9' {
					k++
				}
				if k > j {
					n := atoi(src[j:k])
					if neg {
						n = -n
					}
					// Spec: negative values represent the codepoint
					// (n + 65536) — they only show up because old RTF
					// writers signed-overflowed the 16-bit value.
					if n < 0 {
						n += 65536
					}
					if skipUntilDepth < 0 {
						out.WriteRune(rune(n))
					}
					i = k
					// skip the single replacement byte
					if i < len(src) && src[i] != '\\' && src[i] != '{' && src[i] != '}' {
						i++
					}
					continue
				}
				i = k
			case isAlpha(next):
				// control word: \name optional-int optional-space
				j := i + 1
				for j < len(src) && isAlpha(src[j]) {
					j++
				}
				name := src[i+1 : j]
				// optional numeric arg
				if j < len(src) && (src[j] == '-' || (src[j] >= '0' && src[j] <= '9')) {
					if src[j] == '-' {
						j++
					}
					for j < len(src) && src[j] >= '0' && src[j] <= '9' {
						j++
					}
				}
				// optional single delimiter space — consumed, not emitted
				if j < len(src) && src[j] == ' ' {
					j++
				}
				// Whole-group skip for ignored destinations.
				if _, drop := rtfSkipDestinations[name]; drop && skipUntilDepth < 0 {
					skipUntilDepth = depth
				}
				if skipUntilDepth < 0 {
					switch name {
					case "par", "line", "page", "sect":
						out.WriteByte('\n')
					case "tab":
						out.WriteByte('\t')
					}
				}
				i = j
			default:
				// control symbol like \*, \\, \{, \}, \~, \-
				switch next {
				case '\\', '{', '}':
					if skipUntilDepth < 0 {
						out.WriteByte(next)
					}
					i += 2
				case '*':
					// \* marks an ignorable destination — the next group
					// can be safely dropped if we don't recognize it.
					skipUntilDepth = depth
					i += 2
				case '~':
					if skipUntilDepth < 0 {
						out.WriteRune(' ')
					}
					i += 2
				default:
					i += 2
				}
			}
		case '\r', '\n':
			// literal newlines in RTF source are NOT paragraph breaks
			i++
		default:
			if skipUntilDepth < 0 {
				out.WriteByte(c)
			}
			i++
		}
	}
	// Trim non-printable noise that survived (control chars sometimes
	// slip in via partial escapes); collapse blank lines.
	text := stripNonText(out.String())
	return Result{Text: normalizeWS(text)}, nil
}

func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func fromHex(a, b byte) byte {
	hi := hexVal(a)
	lo := hexVal(b)
	if hi < 0 || lo < 0 {
		return 0
	}
	return byte(hi)<<4 | byte(lo)
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			break
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// Windows-1252 specific high bytes (0x80-0x9F). Returns -1 for the
// few unassigned slots.
func cp1252ToRune(b byte) rune {
	if b < 0x80 {
		return rune(b)
	}
	if b >= 0xA0 {
		return rune(b) // Latin-1 supplement passes through
	}
	tab := map[byte]rune{
		0x80: '€', 0x82: '‚', 0x83: 'ƒ', 0x84: '„',
		0x85: '…', 0x86: '†', 0x87: '‡', 0x88: 'ˆ',
		0x89: '‰', 0x8A: 'Š', 0x8B: '‹', 0x8C: 'Œ',
		0x8E: 'Ž',
		0x91: '‘', 0x92: '’', 0x93: '“', 0x94: '”',
		0x95: '•', 0x96: '–', 0x97: '—', 0x98: '˜',
		0x99: '™', 0x9A: 'š', 0x9B: '›', 0x9C: 'œ',
		0x9E: 'ž', 0x9F: 'Ÿ',
	}
	if r, ok := tab[b]; ok {
		return r
	}
	return -1
}

func stripNonText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == utf8.RuneError && size == 1 {
			continue
		}
		if r == '\t' || r == '\n' || r >= 0x20 {
			b.WriteRune(r)
		}
	}
	return b.String()
}
