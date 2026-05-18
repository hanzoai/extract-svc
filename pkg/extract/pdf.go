// PDF text extraction via github.com/ledongthuc/pdf — MIT-licensed,
// pure Go, no LGPL transitive deps. We use GetPlainText (visible-page
// text walker) rather than Reader.Rows so we get a single concatenated
// stream that matches what rekoni emitted.
package extract

import (
	"bytes"
	"fmt"
	"io"

	"github.com/ledongthuc/pdf"
)

type pdfExtractor struct{}

func init() { register(FormatPDF, pdfExtractor{}) }

func (pdfExtractor) Extract(data []byte) (Result, error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Result{}, fmt.Errorf("pdf parse: %w", err)
	}
	tr, err := r.GetPlainText()
	if err != nil {
		return Result{}, fmt.Errorf("pdf text: %w", err)
	}
	out, err := io.ReadAll(tr)
	if err != nil {
		return Result{}, fmt.Errorf("pdf read: %w", err)
	}
	return Result{
		Text: string(out),
		Metadata: map[string]any{
			"pages": r.NumPage(),
		},
	}, nil
}
