package ui

import (
	"html"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// markdownText renders Markdown as plain text for the minimal PDF writer.
// Parsing preserves code and link destinations while removing formatting
// syntax; global character replacement would corrupt both.
func markdownText(markdown string) string {
	source := []byte(markdown)
	doc := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(text.NewReader(source))
	var out strings.Builder
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			finishMarkdownNode(&out, n)
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Text:
			value := n.Value(source)
			if !n.IsRaw() {
				value = util.UnescapePunctuations(value)
				out.WriteString(html.UnescapeString(string(value)))
			} else {
				out.Write(value)
			}
			if n.SoftLineBreak() || n.HardLineBreak() {
				out.WriteByte('\n')
			}
		case *ast.String:
			out.Write(n.Value)
		case *ast.AutoLink:
			out.Write(n.URL(source))
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			for i := 0; i < n.Lines().Len(); i++ {
				line := n.Lines().At(i)
				out.Write(line.Value(source))
			}
		case *ast.ListItem:
			out.WriteString("- ")
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(out.String())
}

func finishMarkdownNode(out *strings.Builder, n ast.Node) {
	switch n := n.(type) {
	case *ast.Link:
		out.WriteString(" (" + string(n.Destination) + ")")
	case *extast.TableCell:
		if n.NextSibling() != nil {
			out.WriteString(" — ")
		}
	case *extast.TableHeader, *extast.TableRow, *extast.Table:
		out.WriteByte('\n')
	case *ast.Heading, *ast.Paragraph, *ast.TextBlock, *ast.ListItem,
		*ast.FencedCodeBlock, *ast.CodeBlock, *ast.ThematicBreak:
		out.WriteString("\n\n")
	}
}
