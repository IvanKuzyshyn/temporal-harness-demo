// qualify-chat-models is an opt-in live gate, not an application client.
// Run: go run ./scripts/qualify-chat-models.go -image scripts/testdata/bonjour.png
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
)

type chatRequest struct {
	Model    string    `json:"model"`
	Stream   bool      `json:"stream"`
	Think    bool      `json:"think"`
	Messages []message `json:"messages"`
	Tools    []any     `json:"tools,omitempty"`
	Options  struct {
		NumCtx      int     `json:"num_ctx"`
		NumPredict  int     `json:"num_predict"`
		Temperature float64 `json:"temperature"`
	} `json:"options"`
}
type message struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  [][]byte `json:"images,omitempty"`
}
type toolCall struct {
	Function struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"function"`
}
type chunk struct {
	Model   string `json:"model"`
	Message struct {
		Content   string     `json:"content"`
		Thinking  string     `json:"thinking"`
		ToolCalls []toolCall `json:"tool_calls"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	Error           string `json:"error"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
}

// Receipt evidence describes this client's transport observation, NOT when the
// provider generated tokens or stopped GPU work. Use a broad observation window
// rather than treating parser iterations or tiny scheduling gaps as streaming.
// Short/coalesced responses are inconclusive, not proof of a provider failure.
const receiptObservationWindow = 250 * time.Millisecond

type textReceipt struct {
	Read int   `json:"read"`
	AtMS int64 `json:"at_ms"`
}

type receiptReader struct {
	io.Reader
	start time.Time
	read  int
	at    time.Duration
}

func (r *receiptReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 {
		r.read++
		r.at = time.Since(r.start)
	}
	return n, err
}

type result struct {
	Model             string        `json:"model"`
	Kind              string        `json:"kind"`
	Error             string        `json:"error,omitempty"`
	Cancelled         bool          `json:"cancelled,omitempty"`
	Text              string        `json:"text,omitempty"`
	Tools             []toolCall    `json:"tools,omitempty"`
	Chunks            int           `json:"chunks"`
	TextChunks        int           `json:"text_chunks"`
	FirstChunkMS      int64         `json:"first_chunk_ms"`
	ElapsedMS         int64         `json:"elapsed_ms"`
	CancelMS          int64         `json:"cancel_ms,omitempty"`
	PromptTokens      int           `json:"prompt_tokens"`
	OutputTokens      int           `json:"output_tokens"`
	ContextLength     int           `json:"context_length,omitempty"`
	Delivery          string        `json:"delivery,omitempty"`
	ContentLength     int64         `json:"content_length"`
	TextReceipts      []textReceipt `json:"text_receipts,omitempty"`
	TextReceiptSpanMS int64         `json:"text_receipt_span_ms"`
	ImageSemantics    string        `json:"image_semantics,omitempty"`
}

func request(model, kind string, img []byte) chatRequest {
	r := chatRequest{Model: model, Stream: true, Messages: []message{{Role: "user", Content: "Say hello and then explain in two sentences why the sky looks blue."}}}
	r.Options.NumCtx = 16384
	r.Options.NumPredict = 2000
	switch kind {
	case "tools":
		r.Messages[0].Content = "Call web_fetch with url https://example.com/ now. Do not answer in prose."
		r.Tools = []any{map[string]any{"type": "function", "function": map[string]any{"name": "web_fetch", "description": "Fetch a public web page by URL", "parameters": map[string]any{"type": "object", "properties": map[string]any{"url": map[string]any{"type": "string", "enum": []string{"https://example.com/"}}}, "required": []string{"url"}, "additionalProperties": false}}}}
	case "context":
		r.Messages[0].Content = "The secret code is orchid-731. Remember it.\n" + strings.Repeat("x ", 11000) + "\nWhat is the secret code at the beginning? Return only that code."
	case "image", "image-control":
		r.Messages[0].Content = "Translate the text visible in this image into English. Return only the translation."
		if kind == "image" {
			r.Messages[0].Images = [][]byte{img}
		}
	case "cancel":
		r.Messages[0].Content = "Write a very long numbered list of 1000 different animals with a sentence about each. Keep going until all 1000 are listed."
	}
	return r
}

func post(ctx context.Context, client *http.Client, base string, body chatRequest) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/api/chat", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return client.Do(req)
}

func qualifyChat(parent context.Context, client *http.Client, base, model, kind string, img []byte) (out result) {
	out.Model = model
	out.Kind = kind
	out.Delivery = "inconclusive"
	start := time.Now()
	defer func() { out.ElapsedMS = time.Since(start).Milliseconds() }()
	if model != "qwen3.5:9b" && model != "gemma4:e4b" {
		out.Error = "model is not in the exact allowlist"
		return
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	resp, err := post(ctx, client, base, request(model, kind, img))
	if err != nil {
		out.Error = err.Error()
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		out.Error = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, b)
		return
	}
	out.ContentLength = resp.ContentLength
	receipts := &receiptReader{Reader: io.LimitReader(resp.Body, 4<<20), start: start}
	scanner := bufio.NewScanner(receipts)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	done := false
	var cancelAt time.Time
	var firstTextReceipt time.Duration
	for scanner.Scan() {
		var c chunk
		if err := json.Unmarshal(scanner.Bytes(), &c); err != nil {
			out.Error = "invalid stream: " + err.Error()
			return
		}
		if c.Error != "" {
			out.Error = "provider: " + c.Error
			return
		}
		if c.Model != model {
			out.Error = fmt.Sprintf("model routing mismatch: requested %q got %q", model, c.Model)
			return
		}
		out.Chunks++
		if out.Chunks == 1 {
			out.FirstChunkMS = time.Since(start).Milliseconds()
		}
		out.Text += c.Message.Content
		out.Tools = append(out.Tools, c.Message.ToolCalls...)
		if !c.Done && c.Message.Content != "" {
			out.TextChunks++
			if len(out.TextReceipts) == 0 {
				firstTextReceipt = receipts.at
			}
			if len(out.TextReceipts) == 0 || out.TextReceipts[len(out.TextReceipts)-1].Read != receipts.read {
				out.TextReceipts = append(out.TextReceipts, textReceipt{Read: receipts.read, AtMS: receipts.at.Milliseconds()})
				out.TextReceiptSpanMS = (receipts.at - firstTextReceipt).Milliseconds()
				// Known-length bodies and quickly coalesced records are deliberately
				// inconclusive. Even positive evidence means incremental RECEIPT only.
				if resp.ContentLength < 0 && len(out.TextReceipts) >= 2 && receipts.at-firstTextReceipt >= receiptObservationWindow {
					out.Delivery = "incremental-receipt-observed"
				}
			}
		}
		if c.Done {
			done = true
			out.PromptTokens = c.PromptEvalCount
			out.OutputTokens = c.EvalCount
			if c.DoneReason == "length" {
				out.Error = "output was truncated at token cap"
			}
			break
		}
		if kind == "cancel" && cancelAt.IsZero() && (c.Message.Content != "" || c.Message.Thinking != "") {
			cancelAt = time.Now()
			cancel()
		}
	}
	if kind == "cancel" {
		if !cancelAt.IsZero() {
			out.CancelMS = time.Since(cancelAt).Milliseconds()
		}
		if cancelAt.IsZero() || done || !errors.Is(scanner.Err(), context.Canceled) || out.CancelMS > 2000 {
			out.Error = fmt.Sprintf("active stream cancellation not confirmed: done=%v err=%v", done, scanner.Err())
			return
		}
		out.Cancelled = true
		return
	}
	if err := scanner.Err(); err != nil {
		out.Error = err.Error()
		return
	}
	if !done {
		out.Error = "stream ended without done"
		return
	}
	if out.Error != "" {
		return
	}
	switch kind {
	case "context":
		if !strings.Contains(out.Text, "orchid-731") || out.PromptTokens < 10000 || out.PromptTokens > 12000 {
			out.Error = "long-context probe lost early marker or reported input outside 10000..12000 tokens"
		}
	case "text":
		if out.TextChunks < 2 || strings.TrimSpace(out.Text) == "" {
			out.Error = "expected at least two non-final text chunks"
		} else if out.Delivery != "incremental-receipt-observed" {
			out.Error = "streaming inconclusive: protocol records alone do not establish incremental receipt"
		}
	case "tools":
		if len(out.Tools) != 1 {
			out.Error = "expected exactly one tool call"
			return
		}
		f := out.Tools[0].Function
		if f.Name != "web_fetch" || len(f.Arguments) != 1 || f.Arguments["url"] != "https://example.com/" {
			out.Error = "tool call violates fixture schema"
		}
	case "image":
		out.ImageSemantics = "review-required"
		if fixtureTranslationAccepted(out.Text) {
			out.ImageSemantics = "accepted"
		} else {
			out.Error = "image translation not auto-certified: explicit review required (wrong, refused, or ambiguous fixture answer)"
		}
		// A short translation is a semantic-only probe. The longer text probe
		// independently gates incremental receipt for each exact model.
	}
	return
}

// This is intentionally a closed acceptance set for BONJOUR LE MONDE, not a
// general semantic classifier. Preserve all words: removing refusal/uncertainty
// words or merely finding keywords would turn guesses into false positives.
// Legitimate answers outside this small set require explicit human review.
func fixtureTranslationAccepted(text string) bool {
	if strings.Contains(text, "...") || strings.ContainsRune(text, '…') {
		return false
	}
	normalized := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || strings.ContainsRune(",.!\"'“”‘’:", r) {
			return ' '
		}
		return unicode.ToLower(r)
	}, text)
	normalized = strings.Join(strings.Fields(normalized), " ")
	for _, prefix := range []string{"english translation ", "translation "} {
		if strings.HasPrefix(normalized, prefix) {
			normalized = strings.TrimPrefix(normalized, prefix)
			break
		}
	}
	return normalized == "hello world" || normalized == "hello to the world"
}

func qualifyUnavailable(ctx context.Context, client *http.Client, base string) (out result) {
	out.Model = "webchat-qualification-missing:never"
	out.Kind = "unavailable"
	resp, err := post(ctx, client, base, request(out.Model, "text", nil))
	if err != nil {
		out.Error = err.Error()
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) != nil || resp.StatusCode != 404 || !strings.Contains(strings.ToLower(e.Error), "not found") {
		out.Error = fmt.Sprintf("expected explicit model-not-found HTTP 404, got %d: %s", resp.StatusCode, b)
	}
	return
}

func contextSize(ctx context.Context, client *http.Client, base, model string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/ps", nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("context metadata HTTP %d", resp.StatusCode)
	}
	var body struct {
		Models []struct {
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return 0, err
	}
	for _, m := range body.Models {
		if m.Name == model && m.ContextLength == 16384 {
			return m.ContextLength, nil
		}
	}
	return 0, fmt.Errorf("%s is not running with the requested 16384-token context", model)
}

func main() {
	base := flag.String("url", "http://127.0.0.1:11434", "Ollama-compatible endpoint")
	imagePath := flag.String("image", "scripts/testdata/bonjour.png", "PNG/JPEG with BONJOUR LE MONDE")
	timeout := flag.Duration("timeout", 5*time.Minute, "deadline per probe")
	model := flag.String("model", "", "one exact model, or empty for both")
	extended := flag.Bool("extended", false, "also probe long context, active 16K configuration, and image-free token baseline")
	flag.Parse()
	img, err := os.ReadFile(*imagePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(img))
	if err != nil || len(img) > 1048576 || cfg.Width > 2048 || cfg.Height > 2048 || cfg.Width < 1 || cfg.Height < 1 {
		fmt.Fprintln(os.Stderr, "fixture must be a valid <=1MiB PNG/JPEG, <=2048 per dimension")
		os.Exit(2)
	}
	models := []string{"qwen3.5:9b", "gemma4:e4b"}
	if *model != "" {
		models = []string{*model}
	}
	client := &http.Client{Timeout: *timeout}
	enc := json.NewEncoder(os.Stdout)
	failed := false
	kinds := []string{"text", "tools", "image", "cancel"}
	if *extended {
		kinds = []string{"text", "tools", "context", "image-control", "image", "cancel"}
	}
	for _, m := range models {
		for _, kind := range kinds {
			ctx, cancel := context.WithTimeout(context.Background(), *timeout)
			r := qualifyChat(ctx, client, *base, m, kind, img)
			if kind == "context" && r.Error == "" {
				size, err := contextSize(ctx, client, *base, m)
				r.ContextLength = size
				if err != nil {
					r.Error = err.Error()
				}
			}
			cancel()
			if r.Error != "" {
				failed = true
			}
			enc.Encode(r)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	r := qualifyUnavailable(ctx, client, *base)
	enc.Encode(r)
	if r.Error != "" {
		failed = true
	}
	if failed {
		os.Exit(1)
	}
}
