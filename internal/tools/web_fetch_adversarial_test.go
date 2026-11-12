package tools

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

type heldTLSRead struct {
	net.Conn
	entered, release chan struct{}
	once             sync.Once
}

func (c *heldTLSRead) Write(p []byte) (int, error) { return len(p), nil }
func (c *heldTLSRead) Read(p []byte) (int, error) {
	c.once.Do(func() { close(c.entered) })
	<-c.release
	return 0, io.EOF
}
func TestFetchCancellationJoinsTLS(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	held := &heldTLSRead{Conn: client, entered: make(chan struct{}), release: make(chan struct{})}
	unblock := sync.OnceFunc(func() { close(held.release) })
	defer unblock()
	f := NewFetcherWithNetwork(publicDNS, func(context.Context, string, string) (net.Conn, error) { return held, nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() { _, err := f.Fetch(ctx, FetchArgs{URL: "https://source.example/"}); returned <- err }()
	<-held.entered
	cancel()
	select {
	case <-returned:
		t.Fatal("fetch returned while TLS handshake read remained active")
	case <-time.After(50 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-returned:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("TLS handshake did not join")
	}
}

func TestFetchDefaultOperationDeadline(t *testing.T) {
	f := NewFetcherWithNetwork(func(ctx context.Context, _ string) ([]netip.Addr, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.InDelta(t, 20, time.Until(deadline).Seconds(), 0.5)
		return nil, errors.New("stop before network")
	}, nil)
	_, err := f.Fetch(context.Background(), FetchArgs{URL: "http://source.example/"})
	require.Error(t, err)
}
func TestFetchDeflateAndCharacterDecoding(t *testing.T) {
	var b bytes.Buffer
	z := zlib.NewWriter(&b)
	_, err := z.Write([]byte("caf\xe9"))
	require.NoError(t, err)
	require.NoError(t, z.Close())
	f, _ := wireFetcher(t, publicDNS, func(_ int, _ *http.Request, c net.Conn) {
		response(c, 200, "Content-Type: text/plain; charset=windows-1252\r\nContent-Encoding: deflate\r\n", b.String())
	})
	r, err := f.Fetch(context.Background(), FetchArgs{URL: "http://source.example/"})
	require.NoError(t, err)
	require.Equal(t, "café", r.Text)
	require.False(t, r.Truncated)
}
func TestFetchOutputTruncationPreservesUTF8(t *testing.T) {
	f, _ := wireFetcher(t, publicDNS, func(_ int, _ *http.Request, c net.Conn) {
		response(c, 200, "Content-Type: text/plain\r\n", strings.Repeat("界", 6000))
	})
	r, err := f.Fetch(context.Background(), FetchArgs{URL: "http://source.example/"})
	require.NoError(t, err)
	require.True(t, r.Truncated)
	require.True(t, utf8.ValidString(r.Text))
	require.Len(t, r.Text, 15999)
}
func TestFetchPublicIPv6Pinned(t *testing.T) {
	f, w := wireFetcher(t, func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("2606:4700:4700::1111")}, nil
	}, textResponse)
	r, err := f.Fetch(context.Background(), FetchArgs{URL: "http://source.example/"})
	require.NoError(t, err)
	require.Equal(t, "readable evidence", r.Text)
	require.Equal(t, []string{"[2606:4700:4700::1111]:80"}, w.addresses)
}
func TestFetchHTTPSPinsIPButVerifiesHostnameAndCertificate(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"source.example"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	require.NoError(t, err)
	names := make(chan string, 1)
	done := make(chan struct{})
	f := NewFetcherWithNetwork(publicDNS, func(ctx context.Context, _, addr string) (net.Conn, error) {
		if addr != "93.184.216.34:443" {
			return nil, fmt.Errorf("unpinned HTTPS %s", addr)
		}
		client, server := net.Pipe()
		go func() {
			defer close(done)
			defer server.Close()
			s := tls.Server(server, &tls.Config{GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
				names <- hello.ServerName
				return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
			}})
			_ = s.HandshakeContext(ctx)
		}()
		return client, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := f.Fetch(ctx, FetchArgs{URL: "https://source.example/"})
	require.Error(t, err)
	require.Empty(t, r.Text)
	select {
	case name := <-names:
		require.Equal(t, "source.example", name)
	case <-time.After(time.Second):
		t.Fatal("TLS ClientHello not sent")
	}
	<-done
	var verification *tls.CertificateVerificationError
	require.ErrorAs(t, err, &verification, "must not disable TLS verification to pin IPs")
}
