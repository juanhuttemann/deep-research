package tools

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"golang.org/x/net/html"
)

// DirectClient reads public HTML without a separate extraction service. PDFs
// and JavaScript-only pages fall back to the search snippet through SearchTools.
type DirectClient struct{ HTTPClient *http.Client }

func NewDirectClient(timeout time.Duration) *DirectClient {
	if timeout <= 0 || timeout > 20*time.Second {
		timeout = 20 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Resolve and dial the checked address ourselves: checking the hostname
	// alone would let a redirect or DNS rebinding reach a private service.
	transport.Proxy = nil
	transport.DialContext = dialPublicPage
	return &DirectClient{HTTPClient: &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many page redirects")
		}
		return checkScrapeTarget(req.URL.String())
	}}}
}

func dialPublicPage(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var last error
	for _, ip := range ips {
		target := net.JoinHostPort(ip.IP.String(), port)
		if err := checkScrapeTarget("http://" + target); err != nil {
			last = err
			continue
		}
		conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, target)
		if err == nil {
			return conn, nil
		}
		last = err
	}
	if last == nil {
		last = fmt.Errorf("no public address for %s", host)
	}
	return nil, last
}

func (c *DirectClient) Name() string { return ScraperDirect }

func (c *DirectClient) ScrapeURL(ctx context.Context, target string) (*ScrapedContent, error) {
	if err := checkScrapeTarget(target); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "deep-research-agent/1.0")
	req.Header.Set("Accept", "text/html,text/plain;q=0.9")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &httpError{Code: resp.StatusCode}
	}
	kind := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.Contains(kind, "text/html") && !strings.Contains(kind, "text/plain") {
		return nil, fmt.Errorf("direct extraction does not support %q", kind)
	}
	const bodyLimit = 2 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit+1))
	if err != nil {
		return nil, err
	}
	if len(body) > bodyLimit {
		return nil, fmt.Errorf("page exceeds extraction limit")
	}
	page := &ScrapedContent{URL: target, Content: string(body)}
	if strings.Contains(kind, "text/html") {
		page, err = extractHTML(string(body))
		if err != nil {
			return nil, err
		}
		page.URL = target
	}
	if strings.TrimSpace(page.Content) == "" {
		return nil, errEmptyScrape
	}
	page.Content = truncateUTF8Bare(page.Content, hostedContentLimit)
	return page, nil
}

func extractHTML(body string) (*ScrapedContent, error) {
	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	page := &ScrapedContent{}
	if n := firstElement(root, "title"); n != nil {
		page.Title = strings.Join(strings.Fields(nodeText(n)), " ")
	}
	// A challenge page is not article evidence even when the server says 200.
	for _, s := range []string{"just a moment", "access denied", "verify you are human"} {
		if strings.Contains(strings.ToLower(page.Title), s) {
			return nil, fmt.Errorf("%w: %s", errPageChallenge, page.Title)
		}
	}
	content := root
	for _, tag := range []string{"main", "article", "body"} {
		if n := firstElement(root, tag); n != nil {
			content = n
			break
		}
	}
	prunePage(content)
	md, err := pageConverter.ConvertNode(content)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	if page.Title != "" {
		fmt.Fprintf(&b, "# %s\n\n", page.Title)
	}
	b.Write(md)
	page.Content = cleanPageText(b.String())
	return page, nil
}

// pageConverter turns a page's content into Markdown. Tables matter most:
// the excerpts score a Markdown table by its header and answering rows (see
// ExcerptFor), and the hand-written renderer this replaced wrote cells with no
// header row or separator, so no page it read had a table to score. Escaping
// is off because the page text is evidence quoted back against itself: a
// "C\+\+" the page never wrote would fail the quote check.
var pageConverter = converter.NewConverter(
	converter.WithEscapeMode(converter.EscapeModeDisabled),
	converter.WithPlugins(
		base.NewBasePlugin(),
		commonmark.NewCommonmarkPlugin(),
		table.NewTablePlugin(table.WithCellPaddingBehavior(table.CellPaddingBehaviorMinimal)),
		pagePlugin{},
	),
)

// pagePlugin keeps a link's words and drops its target, and drops images. A
// source reaches a prompt as 1500 characters of excerpt, and a documentation
// page's link targets would spend most of them on URLs no claim cites.
type pagePlugin struct{}

func (pagePlugin) Name() string { return "deep-research-page" }

func (pagePlugin) Init(conv *converter.Converter) error {
	conv.Register.RendererFor("a", converter.TagTypeInline, func(ctx converter.Context, w converter.Writer, n *html.Node) converter.RenderStatus {
		ctx.RenderChildNodes(ctx, w, n)
		return converter.RenderSuccess
	}, converter.PriorityEarly)
	conv.Register.TagType("img", converter.TagTypeRemove, converter.PriorityEarly)
	return nil
}

// prunePage removes what is not the page's text (scripts, navigation, hidden
// elements) and spells out struck text. A struck sentence is usually a
// correction the page made, and as Markdown's ~~ it read as current text to a
// model that skims the markers.
func prunePage(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		switch {
		case c.Type != html.ElementNode:
		case skipPageNode(c):
			n.RemoveChild(c)
		case c.Data == "del" || c.Data == "s" || c.Data == "strike":
			text := " [Superseded text: " + strings.Join(strings.Fields(nodeText(c)), " ") + "] "
			n.InsertBefore(&html.Node{Type: html.TextNode, Data: text}, c)
			n.RemoveChild(c)
		default:
			prunePage(c)
		}
		c = next
	}
}

func firstElement(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := firstElement(c, tag); found != nil {
			return found
		}
	}
	return nil
}

func nodeText(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(nodeText(c))
	}
	return b.String()
}

func skipPageNode(n *html.Node) bool {
	switch n.Data {
	case "script", "style", "nav", "footer", "header", "form", "aside", "noscript", "svg", "template":
		return true
	}
	for _, a := range n.Attr {
		if a.Key == "hidden" || a.Key == "aria-hidden" && a.Val == "true" {
			return true
		}
	}
	return false
}

// cleanPageText collapses the blank-line runs and the spacing inside each
// line that page markup leaves, keeping a line's indentation, which nests a
// Markdown list, and a fenced code block as the page wrote it.
func cleanPageText(s string) string {
	var lines []string
	fenced := false
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			fenced = !fenced
		} else if fenced {
			lines = append(lines, strings.TrimRight(line, " \t"))
			continue
		}
		if trimmed == "" {
			if len(lines) > 0 && lines[len(lines)-1] != "" {
				lines = append(lines, "")
			}
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
		lines = append(lines, indent+strings.Join(strings.Fields(trimmed), " "))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
