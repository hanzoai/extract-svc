// Plain text and Markdown pass-through. We don't strip Markdown
// syntax — downstream indexers handle that more accurately, and the
// rekoni contract was "return what you got, just decoded".
package extract

import "strings"

type textExtractor struct{}
type markdownExtractor struct{}

func init() {
	register(FormatText, textExtractor{})
	register(FormatMarkdown, markdownExtractor{})
}

func (textExtractor) Extract(data []byte) (Result, error) {
	return Result{Text: strings.TrimSpace(string(data))}, nil
}

func (markdownExtractor) Extract(data []byte) (Result, error) {
	return Result{
		Text:     strings.TrimSpace(string(data)),
		Metadata: map[string]any{"syntax": "markdown"},
	}, nil
}
