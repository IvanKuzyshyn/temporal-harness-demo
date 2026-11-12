package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
)

type Config struct {
	Attachments   *attachments.Store
	ListenAddress string
	ProxySecret   string
	BrowserOrigin string
	// Task4 supplies a live transport readiness check. Nil explicitly means no
	// execution gateway; it does NOT disable durable command acceptance.
	GatewayConnected    func() bool
	WorkersPolling      func() bool
	ReconciliationReady func() bool
	RequestTimeout      time.Duration
}

func (c Config) validate() error {
	host, port, err := net.SplitHostPort(c.ListenAddress)
	if err != nil {
		return errors.New("listen address must be a loopback IP and port")
	}
	ip := net.ParseIP(host)
	n, e := strconv.Atoi(port)
	if ip == nil || !ip.IsLoopback() || e != nil || n < 1 || n > 65535 {
		return errors.New("listen address must be a loopback IP and port")
	}
	b, e := hex.DecodeString(c.ProxySecret)
	if e != nil || len(b) != 32 || strings.ToLower(c.ProxySecret) != c.ProxySecret {
		return errors.New("proxy secret must be 32 random bytes encoded as lowercase hex")
	}
	distinct := map[byte]bool{}
	for _, v := range b {
		distinct[v] = true
	}
	if len(distinct) < 8 {
		return errors.New("proxy secret is a placeholder or insufficiently varied")
	}
	u, e := url.Parse(c.BrowserOrigin)
	if e != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("browser origin must be an exact loopback HTTP origin")
	}
	ip = net.ParseIP(u.Hostname())
	if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("browser origin must be loopback")
	}
	if u.Port() != "" {
		p, e := strconv.Atoi(u.Port())
		if e != nil || p < 1 || p > 65535 {
			return errors.New("invalid browser origin port")
		}
	}
	if c.RequestTimeout < 0 || c.RequestTimeout > 30*time.Second {
		return errors.New("request timeout must not exceed 30 seconds")
	}
	return nil
}
func (s *Server) secure(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	supplied := sha256.Sum256([]byte(r.Header.Get("X-Webchat-Secret")))
	if r.Host != s.config.ListenAddress || len(r.Header.Values("X-Webchat-Secret")) != 1 || subtle.ConstantTimeCompare(s.secretHash[:], supplied[:]) != 1 {
		fail(w, 403, "forbidden")
		return false
	}
	// The proxy must construct its upstream headers, not forward browser Host,
	// cookies, or authorization. If it forwards browser security context, only
	// the exact configured origin and same-origin fetch context are accepted.
	origin := r.Header.Get("Origin")
	site := r.Header.Get("Sec-Fetch-Site")
	mode := r.Header.Get("Sec-Fetch-Mode")
	if len(r.Header.Values("Origin")) > 1 || (origin != "" && origin != s.config.BrowserOrigin) || (site != "" && site != "same-origin") || (mode != "" && mode != "cors" && mode != "same-origin") {
		fail(w, 403, "forbidden")
		return false
	}
	return true
}
