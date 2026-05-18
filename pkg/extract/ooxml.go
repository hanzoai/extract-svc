// OOXML (DOCX / XLSX / PPTX) extraction directly off the ZIP container.
//
// Why not unioffice or sajari/docconv: unioffice carries a heavy AGPL-
// vs-commercial split and pulls a forest of generated XSD code we don't
// need. docconv shells out to external binaries (catdoc/pdftotext) which
// breaks the distroless image. The actual text we care about lives in
// well-known XML files inside the ZIP — extracting it natively is one
// small file, fully static, and trivial to test.
package extract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

type docxExtractor struct{}
type xlsxExtractor struct{}
type pptxExtractor struct{}

func init() {
	register(FormatDOCX, docxExtractor{})
	register(FormatXLSX, xlsxExtractor{})
	register(FormatPPTX, pptxExtractor{})
}

// ooxmlKind peeks inside the ZIP to distinguish DOCX/XLSX/PPTX.
// Falls back to FormatUnknown for a plain ZIP we can't classify.
func ooxmlKind(data []byte) Format {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return FormatUnknown
	}
	for _, f := range zr.File {
		switch {
		case strings.HasPrefix(f.Name, "word/"):
			return FormatDOCX
		case strings.HasPrefix(f.Name, "xl/"):
			return FormatXLSX
		case strings.HasPrefix(f.Name, "ppt/"):
			return FormatPPTX
		}
	}
	return FormatUnknown
}

func (docxExtractor) Extract(data []byte) (Result, error) {
	parts, err := readZipParts(data, func(name string) bool {
		// document.xml plus all header/footer/footnote fragments.
		return name == "word/document.xml" ||
			(strings.HasPrefix(name, "word/header") && strings.HasSuffix(name, ".xml")) ||
			(strings.HasPrefix(name, "word/footer") && strings.HasSuffix(name, ".xml")) ||
			name == "word/footnotes.xml" ||
			name == "word/endnotes.xml"
	})
	if err != nil {
		return Result{}, err
	}
	var out strings.Builder
	for _, body := range parts {
		walkXMLText(body, "w:t", "w:p", &out)
	}
	return Result{
		Text:     normalizeWS(out.String()),
		Metadata: map[string]any{"parts": len(parts)},
	}, nil
}

func (xlsxExtractor) Extract(data []byte) (Result, error) {
	// Shared strings: <sst><si><t>value</t></si>…
	parts, err := readZipParts(data, func(name string) bool {
		return name == "xl/sharedStrings.xml" ||
			strings.HasPrefix(name, "xl/worksheets/sheet")
	})
	if err != nil {
		return Result{}, err
	}
	shared := []string{}
	if ss, ok := parts["xl/sharedStrings.xml"]; ok {
		shared = parseSharedStrings(ss)
	}
	var out strings.Builder
	sheets := 0
	for name, body := range parts {
		if !strings.HasPrefix(name, "xl/worksheets/sheet") {
			continue
		}
		sheets++
		extractSheetText(body, shared, &out)
	}
	return Result{
		Text:     normalizeWS(out.String()),
		Metadata: map[string]any{"sheets": sheets},
	}, nil
}

func (pptxExtractor) Extract(data []byte) (Result, error) {
	parts, err := readZipParts(data, func(name string) bool {
		return strings.HasPrefix(name, "ppt/slides/slide") && strings.HasSuffix(name, ".xml")
	})
	if err != nil {
		return Result{}, err
	}
	var out strings.Builder
	for _, body := range parts {
		// In PPTX text runs are <a:t> nested in <a:p>.
		walkXMLText(body, "a:t", "a:p", &out)
	}
	return Result{
		Text:     normalizeWS(out.String()),
		Metadata: map[string]any{"slides": len(parts)},
	}, nil
}

// readZipParts pulls only the files matching want into memory. Each
// file is bounded by the ZIP local-header uncompressed-size field that
// archive/zip already validates; we add no extra limit here because
// the caller bounds the OUTER upload size.
func readZipParts(data []byte, want func(string) bool) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("zip open: %w", err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		if !want(f.Name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("zip read %s: %w", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("zip read %s: %w", f.Name, err)
		}
		out[f.Name] = b
	}
	return out, nil
}

// walkXMLText emits inner-text of textTag elements, followed by a
// newline when paraTag ends. Streaming so we don't materialize the
// full DOM. Empty textTag is the trigger because xml.Decoder reports
// element open/close cleanly without needing namespace mapping.
func walkXMLText(body []byte, textTag, paraTag string, out *strings.Builder) {
	d := xml.NewDecoder(bytes.NewReader(body))
	inText := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Best-effort: if Word's XML has an oddball entity, stop
			// at the failure point rather than discarding what we have.
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if localName(t.Name) == textTag || matchTag(t.Name, textTag) {
				inText = true
			}
		case xml.EndElement:
			if localName(t.Name) == textTag || matchTag(t.Name, textTag) {
				inText = false
			}
			if localName(t.Name) == paraTag || matchTag(t.Name, paraTag) {
				out.WriteByte('\n')
			}
		case xml.CharData:
			if inText {
				out.Write(t)
			}
		}
	}
}

// matchTag handles namespace-prefixed comparisons like "w:t" against
// xml.Name{Space: ".../wordprocessingml/2006/main", Local: "t"}.
func matchTag(n xml.Name, tag string) bool {
	if i := strings.Index(tag, ":"); i > 0 {
		return n.Local == tag[i+1:]
	}
	return n.Local == tag
}

func localName(n xml.Name) string { return n.Local }

func parseSharedStrings(body []byte) []string {
	var out []string
	d := xml.NewDecoder(bytes.NewReader(body))
	inSI := false
	inT := false
	var cur strings.Builder
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				cur.Reset()
			case "t":
				if inSI {
					inT = true
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				inSI = false
				out = append(out, cur.String())
			case "t":
				inT = false
			}
		case xml.CharData:
			if inT {
				cur.Write(t)
			}
		}
	}
	return out
}

func extractSheetText(body []byte, shared []string, out *strings.Builder) {
	d := xml.NewDecoder(bytes.NewReader(body))
	inCell := false
	cellType := ""
	inVal := false
	inInlineT := false
	var cur strings.Builder
	flush := func() {
		s := cur.String()
		cur.Reset()
		if s == "" {
			return
		}
		if cellType == "s" {
			// shared-string index
			var idx int
			fmt.Sscanf(s, "%d", &idx)
			if idx >= 0 && idx < len(shared) {
				out.WriteString(shared[idx])
				out.WriteByte('\t')
			}
			return
		}
		out.WriteString(s)
		out.WriteByte('\t')
	}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "c":
				inCell = true
				cellType = ""
				for _, a := range t.Attr {
					if a.Name.Local == "t" {
						cellType = a.Value
					}
				}
			case "v":
				if inCell {
					inVal = true
				}
			case "t":
				if inCell && cellType == "inlineStr" {
					inInlineT = true
				}
			case "row":
				// noop, handled by row end
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inVal = false
				flush()
			case "t":
				if inInlineT {
					flush()
					inInlineT = false
				}
			case "c":
				inCell = false
			case "row":
				out.WriteByte('\n')
			}
		case xml.CharData:
			if inVal || inInlineT {
				cur.Write(t)
			}
		}
	}
}

func normalizeWS(s string) string {
	// Collapse runs of blank lines, trim trailing space per line.
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, ln := range lines {
		ln = strings.TrimRight(ln, " \t\r")
		if ln == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, ln)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
