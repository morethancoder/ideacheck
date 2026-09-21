package search

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
	"codeberg.org/readeck/go-readability/v2/render"
	"golang.org/x/net/html"
)

const (
	maxPage   = 2 << 20 // bytes of HTML read from one page
	thinPage  = 400     // runes: below this the article extraction found too little
	userAgent = "ideacheck/1 (+https://github.com/morethancoder/ideacheck)"
)

// Reader fetches pages and boils them down to their text.
type Reader struct {
	Client *http.Client
}

// NewReader builds a reader that only talks to the public internet. The URLs
// it is handed come out of search results, and `ideacheck serve` may run where
// 127.0.0.1 or 10.x is somebody's admin panel: a search result must never be a
// way to read it. The check is made on the address actually dialled, after DNS
// and after every redirect.
func NewReader(timeout time.Duration) *Reader {
	dialer := &net.Dialer{Timeout: timeout, Control: publicOnly}
	return &Reader{Client: &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: timeout, MaxIdleConnsPerHost: 2},
	}}
}

var errPrivate = errors.New("refusing to read a page on a private or local address")

func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return errPrivate
	}
	return nil
}

// Read returns the page's readable text, at most maxChars runes of it.
func (r *Reader) Read(ctx context.Context, pageURL string, maxChars int) (string, error) {
	u, err := url.Parse(pageURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("not a web page: %q", pageURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	res, err := r.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return "", fmt.Errorf("%s", res.Status)
	}
	if ct := res.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "html") {
		return "", fmt.Errorf("not HTML (%s)", ct)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxPage))
	if err != nil {
		return "", err
	}
	return clipText(Digest(raw, u), maxChars), nil
}

// Digest turns a page's HTML into the text a person would read on it. An
// article extractor goes first: it drops navigation, cookie banners, footers and
// related-links rails, which on most pages outweigh the content. It is built
// for articles, though, and a product's landing page often is not one — when it
// comes back with next to nothing, the visible text of the whole body is used
// instead, minus the parts that are never content.
func Digest(raw []byte, pageURL *url.URL) string {
	article, err := readability.FromReader(bytes.NewReader(raw), pageURL)
	text := ""
	if err == nil && article.Node != nil {
		text = Text(render.InnerText(article.Node))
	}
	if len([]rune(text)) >= thinPage {
		return withTitle(article.Title(), text)
	}
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return text
	}
	strip(doc)
	if body := Text(render.InnerText(doc)); len(body) > len(text) {
		text = body
	}
	title := ""
	if err == nil {
		title = article.Title()
	}
	return withTitle(title, text)
}

func withTitle(title, text string) string {
	if title = Text(title); title != "" && !strings.HasPrefix(text, title) {
		return title + "\n" + text
	}
	return text
}

// never holds the elements whose text is never the page's content.
var never = map[string]bool{"script": true, "style": true, "noscript": true, "svg": true, "template": true,
	"nav": true, "footer": true, "header": true, "aside": true, "form": true, "iframe": true}

func strip(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.ElementNode && never[c.Data] {
			n.RemoveChild(c)
		} else {
			strip(c)
		}
		c = next
	}
}

// Text collapses whitespace, keeping paragraph breaks: a model reads the result,
// and runs of blank lines and indentation are tokens that say nothing.
func Text(s string) string {
	var paragraphs []string
	for _, p := range strings.Split(s, "\n") {
		if p = strings.Join(strings.Fields(p), " "); p != "" {
			paragraphs = append(paragraphs, p)
		}
	}
	return strings.Join(paragraphs, "\n")
}

func clipText(s string, n int) string {
	if r := []rune(s); n > 0 && len(r) > n {
		return strings.TrimSpace(string(r[:n])) + "…"
	}
	return s
}
