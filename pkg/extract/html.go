// HTML text extraction. We walk the tokenized document with
// golang.org/x/net/html and concatenate visible text — skipping
// <script>, <style>, <template> and other non-rendered elements.
package extract

import (
	"bytes"
	"fmt"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type htmlExtractor struct{}

func init() { register(FormatHTML, htmlExtractor{}) }

// blockElements emit a newline boundary after their content so the
// extracted plaintext keeps a sane paragraph shape.
var blockElements = map[atom.Atom]struct{}{
	atom.P: {}, atom.Br: {}, atom.Div: {}, atom.Li: {}, atom.Tr: {},
	atom.H1: {}, atom.H2: {}, atom.H3: {}, atom.H4: {}, atom.H5: {}, atom.H6: {},
	atom.Article: {}, atom.Section: {}, atom.Header: {}, atom.Footer: {},
	atom.Nav: {}, atom.Pre: {}, atom.Blockquote: {},
}

var skipElements = map[atom.Atom]struct{}{
	atom.Script: {}, atom.Style: {}, atom.Template: {},
	atom.Noscript: {}, atom.Iframe: {},
}

func (htmlExtractor) Extract(data []byte) (Result, error) {
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return Result{}, fmt.Errorf("html parse: %w", err)
	}
	var (
		out   strings.Builder
		title string
	)
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n == nil {
			return
		}
		if n.Type == html.ElementNode {
			if _, skip := skipElements[n.DataAtom]; skip {
				return
			}
			if n.DataAtom == atom.Title && title == "" {
				title = strings.TrimSpace(textOf(n))
				return
			}
		}
		if n.Type == html.TextNode {
			out.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode {
			if _, block := blockElements[n.DataAtom]; block {
				out.WriteByte('\n')
			}
		}
	}
	walk(doc)
	return Result{
		Text:     normalizeWS(out.String()),
		Metadata: map[string]any{"title": title},
	}, nil
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}
