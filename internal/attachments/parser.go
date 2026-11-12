package attachments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Build with: docker build -t webchat-attachment-parser:1 infra/attachment-parser
// No caller/model controls the image, command, mounts or container options.
const ParserImage = "webchat-attachment-parser:1"

var ErrCleanupUnconfirmed = errors.New("parser cleanup unconfirmed")
var ErrPersistence = errors.New("attachment persistence unavailable")

type ContainerParser struct{}
type boundedBuffer struct {
	bytes.Buffer
	max int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, errors.New("parser output limit")
	}
	return b.Buffer.Write(p)
}
func (ContainerParser) Parse(parent context.Context, b []byte, media string, extract bool) (out Document, err error) {
	if len(b) > MaxBytes {
		return out, ErrTooLarge
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	if e := parent.Err(); e != nil {
		return out, e
	}
	name := "webchat-parser-" + uuid.NewString()
	// Join the create RPC before cleanup. Killing its CLI on caller cancellation
	// can race a still-running daemon create: rm sees no object, then it appears.
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		cmd := exec.CommandContext(cleanup, "docker", "rm", "-f", name)
		var stderr boundedBuffer
		stderr.max = 4096
		cmd.Stderr = &stderr
		if e := cmd.Run(); e != nil && !strings.Contains(stderr.String(), "No such container") {
			err = fmt.Errorf("%w: %v", ErrCleanupUnconfirmed, e)
		}
	}()
	createCtx, createCancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
	defer createCancel()
	create := exec.CommandContext(createCtx, "docker", "create", "--pull=never", "--name", name, "--label", "webchat.owner="+name, "--network", "none", "--read-only", "--memory", "512m", "--memory-swap", "512m", "--cpus", "1", "--pids-limit", "32", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--user", "65534:65534", "--ulimit", "nofile=64:64", "--ulimit", "cpu=8:8", "-i", ParserImage)
	var diag boundedBuffer
	diag.max = 8192
	create.Stderr = &diag
	if e := create.Run(); e != nil {
		if createCtx.Err() != nil {
			// The daemon's create outcome is unknown even if rm observes absence.
			return out, fmt.Errorf("%w: create outcome unknown", ErrCleanupUnconfirmed)
		}
		// A transport failure can also lose a successful daemon acknowledgment.
		return out, fmt.Errorf("%w: create failed (build %s): %v", ErrCleanupUnconfirmed, ParserImage, e)
	}
	if e := ctx.Err(); e != nil {
		return out, e
	}
	raw, _ := json.Marshal(struct {
		Data    []byte `json:"data"`
		Media   string `json:"media"`
		Extract bool   `json:"extract"`
	}{b, media, extract})
	cmd := exec.CommandContext(ctx, "docker", "start", "-ai", name)
	cmd.Stdin = bytes.NewReader(raw)
	output := &boundedBuffer{max: 2 * 1024 * 1024}
	cmd.Stdout = output
	cmd.Stderr = &diag
	if e := cmd.Run(); e != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, fmt.Errorf("bounded parser rejected input (%s): %w", strings.TrimSpace(diag.String()), ErrInvalid)
	}
	if e := json.Unmarshal(output.Bytes(), &out); e != nil {
		return Document{}, fmt.Errorf("parser protocol: %w", ErrInvalid)
	}
	if len([]rune(out.Text)) > 200000 || out.Pages < 0 || (out.Status != "completed" && out.Status != "unsupported") || (!extract && out.Text != "") {
		return Document{}, ErrInvalid
	}
	return out, ctx.Err()
}
