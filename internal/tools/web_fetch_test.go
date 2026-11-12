package tools

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Only DNS and the wire are replaced. The production HTTP transport, redirect
// policy, pinned address selection, decompression and extraction remain real.
type fetchWire struct {
	mu        sync.Mutex
	addresses []string
	requests  []*http.Request
	wg        sync.WaitGroup
	serve     func(int, *http.Request, net.Conn)
}

func wireFetcher(t *testing.T, lookup func(context.Context, string) ([]netip.Addr, error), serve func(int, *http.Request, net.Conn)) (*Fetcher, *fetchWire) {
	t.Helper()
	w := &fetchWire{serve: serve}
	t.Cleanup(func() { w.wg.Wait() })
	f := NewFetcherWithNetwork(lookup, func(ctx context.Context, network, address string) (net.Conn, error) {
		client, server := net.Pipe()
		w.mu.Lock()
		w.addresses = append(w.addresses, address)
		w.mu.Unlock()
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			defer server.Close()
			r, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				return
			}
			defer r.Body.Close()
			w.mu.Lock()
			n := len(w.requests)
			w.requests = append(w.requests, r)
			w.mu.Unlock()
			serve(n, r, server)
		}()
		return client, nil
	})
	return f, w
}
func publicDNS(context.Context, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}
func response(c net.Conn, status int, headers, body string) {
	fmt.Fprintf(c, "HTTP/1.1 %d status\r\nContent-Length: %d\r\n%sConnection: close\r\n\r\n%s", status, len(body), headers, body)
}
func textResponse(_ int, _ *http.Request, c net.Conn) {
	response(c, 200, "Content-Type: text/plain\r\n", "readable evidence")
}

