package extract

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// We build binary fixtures (DOCX/XLSX/PPTX/PDF) at test time rather
// than committing opaque binary blobs. Each generator emits the
// minimum valid container with one piece of known text so the test
// can assert round-tripping. Generated lazily once per `go test` run.
var (
	genOnce sync.Once
	genErr  error
)

func ensureFixtures(t *testing.T) {
	t.Helper()
	genOnce.Do(func() {
		dir := filepath.Join("testdata")
		genErr = writeIfMissing(filepath.Join(dir, "sample.docx"), buildDOCX())
		if genErr != nil {
			return
		}
		genErr = writeIfMissing(filepath.Join(dir, "sample.xlsx"), buildXLSX())
		if genErr != nil {
			return
		}
		genErr = writeIfMissing(filepath.Join(dir, "sample.pptx"), buildPPTX())
		if genErr != nil {
			return
		}
		genErr = writeIfMissing(filepath.Join(dir, "sample.pdf"), buildPDF())
	})
	if genErr != nil {
		t.Fatalf("fixture gen: %v", genErr)
	}
}

func writeIfMissing(path string, data []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, data, 0o644)
}

func buildZip(parts map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range parts {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	return buf.Bytes()
}

func buildDOCX() []byte {
	doc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>Hello DOCX world.</w:t></w:r></w:p>
    <w:p><w:r><w:t>Second paragraph.</w:t></w:r></w:p>
  </w:body>
</w:document>`
	rels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`
	ct := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="xml" ContentType="application/xml"/>
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`
	return buildZip(map[string]string{
		"[Content_Types].xml": ct,
		"_rels/.rels":         rels,
		"word/document.xml":   doc,
	})
}

func buildXLSX() []byte {
	ss := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="2" uniqueCount="2">
  <si><t>Alpha</t></si>
  <si><t>Beta</t></si>
</sst>`
	sheet := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <sheetData>
    <row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>
    <row r="2"><c r="A2"><v>42</v></c></row>
  </sheetData>
</worksheet>`
	wb := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <sheets><sheet name="Sheet1" sheetId="1" r:id="rId1" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"/></sheets>
</workbook>`
	ct := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="xml" ContentType="application/xml"/>
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
</Types>`
	rels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`
	return buildZip(map[string]string{
		"[Content_Types].xml":     ct,
		"_rels/.rels":             rels,
		"xl/workbook.xml":         wb,
		"xl/worksheets/sheet1.xml": sheet,
		"xl/sharedStrings.xml":    ss,
	})
}

func buildPPTX() []byte {
	slide := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
       xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
  <p:cSld><p:spTree>
    <p:sp><p:txBody>
      <a:p><a:r><a:t>Slide Title</a:t></a:r></a:p>
      <a:p><a:r><a:t>Slide body text.</a:t></a:r></a:p>
    </p:txBody></p:sp>
  </p:spTree></p:cSld>
</p:sld>`
	ct := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="xml" ContentType="application/xml"/>
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Override PartName="/ppt/slides/slide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>
</Types>`
	rels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/>
</Relationships>`
	return buildZip(map[string]string{
		"[Content_Types].xml":   ct,
		"_rels/.rels":           rels,
		"ppt/slides/slide1.xml": slide,
	})
}

// buildPDF emits a hand-rolled minimal PDF whose page text content is
// "Hanzo extract pdf fixture." The byte offsets in the xref table are
// fixed at byte-counted positions in the document we assemble below.
func buildPDF() []byte {
	var buf bytes.Buffer
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		"<< /Length 56 >>\nstream\nBT\n/F1 12 Tf\n72 720 Td\n(Hanzo extract pdf fixture.) Tj\nET\nendstream",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, body := range objs {
		offsets[i] = buf.Len()
		buf.WriteString(formatObj(i+1, body))
	}
	xrefStart := buf.Len()
	buf.WriteString("xref\n")
	buf.WriteString("0 ")
	buf.WriteString(itoa(len(objs) + 1))
	buf.WriteString("\n0000000000 65535 f \n")
	for _, off := range offsets {
		buf.WriteString(padZero(off, 10))
		buf.WriteString(" 00000 n \n")
	}
	buf.WriteString("trailer\n<< /Size ")
	buf.WriteString(itoa(len(objs) + 1))
	buf.WriteString(" /Root 1 0 R >>\nstartxref\n")
	buf.WriteString(itoa(xrefStart))
	buf.WriteString("\n%%EOF\n")
	return buf.Bytes()
}

func formatObj(id int, body string) string {
	return itoa(id) + " 0 obj\n" + body + "\nendobj\n"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	s := string(b[i:])
	if neg {
		s = "-" + s
	}
	return s
}

func padZero(n, width int) string {
	s := itoa(n)
	for len(s) < width {
		s = "0" + s
	}
	return s
}
