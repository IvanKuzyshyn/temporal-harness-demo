package api

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
)

// Exactly one original per upload; the admitted message independently caps four
// owned ready IDs. This reader verifies the multipart tail before Store commits.
type singleFileReader struct {
	part      *multipart.Part
	multipart *multipart.Reader
}

func (r *singleFileReader) Read(p []byte) (int, error) {
	n, e := r.part.Read(p)
	if e == io.EOF {
		_, tail := r.multipart.NextPart()
		if tail != io.EOF {
			return n, attachments.ErrInvalid
		}
	}
	return n, e
}
func (s *Server) attachment(w http.ResponseWriter, r *http.Request, c chat.ConversationID, path []string) {
	store := s.config.Attachments
	if store == nil || s.repo == nil {
		fail(w, 503, "attachment_store_unavailable")
		return
	}
	if len(path) == 0 && r.Method == "POST" {
		r.Body = http.MaxBytesReader(w, r.Body, attachments.MaxBytes+16384)
		reader, e := r.MultipartReader()
		if e != nil {
			fail(w, 415, "multipart_required")
			return
		}
		part, e := reader.NextPart()
		if e != nil || part.FormName() != "file" || part.FileName() == "" {
			fail(w, 400, "one_file_required")
			return
		}
		defer part.Close()
		name := part.FileName()
		media := part.Header.Get("Content-Type")
		if media == "" || media == "application/octet-stream" {
			media = map[string]string{".txt": "text/plain", ".md": "text/markdown", ".markdown": "text/markdown", ".pdf": "application/pdf", ".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".svg": "image/svg+xml"}[strings.ToLower(filepath.Ext(name))]
		}
		a, e := store.Put(r.Context(), c, &singleFileReader{part, reader}, attachments.UploadMetadata{Name: name, MediaType: media})
		if e != nil {
			var max *http.MaxBytesError
			switch {
			case errors.Is(e, attachments.ErrTooLarge), errors.As(e, &max):
				fail(w, 413, "file_exceeds_1048576_bytes")
			case errors.Is(e, attachments.ErrInvalid):
				fail(w, 422, "invalid_signature_or_unreadable_file")
			default:
				storageError(w, e)
			}
			return
		}
		respond(w, 201, map[string]any{"attachment": map[string]any{"id": a.ID, "conversationId": a.ConversationID, "name": a.OriginalName, "mediaType": a.MediaType, "size": a.Size, "checksum": a.Checksum, "status": a.Status}})
		return
	}
	if len(path) == 1 && r.Method == "GET" {
		id := chat.AttachmentID(path[0])
		if !chat.ValidID(string(id)) {
			fail(w, 400, "invalid_request")
			return
		}
		a, e := s.repo.GetAttachment(r.Context(), c, id)
		if e != nil {
			storageError(w, e)
			return
		}
		f, e := store.Open(r.Context(), c, id)
		if e != nil {
			storageError(w, e)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", a.MediaType)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": a.OriginalName}))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Length", strconv.FormatInt(a.Size, 10))
		w.WriteHeader(200)
		_, _ = io.Copy(w, f)
		return
	}
	fail(w, 405, "method_not_allowed")
}
