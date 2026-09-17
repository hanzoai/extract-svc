// Package extract turns bytes into plain text plus per-format metadata.
//
// One Extractor interface, one registry, one detect-by-magic-bytes
// path. Every format implementation lives in its own file. Callers
// only ever touch Detect / FromBytes; the registry is package-private
// so we don't grow N callsites tied to specific formats.
package extract

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Format is the canonical short tag we put on the wire ("pdf", "docx",
// "html", "rtf", "txt", "md"). It is NOT the MIME type — keep them
// separate so we never leak Content-Type quirks into the API surface.
type Format string

const (
	FormatPDF      Format = "pdf"
	FormatDOCX     Format = "docx"
	FormatXLSX     Format = "xlsx"
	FormatPPTX     Format = "pptx"
	FormatHTML     Format = "html"
	FormatRTF      Format = "rtf"
	FormatMarkdown Format = "md"
	FormatText     Format = "txt"
	FormatImage    Format = "image" // detected, but unsupported in v0.1
	FormatUnknown  Format = ""
)

// Result is what every extractor returns. Metadata is intentionally
// loose: each format puts what it has (pages, author, title…) — we
// don't pre-declare a struct we'd then have to maintain forever.
type Result struct {
	Text     string         `json:"text"`
	Metadata map[string]any `json:"metadata"`
	Format   Format         `json:"format"`
	Language string         `json:"language"`
}

// Extractor is the only contract a format implementation has to honor.
// Implementations MUST be safe for concurrent use (we run one goroutine
// per request).
type Extractor interface {
	Extract(data []byte) (Result, error)
}

// ErrUnsupportedFormat is returned by FromBytes when Detect produced
// FormatImage (or FormatUnknown). The server maps this to 415.
var ErrUnsupportedFormat = errors.New("unsupported format")

var registry = map[Format]Extractor{}

func register(f Format, e Extractor) { registry[f] = e }

// FromBytes detects format from the leading bytes, dispatches to the
// registered Extractor, and stamps the format + a cheap language
// guess. We deliberately do detection here (not in each Extractor) so
// the dispatch decision is single-sourced.
func FromBytes(data []byte) (Result, error) {
	f := Detect(data)
	if f == FormatImage || f == FormatUnknown {
		// Unknown falls through to text only if it actually looks
		// like text — otherwise we refuse rather than spray garbage.
		if f == FormatUnknown && looksLikeText(data) {
			f = FormatText
		} else {
			return Result{Format: f}, fmt.Errorf("%w: %s", ErrUnsupportedFormat, f)
		}
	}
	ex, ok := registry[f]
	if !ok {
		return Result{Format: f}, fmt.Errorf("%w: no extractor for %s", ErrUnsupportedFormat, f)
	}
	r, err := ex.Extract(data)
	if err != nil {
		return Result{Format: f}, err
	}
	r.Format = f
	if r.Language == "" {
		r.Language = guessLanguage(r.Text)
	}
	if r.Metadata == nil {
		r.Metadata = map[string]any{}
	}
	return r, nil
}

// Detect inspects the leading bytes. Order matters: ZIP-container
// formats (DOCX/XLSX/PPTX) share the PK signature with plain ZIPs,
// so we sniff the inner [Content_Types].xml after the ZIP check.
func Detect(data []byte) Format {
	if len(data) < 4 {
		if looksLikeText(data) {
			return FormatText
		}
		return FormatUnknown
	}
	// PDF — `%PDF-`
	if bytes.HasPrefix(data, []byte("%PDF-")) {
		return FormatPDF
	}
	// RTF — `{\rtf`
	if bytes.HasPrefix(data, []byte("{\\rtf")) {
		return FormatRTF
	}
	// OOXML (ZIP) — `PK\x03\x04` or `PK\x05\x06` (empty) or `PK\x07\x08`.
	if bytes.HasPrefix(data, []byte{0x50, 0x4B, 0x03, 0x04}) ||
		bytes.HasPrefix(data, []byte{0x50, 0x4B, 0x05, 0x06}) ||
		bytes.HasPrefix(data, []byte{0x50, 0x4B, 0x07, 0x08}) {
		return ooxmlKind(data)
	}
	// Image magic — we recognize them only so we can return a clean 415.
	if isImageMagic(data) {
		return FormatImage
	}
	// HTML — leading whitespace then `<`. We tolerate UTF-8 BOM + leading text.
	trim := bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	trim = bytes.TrimLeft(trim, " \t\r\n")
	if len(trim) > 0 && trim[0] == '<' {
		lower := bytes.ToLower(trim[:min(len(trim), 256)])
		if bytes.Contains(lower, []byte("<html")) ||
			bytes.Contains(lower, []byte("<!doctype html")) ||
			bytes.Contains(lower, []byte("<head")) ||
			bytes.Contains(lower, []byte("<body")) {
			return FormatHTML
		}
	}
	// Markdown smells like text with heading / list / fence markers in the
	// first ~2KB. We only upgrade past plain text — never downgrade.
	if looksLikeText(data) {
		if smellsMarkdown(data) {
			return FormatMarkdown
		}
		return FormatText
	}
	// Last-ditch: net/http's sniffer. It's good at HTML & images, less
	// at office docs (which we've already handled).
	switch http.DetectContentType(data) {
	case "text/html; charset=utf-8":
		return FormatHTML
	case "application/pdf":
		return FormatPDF
	}
	return FormatUnknown
}

func isImageMagic(b []byte) bool {
	switch {
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}): // JPEG
		return true
	case bytes.HasPrefix(b, []byte{0x89, 'P', 'N', 'G'}): // PNG
		return true
	case bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a")):
		return true
	case bytes.HasPrefix(b, []byte("RIFF")) && len(b) >= 12 && bytes.Equal(b[8:12], []byte("WEBP")):
		return true
	case bytes.HasPrefix(b, []byte{0x42, 0x4D}): // BMP
		return true
	case bytes.HasPrefix(b, []byte("II*\x00")) || bytes.HasPrefix(b, []byte("MM\x00*")): // TIFF
		return true
	}
	return false
}

