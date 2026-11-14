package tools

import (
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const (
	fetchTimeout   = 20 * time.Second
	fetchBodyLimit = 2 * 1024 * 1024
	fetchTextLimit = 16000
)

type FetchArgs struct {
	URL string `json:"url"`
}
type FetchResult struct {
	RequestedURL string `json:"requestedUrl"`
	FinalURL     string `json:"finalUrl"`
	ContentType  string `json:"contentType"`
	Text         string `json:"text"`
	Truncated    bool   `json:"truncated"`
}

// Fetcher owns no persistent connections or goroutines. Each Fetch joins its
// request/body before returning; it is safe to call concurrently. Authorization
// belongs to BeginWebTool, not this lower-level HTTP primitive.
type Fetcher struct {
	lookup func(context.Context, string) ([]netip.Addr, error)
	dial   func(context.Context, string, string) (net.Conn, error)
}

// Avoid a detached transport TLS handshake and a graceful close_notify write
// extending the operation after cancellation. We own and close the raw socket.
type fetchTLSConnection struct {
	*tls.Conn
	raw net.Conn
}

func (c *fetchTLSConnection) Close() error { return c.raw.Close() }

func NewFetcher() *Fetcher {
	return NewFetcherWithNetwork(func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}, (&net.Dialer{Timeout: fetchTimeout}).DialContext)
}

// NewFetcherWithNetwork is an explicit dependency-injection seam, not a runtime
// policy option. Even injected DNS answers pass the same public-address checks;
// the dialer receives only validated literal IPs. Production uses NewFetcher.
func NewFetcherWithNetwork(lookup func(context.Context, string) ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error)) *Fetcher {
	return &Fetcher{lookup: lookup, dial: dial}
}

func parseFetchURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Opaque != "" || u.Host == "" || u.Hostname() == "" || u.Fragment != "" || strings.ContainsAny(u.Host, "%\\") {
		return nil, errors.New("invalid fetch URL")
	}
	if strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("invalid fetch port")
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return nil, errors.New("invalid fetch port")
		}
	}
	u.Host = strings.ToLower(u.Host)
	return u, nil
}