func TestFetchBlocksPrivateAndMappedAddresses(t *testing.T) {
	for _, ip := range []string{"0.0.0.0", "0.1.2.3", "10.0.0.1", "100.64.0.1", "127.0.0.1", "169.254.169.254", "172.16.0.1", "192.168.1.1", "192.0.0.9", "192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1", "255.255.255.255", "::", "::1", "::ffff:93.184.216.34", "::ffff:127.0.0.1", "64:ff9b::a00:1", "100::1", "2001:db8::1", "2001::1", "2002:7f00:1::", "fc00::1", "fe80::1", "ff02::1"} {
		t.Run(ip, func(t *testing.T) {
			f, w := wireFetcher(t, func(context.Context, string) ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr(ip)}, nil }, textResponse)
			_, err := f.Fetch(context.Background(), FetchArgs{URL: "http://source.example/"})
			require.Error(t, err)
			require.Empty(t, w.addresses)
			host := ip
			if strings.Contains(ip, ":") {
				host = "[" + ip + "]"
			}
			_, err = f.Fetch(context.Background(), FetchArgs{URL: "http://" + host + "/"})
			require.Error(t, err)
			require.Empty(t, w.addresses)
		})
	}
}
func TestFetchPinsValidatedDNS(t *testing.T) {
	var lookups atomic.Int32
	f, w := wireFetcher(t, func(context.Context, string) ([]netip.Addr, error) {
		if lookups.Add(1) > 1 {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return publicDNS(context.Background(), "")
	}, textResponse)
	result, err := f.Fetch(context.Background(), FetchArgs{URL: "http://source.example:8080/a"})
	require.NoError(t, err)
	require.Equal(t, "readable evidence", result.Text)
	require.Equal(t, []string{"93.184.216.34:8080"}, w.addresses)
	require.Equal(t, "source.example:8080", w.requests[0].Host)
	require.EqualValues(t, 1, lookups.Load())
	t.Run("mixed public private fails before dialing", func(t *testing.T) {
		f, w := wireFetcher(t, func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.1.2.3")}, nil
		}, textResponse)
		_, err := f.Fetch(context.Background(), FetchArgs{URL: "http://source.example"})
		require.Error(t, err)
		require.Empty(t, w.addresses)
	})
}
func TestFetchRevalidatesRedirect(t *testing.T) {
	for _, target := range []string{"http://127.0.0.1/", "http://[::ffff:93.184.216.34]/", "http://u:p@source.example/", "file:///etc/passwd", "http://private.example/", "http://source.example/rebind"} {
		t.Run(target, func(t *testing.T) {
			var lookups atomic.Int32
			f, w := wireFetcher(t, func(_ context.Context, host string) ([]netip.Addr, error) {
				n := lookups.Add(1)
				if host == "private.example" || n > 1 {
					return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
				}
				return publicDNS(context.Background(), host)
			}, func(_ int, _ *http.Request, c net.Conn) { response(c, 302, "Location: "+target+"\r\n", "") })
			_, err := f.Fetch(context.Background(), FetchArgs{URL: "http://source.example/start"})
			require.Error(t, err)
			require.Len(t, w.addresses, 1)
		})
	}
}
func TestFetchRedirectLimitAndSource(t *testing.T) {
	for _, redirects := range []int{3, 4} {
		t.Run(fmt.Sprint(redirects), func(t *testing.T) {
			f, w := wireFetcher(t, publicDNS, func(n int, r *http.Request, c net.Conn) {
				if n < redirects {
					response(c, 302, fmt.Sprintf("Location: /%d\r\nSet-Cookie: secret=bad\r\n", n+1), "")
				} else {
					textResponse(n, r, c)
				}
			})
			result, err := f.Fetch(context.Background(), FetchArgs{URL: "http://source.example/start"})
			if redirects == 4 {
				require.Error(t, err)
				require.Len(t, w.requests, 4)
			} else {
				require.NoError(t, err)
				require.Equal(t, "http://source.example/start", result.RequestedURL)
				require.Equal(t, "http://source.example/3", result.FinalURL)
				for _, r := range w.requests {
					require.Equal(t, "GET", r.Method)
					require.Empty(t, r.Header.Get("Cookie"))
					require.Empty(t, r.Header.Get("Authorization"))
					require.Empty(t, r.Header.Get("Referer"))
				}
			}
		})
	}
}
func TestFetchIgnoresEnvProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	f, w := wireFetcher(t, publicDNS, textResponse)
	_, err := f.Fetch(context.Background(), FetchArgs{URL: "http://source.example/"})
	require.NoError(t, err)
	require.Equal(t, []string{"93.184.216.34:80"}, w.addresses)
	require.Equal(t, "/", w.requests[0].RequestURI)
}
func TestFetchDecodedBodyBound(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(fmt.Sprint(compressed), func(t *testing.T) {
			body := strings.Repeat(" ", 2*1024*1024) + "AFTER_DECODED_LIMIT"
			headers := "Content-Type: text/plain\r\n"
			if compressed {
				var b bytes.Buffer
				z := gzip.NewWriter(&b)
				_, err := io.WriteString(z, body)
				require.NoError(t, err)
				require.NoError(t, z.Close())
				body = b.String()
				headers += "Content-Encoding: gzip\r\n"
			}
			f, _ := wireFetcher(t, publicDNS, func(_ int, _ *http.Request, c net.Conn) { response(c, 200, headers, body) })
			r, err := f.Fetch(context.Background(), FetchArgs{URL: "http://source.example/"})
			require.NoError(t, err)
			require.True(t, r.Truncated)
			require.Empty(t, r.Text, "decoded input must stop before the marker, independently of the text-output cap")
		})
	}
}
func TestFetchInvalidUTF8CannotBecomeTruncatedSuccess(t *testing.T) {
	f, _ := wireFetcher(t, publicDNS, func(_ int, _ *http.Request, c net.Conn) {
		response(c, 200, "Content-Type: text/plain\r\n", "\xff"+strings.Repeat("a", 2*1024*1024))
	})
	r, err := f.Fetch(context.Background(), FetchArgs{URL: "http://source.example/"})
	require.Error(t, err)
	require.Empty(t, r.Text)
}
func TestFetchBodyFailuresNeverPublishPartialSuccess(t *testing.T) {
	for _, kind := range []string{"short", "bad-gzip", "unknown-encoding", "unsupported-type", "http-error"} {
		t.Run(kind, func(t *testing.T) {
			f, _ := wireFetcher(t, publicDNS, func(_ int, _ *http.Request, c net.Conn) {
				switch kind {
				case "short":
					io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 200\r\n\r\npartial")
				case "bad-gzip":
					var b bytes.Buffer
					z := gzip.NewWriter(&b)
					io.WriteString(z, "partial")
					z.Close()
					b.Bytes()[b.Len()-8] ^= 0xff
					response(c, 200, "Content-Type: text/plain\r\nContent-Encoding: gzip\r\n", b.String())
				case "unknown-encoding":
					response(c, 200, "Content-Type: text/plain\r\nContent-Encoding: br\r\n", "partial")
				case "unsupported-type":
					response(c, 200, "Content-Type: application/octet-stream\r\n", "partial")
				case "http-error":
					response(c, 401, "Content-Type: text/plain\r\n", "partial")
				}
			})
			r, err := f.Fetch(context.Background(), FetchArgs{URL: "http://source.example/"})
			require.Error(t, err)
			require.Empty(t, r.Text)
		})
	}
}
func TestFetchTotalDeadlineIncludesBody(t *testing.T) {
	f, w := wireFetcher(t, publicDNS, func(_ int, _ *http.Request, c net.Conn) {
		io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 999\r\n\r\npartial")
		io.Copy(io.Discard, c)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	r, err := f.Fetch(ctx, FetchArgs{URL: "http://source.example/"})
	require.Error(t, err)
	require.Empty(t, r.Text)
	require.Less(t, time.Since(start), time.Second)
	w.wg.Wait()
}
func TestFetchExtractsOnlyReadableSupportedContent(t *testing.T) {
	for _, tc := range []struct{ mime, body, want string }{{"text/html; charset=utf-8", `<html><head><title>hidden title</title><style>hidden style</style></head><body><h1>Readable &amp; safe</h1><script>hidden script</script><noscript>hidden fallback</noscript><template>hidden template</template><svg><text>hidden svg</text></svg><iframe>hidden frame</iframe><p hidden>hidden text</p><p aria-hidden="true">hidden aria</p><p>Second paragraph</p></body></html>`, "Readable & safe\nSecond paragraph"}, {"text/plain", "plain evidence", "plain evidence"}, {"text/markdown", "# Evidence", "# Evidence"}, {"application/json", `{"evidence":true}`, `{"evidence":true}`}} {
		t.Run(tc.mime, func(t *testing.T) {
			f, _ := wireFetcher(t, publicDNS, func(_ int, _ *http.Request, c net.Conn) { response(c, 200, "Content-Type: "+tc.mime+"\r\n", tc.body) })
			r, err := f.Fetch(context.Background(), FetchArgs{URL: "http://source.example/"})
			require.NoError(t, err)
			require.Equal(t, tc.want, r.Text)
			require.False(t, r.Truncated)
		})
	}
}
func TestFetchExtractionHonorsTotalDeadline(t *testing.T) {
	f, _ := wireFetcher(t, publicDNS, func(_ int, _ *http.Request, c net.Conn) {
		response(c, 200, "Content-Type: text/html\r\n", strings.Repeat("<div>", 20000)+"visible")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := f.Fetch(ctx, FetchArgs{URL: "http://source.example/"})
	// The pinned HTML parser rejects >512 nested elements rather than doing
	// unbounded DOM work. Deadline expiration is also a valid failed outcome.
	require.Error(t, err)
	require.Less(t, time.Since(start), time.Second)
}

func TestFetchCancellationJoinsDial(t *testing.T) {
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	f := NewFetcherWithNetwork(publicDNS, func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(entered)
		<-release
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, err := f.Fetch(ctx, FetchArgs{URL: "http://source.example/"}); returned <- err }()
	<-entered
	cancel()
	select {
	case <-returned:
		t.Fatal("fetch returned while its physical dial was still active")
	case <-time.After(50 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-returned:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("dial not joined")
	}
}

func TestFetchRejectsCredentialsAndAmbiguousURLsBeforeDNS(t *testing.T) {
	for _, u := range []string{"http://user:pass@source.example/", "http://user@source.example/", "ftp://source.example/", "http://[fe80::1%25en0]/", "http://source.example:0/", "http://source.example:65536/", "http://source.example/#fragment", "http://source.example\\@127.0.0.1/"} {
		t.Run(u, func(t *testing.T) {
			var lookups atomic.Int32
			f, w := wireFetcher(t, func(ctx context.Context, h string) ([]netip.Addr, error) { lookups.Add(1); return publicDNS(ctx, h) }, textResponse)
			_, err := f.Fetch(context.Background(), FetchArgs{URL: u})
			require.Error(t, err)
			require.Zero(t, lookups.Load())
			require.Empty(t, w.addresses)
		})
	}
}