// looksLikeText: >95% printable / whitespace bytes in the first 4KB,
// and valid UTF-8 over the whole sample. Cheap and good enough — we
// don't need chardet for v0.1.
func looksLikeText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	sample := b
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	if !utf8.Valid(sample) {
		return false
	}
	printable := 0
	for _, c := range sample {
		if c == '\t' || c == '\n' || c == '\r' || (c >= 0x20 && c < 0x7F) || c >= 0x80 {
			printable++
		}
	}
	return printable*100/len(sample) >= 95
}

func smellsMarkdown(b []byte) bool {
	sample := b
	if len(sample) > 2048 {
		sample = sample[:2048]
	}
	s := string(sample)
	signals := 0
	for _, marker := range []string{"\n# ", "\n## ", "\n### ", "\n```", "\n- ", "\n* ", "\n> ", "](http"} {
		if strings.Contains(s, marker) {
			signals++
		}
	}
	if strings.HasPrefix(s, "# ") || strings.HasPrefix(s, "## ") {
		signals++
	}
	return signals >= 2
}

// ReadAllLimited reads up to max+1 bytes from r. If r holds more than
// max bytes we return an error rather than silently truncating — the
// HTTP layer maps this to 413.
func ReadAllLimited(r io.Reader, max int64) ([]byte, error) {
	buf := &bytes.Buffer{}
	n, err := io.Copy(buf, io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if n > max {
		return nil, fmt.Errorf("payload exceeds %d bytes", max)
	}
	return buf.Bytes(), nil
}

// guessLanguage is a coarse heuristic: count ASCII-letter ratio over
// the first 1KB; if Latin-script-dominant assume "en". v0.1 only.
// Replace with a proper detector (e.g. lingua) when callers actually
// branch on this field.
func guessLanguage(s string) string {
	if s == "" {
		return ""
	}
	sample := s
	if len(sample) > 1024 {
		sample = sample[:1024]
	}
	latin := 0
	letters := 0
	for _, r := range sample {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			latin++
			letters++
		} else if r > 0x7F {
			letters++
		}
	}
	if letters == 0 {
		return ""
	}
	if latin*100/letters >= 80 {
		return "en"
	}
	return "und"
}