// Fail closed for special-purpose ranges, including translation/tunneling
// prefixes which can encode a private IPv4 destination. IPv6 is limited to
// global-unicast-shaped space, excluding the documented special/retired blocks.
// This static policy is not an allocation or routing certificate. Sources and
// conservative coverage are recorded in docs/web-chat-fetch.md.
var blockedFetchPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "2002::/16", "3ffe::/16", "3fff::/20"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func publicFetchAddress(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" || ip.Is4In6() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, p := range blockedFetchPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
func (f *Fetcher) addresses(ctx context.Context, u *url.URL) ([]netip.Addr, error) {
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		ips = []netip.Addr{ip}
	} else {
		var e error
		ips, e = f.lookup(ctx, u.Hostname())
		if e != nil {
			return nil, e
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("no public addresses")
	}
	for _, ip := range ips {
		if !publicFetchAddress(ip) {
			return nil, errors.New("non-public fetch address")
		}
	}
	return ips, nil
}
func (f *Fetcher) Fetch(parent context.Context, args FetchArgs) (FetchResult, error) {
	ctx, cancel := context.WithTimeout(parent, fetchTimeout)
	defer cancel()
	u, err := parseFetchURL(args.URL)
	if err != nil {
		return FetchResult{}, err
	}
	requested := u.String()
	for redirects := 0; ; redirects++ {
		if err = ctx.Err(); err != nil {
			return FetchResult{}, err
		}
		ips, e := f.addresses(ctx, u)
		if e != nil {
			return FetchResult{}, e
		}
		port := u.Port()
		if port == "" {
			port = "80"
			if u.Scheme == "https" {
				port = "443"
			}
		}
		// Dial synchronously under the operation context. net/http otherwise
		// detaches dialing from RoundTrip cancellation and can return while a
		// physical dial still runs, violating the activity cleanup barrier.
		var conn net.Conn
		for _, ip := range ips {
			if e = ctx.Err(); e != nil {
				return FetchResult{}, e
			}
			conn, e = f.dial(ctx, "tcp", net.JoinHostPort(ip.String(), port))
			if e == nil {
				break
			}
		}
		if e != nil {
			return FetchResult{}, e
		}
		// The raw connection is owned even if Transport exits before consuming it.
		// At most four hops; also close promptly after every response below.
		defer conn.Close()
		if u.Scheme == "https" {
			secure := tls.Client(conn, &tls.Config{ServerName: u.Hostname()})
			if e = secure.HandshakeContext(ctx); e != nil {
				return FetchResult{}, e
			}
			conn = &fetchTLSConnection{Conn: secure, raw: conn}
		}
		var take sync.Once
		// Fresh transport per hop: no cross-hop connection reuse, proxy inheritance,
		// cookie jar, implicit redirects, forwarded credentials or custom headers.
		transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 64 * 1024, TLSHandshakeTimeout: fetchTimeout, ResponseHeaderTimeout: fetchTimeout,
			DialContext: func(context.Context, string, string) (out net.Conn, err error) {
				take.Do(func() { out = conn })
				if out == nil {
					err = errors.New("fetch connection already consumed")
				}
				return
			},
		}
		// TLS is already complete and hostname-verified; do not let Transport
		// start its own handshake goroutine after cancellation.
		transport.DialTLSContext = transport.DialContext
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if e != nil {
			return FetchResult{}, e
		}
		req.Header.Set("Accept", "text/html, text/plain, text/markdown, application/json")
		req.Header.Set("Accept-Encoding", "gzip, deflate")
		resp, e := transport.RoundTrip(req)
		if e != nil {
			transport.CloseIdleConnections()
			return FetchResult{}, e
		}
		if resp.StatusCode == 301 || resp.StatusCode == 302 || resp.StatusCode == 303 || resp.StatusCode == 307 || resp.StatusCode == 308 {
			location := resp.Header.Get("Location")
			resp.Body.Close()
			conn.Close()
			transport.CloseIdleConnections()
			if redirects >= 3 || location == "" {
				return FetchResult{}, errors.New("fetch redirect limit or missing location")
			}
			next, e := url.Parse(location)
			if e != nil {
				return FetchResult{}, e
			}
			u, e = parseFetchURL(u.ResolveReference(next).String())
			if e != nil {
				return FetchResult{}, e
			}
			continue
		}
		result, e := readFetchResponse(ctx, resp)
		resp.Body.Close()
		conn.Close()
		transport.CloseIdleConnections()
		if e != nil {
			return FetchResult{}, e
		}
		if e = ctx.Err(); e != nil {
			return FetchResult{}, e
		}
		result.RequestedURL = requested
		result.FinalURL = u.String()
		return result, nil
	}
}
func readFetchResponse(ctx context.Context, resp *http.Response) (FetchResult, error) {
	var result FetchResult
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, fmt.Errorf("fetch HTTP status %d", resp.StatusCode)
	}
	media, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return result, errors.New("invalid fetch content type")
	}
	switch media {
	case "text/html", "text/plain", "text/markdown", "application/json":
	default:
		return result, errors.New("unsupported fetch content type")
	}
	var body io.Reader = resp.Body
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		z, e := gzip.NewReader(body)
		if e != nil {
			return result, e
		}
		defer z.Close()
		body = z
	case "deflate":
		z, e := zlib.NewReader(body)
		if e != nil {
			return result, e
		}
		defer z.Close()
		body = z
	default:
		return result, errors.New("unsupported fetch content encoding")
	}
	if label := params["charset"]; label != "" {
		body, err = charset.NewReaderLabel(label, body)
		if err != nil {
			return result, err
		}
	}
	// Limit after decompression AND character decoding, independent of wire
	// Content-Length. A read/checksum/cancellation error discards partial text.
	raw, err := io.ReadAll(io.LimitReader(body, fetchBodyLimit+1))
	if err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	result.Truncated = len(raw) > fetchBodyLimit
	if result.Truncated {
		raw = raw[:fetchBodyLimit]
		// Trim only a code point split by our boundary, not arbitrary invalid
		// content (which must fail below). Never repeatedly scan a shrinking body.
		start := len(raw) - 1
		for start > 0 && start > len(raw)-utf8.UTFMax && !utf8.RuneStart(raw[start]) {
			start--
		}
		if !utf8.FullRune(raw[start:]) {
			raw = raw[:start]
		}
	}
	if !utf8.Valid(raw) {
		return FetchResult{}, errors.New("fetch text is not UTF-8")
	}
	text := string(raw)
	if media == "text/html" {
		text, err = readableHTML(ctx, text)
		if err != nil {
			return FetchResult{}, err
		}
	}
	text = strings.TrimSpace(text)
	if len(text) > fetchTextLimit {
		result.Truncated = true
		text = text[:fetchTextLimit]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	result.Text = text
	result.ContentType = media
	return result, nil
}
func readableHTML(ctx context.Context, raw string) (string, error) {
	root, err := parseFetchHTML(ctx, raw)
	if err != nil {
		return "", err
	}
	return extractFetchHTML(ctx, root)
}

