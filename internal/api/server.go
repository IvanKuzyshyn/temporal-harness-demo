// Package api serves only the authenticated loopback Next.js upstream. It is
// not a browser API, an agent loop, or a source of execution authorization.
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/jackc/pgx/v5/pgconn"
)

type Server struct {
	repo       *storage.Repository
	config     Config
	secretHash [32]byte
}

func NewServer(repo *storage.Repository, cfg Config) (*Server, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 5 * time.Second
	}
	return &Server{repo: repo, config: cfg, secretHash: sha256.Sum256([]byte(cfg.ProxySecret))}, nil
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, code string) {
	respond(w, status, map[string]any{"error": map[string]string{"code": code}})
}
func storageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, chat.ErrInvalid):
		fail(w, 400, "invalid_request")
	case errors.Is(err, storage.ErrNotFound):
		fail(w, 404, "not_found")
	case errors.Is(err, storage.ErrConflict):
		fail(w, 409, "conflict")
	default:
		var pg *pgconn.PgError
		if errors.As(err, &pg) {
			switch pg.Code {
			case "23503":
				fail(w, 404, "reference_not_found")
				return
			case "23514", "22P02", "22021":
				fail(w, 400, "invalid_request")
				return
			case "23505":
				fail(w, 409, "conflict")
				return
			}
		}
		fail(w, 503, "storage_unavailable")
	}
}
func decode(w http.ResponseWriter, r *http.Request, out any, allowed ...string) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, 415, "unsupported_media_type")
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 400*1024))
	if err != nil {
		var max *http.MaxBytesError
		if errors.As(err, &max) {
			fail(w, 413, "body_too_large")
		} else {
			fail(w, 400, "invalid_json")
		}
		return false
	}
	if !utf8.Valid(raw) {
		fail(w, 400, "invalid_json")
		return false
	}
	fields := map[string]json.RawMessage{}
	object := json.NewDecoder(bytes.NewReader(raw))
	start, err := object.Token()
	if err != nil || start != json.Delim('{') {
		fail(w, 400, "invalid_json")
		return false
	}
	for object.More() {
		token, err := object.Token()
		key, ok := token.(string)
		if err != nil || !ok || fields[key] != nil {
			fail(w, 400, "invalid_json")
			return false
		}
		var value json.RawMessage
		if object.Decode(&value) != nil {
			fail(w, 400, "invalid_json")
			return false
		}
		fields[key] = value
	}
	if end, err := object.Token(); err != nil || end != json.Delim('}') {
		fail(w, 400, "invalid_json")
		return false
	}
	keys := map[string]bool{}
	for _, k := range allowed {
		keys[k] = true
	}
	for k, v := range fields {
		if !keys[k] || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			fail(w, 400, "invalid_request")
			return false
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		fail(w, 400, "invalid_json")
		return false
	}
	return true
}
func pageArgs(w http.ResponseWriter, r *http.Request, key string) (string, int, bool) {
	q := r.URL.Query()
	for k, v := range q {
		if (k != "limit" && k != key) || len(v) != 1 {
			fail(w, 400, "invalid_query")
			return "", 0, false
		}
	}
	n := 50
	var err error
	if q.Has("limit") {
		n, err = strconv.Atoi(q.Get("limit"))
	}
	if err != nil || n < 1 || n > 200 || len(q.Get(key)) > 256 {
		fail(w, 400, "invalid_query")
		return "", 0, false
	}
	return q.Get(key), n, true
}
func (s *Server) connected() bool {
	return s.config.GatewayConnected != nil && s.config.GatewayConnected()
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.secure(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.config.RequestTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	path := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(path) == 1 {
		switch path[0] {
		case "healthz", "readyz":
			if r.Method != "GET" {
				fail(w, 405, "method_not_allowed")
				return
			}
			dbOK := s.repo != nil && s.repo.Ping(ctx) == nil
			connected := s.connected()
			code := 200
			if path[0] == "readyz" && (!dbOK || !connected) {
				code = 503
			}
			status := "unavailable"
			if connected {
				status = "connected"
			}
			respond(w, code, map[string]any{"alive": true, "storageReady": dbOK, "acceptingCommands": dbOK, "dispatcher": status})
			return
		case "models":
			if r.Method != "GET" {
				fail(w, 405, "method_not_allowed")
				return
			}
			models := []any{}
			for _, id := range []string{"qwen3.5:9b", "gemma4:e4b"} {
				models = append(models, map[string]any{"id": id, "default": id == "qwen3.5:9b", "availability": "unknown", "capabilities": map[string]any{"text": "unverified", "tools": "unverified", "images": "unverified", "imageAdmissionQualified": false}})
			}
			respond(w, 200, map[string]any{"models": models})
			return
		case "conversations":
			s.conversations(w, r)
			return
		}
	}
	if len(path) >= 2 && path[0] == "conversations" {
		id := chat.ConversationID(path[1])
		if !chat.ValidID(string(id)) {
			fail(w, 400, "invalid_request")
			return
		}
		if len(path) >= 3 && (path[2] == "attachments" || path[2] == "events") {
			fail(w, 501, "not_implemented")
			return
		}
		if len(path) == 2 && r.Method == "GET" {
			s.metadata(w, r, id)
			return
		}
		if len(path) == 3 && path[2] == "messages" && r.Method == "GET" {
			s.history(w, r, id)
			return
		}
		if len(path) == 4 && path[2] == "commands" && r.Method == "GET" {
			if s.repo == nil {
				fail(w, 503, "storage_unavailable")
				return
			}
			a, e := s.repo.GetCommand(ctx, id, chat.CommandID(path[3]))
			if e != nil {
				storageError(w, e)
				return
			}
			respond(w, 200, receiptJSON(a))
			return
		}
		if r.Method == "POST" {
			s.command(w, r, id, path[2:])
			return
		}
	}
	fail(w, 404, "not_found")
}
