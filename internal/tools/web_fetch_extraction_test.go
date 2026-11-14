package tools

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

// Observe real cooperative checkpoints, without a parser goroutine or test-only
// hook in production. Err is deliberately held to prove the caller joins it.
type extractionCheckpoint struct {
	context.Context
	calls            atomic.Int32
	at               int32
	entered, release chan struct{}
}

func (c *extractionCheckpoint) Err() error {
	if c.calls.Add(1) == c.at {
		close(c.entered)
		<-c.release
	}
	return c.Context.Err()
}

func TestFetchHTMLNormalizationPreservesHiddenContentAndTruncation(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		truncated        bool
	}{
		{"hidden_style", `<p> Before <b>café</b> </p><div style="color:red;` + strings.Repeat(" \t", 50000) + `DISPLAY : none">secret</div><p> After </p>`, "Before café\nAfter", false},
		{"bounded_unicode", `<p>` + strings.Repeat("界", 6000) + `</p>`, strings.Repeat("界", 5333), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := wireFetcher(t, publicDNS, func(_ int, _ *http.Request, c net.Conn) { response(c, 200, "Content-Type: text/html\r\n", tc.body) })
			result, err := f.Fetch(context.Background(), FetchArgs{URL: "http://source.example/"})
			require.NoError(t, err)
			require.Equal(t, tc.want, result.Text)
			require.Equal(t, tc.truncated, result.Truncated)
		})
	}
}

func TestFetchHTMLCancellationJoinsBufferedPhases(t *testing.T) {
	shallow := "<!doctype html><html><body>" + strings.Repeat("<p>Evidence &amp; detail</p>", 20000) + "</body></html>"
	// Accepted large, shallow trees, not the independent nesting-limit guard.
	root, err := html.Parse(strings.NewReader("<div>" + strings.Repeat("<span></span>", 20000) + "Visible</div>"))
	require.NoError(t, err)
	whitespace := strings.Repeat(" \t\n", 100000) + "Visible"
	largeText, err := html.Parse(strings.NewReader("<p>" + strings.Repeat("e", 500000) + "</p>"))
	require.NoError(t, err)
	require.NotNil(t, root)
	phases := []struct {
		name string
		at   int32
		run  func(context.Context) error
	}{
		{"parse", 2, func(ctx context.Context) error { _, err := parseFetchHTML(ctx, shallow); return err }},
		{"traverse", 64, func(ctx context.Context) error { _, err := extractFetchHTML(ctx, root); return err }},
		{"normalize", 2, func(ctx context.Context) error { _, err := normalizeFetchHTML(ctx, whitespace); return err }},
		{"text_copy", 16, func(ctx context.Context) error { _, err := extractFetchHTML(ctx, largeText); return err }},
		{"style", 2, func(ctx context.Context) error {
			_, err := hiddenFetchStyle(ctx, whitespace+"display:none")
			return err
		}},
	}
	for _, phase := range phases {
		require.NoError(t, phase.run(context.Background()), phase.name+" positive control must be accepted")
		for _, expire := range []bool{false, true} {
			mode := "cancel"
			if expire {
				mode = "deadline"
			}
			t.Run(phase.name+"/"+mode, func(t *testing.T) {
				var base context.Context
				var cancel context.CancelFunc
				if expire {
					base, cancel = context.WithTimeout(context.Background(), 250*time.Millisecond)
				} else {
					base, cancel = context.WithCancel(context.Background())
				}
				defer cancel()
				checkpoint := &extractionCheckpoint{Context: base, at: phase.at, entered: make(chan struct{}), release: make(chan struct{})}
				unblock := sync.OnceFunc(func() { close(checkpoint.release) })
				done := make(chan error, 1)
				go func() { done <- phase.run(checkpoint) }()
				defer unblock()
				select {
				case <-checkpoint.entered:
				case err := <-done:
					t.Fatalf("%s completed without an in-phase cancellation checkpoint: %v", phase.name, err)
				case <-time.After(2 * time.Second):
					t.Fatal("extraction never reached its checkpoint")
				}
				if expire {
					<-base.Done()
				} else {
					cancel()
				}
				// Cancellation cannot return around still-owned work, even while that
				// work is deliberately stalled at a context checkpoint.
				select {
				case err := <-done:
					t.Fatalf("returned before held extraction joined: %v", err)
				case <-time.After(20 * time.Millisecond):
				}
				start := time.Now()
				unblock()
				select {
				case err := <-done:
					require.ErrorIs(t, err, base.Err())
				case <-time.After(time.Second):
					t.Fatal("extraction did not stop promptly at released checkpoint")
				}
				require.Less(t, time.Since(start), time.Second)
			})
		}
	}
}

// Cancellation becomes visible immediately after the pre-extraction body check.
// This catches the original public response path, not only the phase helpers.
type cancelAfterBodyCheck struct {
	context.Context
	cancel context.CancelFunc
	once   sync.Once
}

func (c *cancelAfterBodyCheck) Err() error {
	err := c.Context.Err()
	c.once.Do(c.cancel)
	return err
}
func TestFetchHTMLResponseDoesNotPublishAfterExtractionCancellation(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &cancelAfterBodyCheck{Context: base, cancel: cancel}
	body := "<html><body>" + strings.Repeat("<p>Accepted shallow evidence</p>", 20000) + "</body></html>"
	response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader(body))}
	result, err := readFetchResponse(ctx, response)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, result.Text)
}