// Keep the third-party parser on this goroutine, with bounded buffered input
// between context checks. Its own token/tree work remains bounded by the decoded
// body limit and pinned parser's nesting limit; this is cooperative cancellation.
const fetchHTMLCheckBytes = 4096

type fetchHTMLReader struct {
	ctx    context.Context
	reader *strings.Reader
}

func (r *fetchHTMLReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > fetchHTMLCheckBytes {
		p = p[:fetchHTMLCheckBytes]
	}
	return r.reader.Read(p)
}
func parseFetchHTML(ctx context.Context, raw string) (*html.Node, error) {
	root, err := html.Parse(&fetchHTMLReader{ctx: ctx, reader: strings.NewReader(raw)})
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return root, nil
}
func extractFetchHTML(ctx context.Context, root *html.Node) (string, error) {
	var b strings.Builder
	var visit func(*html.Node) error
	visit = func(n *html.Node) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "head", "script", "style", "noscript", "template", "svg", "math", "iframe", "object", "embed", "canvas", "audio", "video", "form", "input", "button", "select", "textarea":
				return nil
			}
			for _, a := range n.Attr {
				if err := ctx.Err(); err != nil {
					return err
				}
				if a.Key == "hidden" || (a.Key == "aria-hidden" && strings.EqualFold(a.Val, "true")) {
					return nil
				}
				if a.Key == "style" {
					hidden, err := hiddenFetchStyle(ctx, a.Val)
					if err != nil {
						return err
					}
					if hidden {
						return nil
					}
				}
			}
			switch n.Data {
			case "p", "div", "br", "li", "h1", "h2", "h3", "h4", "h5", "h6", "section", "article", "tr":
				b.WriteByte('\n')
			}
		}
		if n.Type == html.TextNode {
			// A single shallow text node can contain the entire decoded body.
			for text := n.Data; len(text) > 0; {
				if err := ctx.Err(); err != nil {
					return err
				}
				end := min(len(text), fetchHTMLCheckBytes)
				b.WriteString(text[:end])
				text = text[end:]
			}
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if err := visit(c); err != nil {
				return err
			}
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "p", "div", "li", "h1", "h2", "h3", "h4", "h5", "h6", "section", "article", "tr":
				b.WriteByte('\n')
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
		return "", err
	}
	return normalizeFetchHTML(ctx, b.String())
}

// Streaming normalization avoids whole-buffer Fields/Split/Join/ToLower work.
// Keep one extra code point so the caller can mark and UTF-8-trim output exactly.
func normalizeFetchHTML(ctx context.Context, raw string) (string, error) {
	var b strings.Builder
	var pending byte
	nextCheck := 0
	for i, r := range raw {
		if i >= nextCheck {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			nextCheck = i + fetchHTMLCheckBytes
		}
		if unicode.IsSpace(r) {
			if r == '\n' {
				pending = '\n'
			} else if pending == 0 {
				pending = ' '
			}
			continue
		}
		if b.Len() > 0 && pending != 0 {
			b.WriteByte(pending)
		}
		pending = 0
		b.WriteRune(r)
		if b.Len() > fetchTextLimit {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return b.String(), nil
}

func hiddenFetchStyle(ctx context.Context, raw string) (bool, error) {
	recent := ""
	nextCheck := 0
	for i, r := range raw {
		if i >= nextCheck {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			nextCheck = i + fetchHTMLCheckBytes
		}
		if unicode.IsSpace(r) {
			continue
		}
		recent += string(unicode.ToLower(r))
		if len(recent) > len("visibility:hidden") {
			recent = recent[len(recent)-len("visibility:hidden"):]
		}
		if strings.Contains(recent, "display:none") || strings.Contains(recent, "visibility:hidden") {
			return true, nil
		}
	}
	return false, ctx.Err()
}
