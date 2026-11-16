package tools

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

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
			return nil, fmt.Errorf("page challenge: %s", page.Title)
		}
	}
	content := root
	for _, tag := range []string{"main", "article", "body"} {
		if n := firstElement(root, tag); n != nil {
			content = n
			break
		}
	}
	var b strings.Builder
	if page.Title != "" {
		fmt.Fprintf(&b, "# %s\n\n", page.Title)
	}
	renderPage(&b, content)
	page.Content = cleanPageText(b.String())
	return page, nil
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

func renderPage(b *strings.Builder, n *html.Node) {
	if n.Type == html.TextNode {
		b.WriteString(n.Data)
		return
	}
	if n.Type != html.ElementNode && n.Type != html.DocumentNode || skipPageNode(n) {
		return
	}
	switch n.Data {
	case "del", "s", "strike":
		fmt.Fprintf(b, " [Superseded text: %s] ", strings.Join(strings.Fields(nodeText(n)), " "))
		return
	case "br":
		b.WriteString("\n")
	case "h1", "h2", "h3", "h4", "h5", "h6":
		fmt.Fprintf(b, "\n\n%s ", strings.Repeat("#", int(n.Data[1]-'0')))
	case "p", "div", "section", "li", "ul", "ol", "table":
		b.WriteString("\n\n")
	case "tr":
		b.WriteString("\n")
	case "td", "th":
		b.WriteString(" | ")
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		renderPage(b, c)
	}
	switch n.Data {
	case "p", "div", "section", "li", "ul", "ol", "table", "h1", "h2", "h3", "h4", "h5", "h6":
		b.WriteString("\n\n")
	}
}

func cleanPageText(s string) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" && (len(lines) == 0 || lines[len(lines)-1] == "") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
