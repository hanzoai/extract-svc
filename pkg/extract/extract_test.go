package extract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

func TestDetect(t *testing.T) {
	ensureFixtures(t)
	cases := []struct {
		file string
		want Format
	}{
		{"sample.txt", FormatText},
		{"sample.md", FormatMarkdown},
		{"sample.html", FormatHTML},
		{"sample.rtf", FormatRTF},
		{"sample.pdf", FormatPDF},
		{"sample.docx", FormatDOCX},
		{"sample.xlsx", FormatXLSX},
		{"sample.pptx", FormatPPTX},
	}
	for _, tc := range cases {
		got := Detect(read(t, tc.file))
		if got != tc.want {
			t.Errorf("Detect(%s) = %q, want %q", tc.file, got, tc.want)
		}
	}
}

func TestText(t *testing.T) {
	r, err := FromBytes(read(t, "sample.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Format != FormatText {
		t.Fatalf("format=%s", r.Format)
	}
	if !strings.Contains(r.Text, "plain text sample") {
		t.Fatalf("text=%q", r.Text)
	}
}

func TestMarkdown(t *testing.T) {
	r, err := FromBytes(read(t, "sample.md"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Format != FormatMarkdown {
		t.Fatalf("format=%s", r.Format)
	}
	if !strings.Contains(r.Text, "Sample Markdown") {
		t.Fatalf("text=%q", r.Text)
	}
}

func TestHTML(t *testing.T) {
	r, err := FromBytes(read(t, "sample.html"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Format != FormatHTML {
		t.Fatalf("format=%s", r.Format)
	}
	if !strings.Contains(r.Text, "Ada Lovelace") {
		t.Fatalf("text=%q", r.Text)
	}
	if strings.Contains(r.Text, "alert(") {
		t.Fatalf("script content leaked: %q", r.Text)
	}
	if strings.Contains(r.Text, "hidden") {
		t.Fatalf("script content leaked: %q", r.Text)
	}
	if !strings.Contains(r.Text, "Bernoulli") {
		t.Fatalf("list item missing: %q", r.Text)
	}
	if r.Metadata["title"] != "Sample Resume" {
		t.Fatalf("title=%v", r.Metadata["title"])
	}
}

func TestRTF(t *testing.T) {
	ensureFixtures(t)
	r, err := FromBytes(read(t, "sample.rtf"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Format != FormatRTF {
		t.Fatalf("format=%s", r.Format)
	}
	if !strings.Contains(r.Text, "Hello") || !strings.Contains(r.Text, "world") {
		t.Fatalf("text=%q", r.Text)
	}
	// stylesheet/info group must be dropped
	if strings.Contains(r.Text, "Helvetica") {
		t.Fatalf("font table leaked: %q", r.Text)
	}
	if strings.Contains(r.Text, "Ada") {
		t.Fatalf("info group leaked: %q", r.Text)
	}
}

func TestPDF(t *testing.T) {
	ensureFixtures(t)
	r, err := FromBytes(read(t, "sample.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Format != FormatPDF {
		t.Fatalf("format=%s", r.Format)
	}
	if !strings.Contains(r.Text, "Hanzo extract pdf fixture") {
		t.Fatalf("text=%q", r.Text)
	}
}

func TestDOCX(t *testing.T) {
	ensureFixtures(t)
	r, err := FromBytes(read(t, "sample.docx"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Format != FormatDOCX {
		t.Fatalf("format=%s", r.Format)
	}
	if !strings.Contains(r.Text, "Hello DOCX world") {
		t.Fatalf("text=%q", r.Text)
	}
	if !strings.Contains(r.Text, "Second paragraph") {
		t.Fatalf("text=%q", r.Text)
	}
}

func TestXLSX(t *testing.T) {
	ensureFixtures(t)
	r, err := FromBytes(read(t, "sample.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Format != FormatXLSX {
		t.Fatalf("format=%s", r.Format)
	}
	if !strings.Contains(r.Text, "Alpha") || !strings.Contains(r.Text, "Beta") {
		t.Fatalf("text=%q", r.Text)
	}
	if !strings.Contains(r.Text, "42") {
		t.Fatalf("text=%q", r.Text)
	}
}

func TestPPTX(t *testing.T) {
	ensureFixtures(t)
	r, err := FromBytes(read(t, "sample.pptx"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Format != FormatPPTX {
		t.Fatalf("format=%s", r.Format)
	}
	if !strings.Contains(r.Text, "Slide Title") {
		t.Fatalf("text=%q", r.Text)
	}
	if !strings.Contains(r.Text, "Slide body text") {
		t.Fatalf("text=%q", r.Text)
	}
}

func TestImageReturnsUnsupported(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 13, 'I', 'H', 'D', 'R'}
	_, err := FromBytes(png)
	if err == nil {
		t.Fatal("expected unsupported")
	}
}

func TestReadAllLimited(t *testing.T) {
	_, err := ReadAllLimited(strings.NewReader("abc"), 10)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ReadAllLimited(strings.NewReader("0123456789"), 5)
	if err == nil {
		t.Fatal("expected size error")
	}
}
