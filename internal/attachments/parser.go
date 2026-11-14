package attachments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Only trusted setup configuration may supply a full local content ID. No
// request/model controls the image, command, mounts or container options.
var parserImageID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func validParserImage(id string) error {
	if !parserImageID.MatchString(id) {
		return errors.New("WEBCHAT_PARSER_IMAGE_ID must be a full sha256 image ID from trusted setup; run mise run setup")
	}
	return nil
}

// NewContainerParser captures the setup-owned immutable ID once. Tags, registry
// references and truncated IDs are forbidden. Inspection never pulls an image.
func NewContainerParser(imageID string) (ContainerParser, error) {
	if err := validParserImage(imageID); err != nil {
		return ContainerParser{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", imageID)
	out := &boundedBuffer{max: 4096}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil || strings.TrimSpace(out.String()) != imageID {
		return ContainerParser{}, errors.New("WEBCHAT_PARSER_IMAGE_ID unavailable or identity mismatch; restore the trusted setup image or run mise run setup (no fallback)")
	}
	return ContainerParser{imageID: imageID}, nil
}

var ErrCleanupUnconfirmed = errors.New("parser cleanup unconfirmed")
var ErrPersistence = errors.New("attachment persistence unavailable")

type ContainerParser struct{ imageID string }
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

type parserOutput struct {
	Document
	Raster []byte `json:"raster,omitempty"`
}

func (p ContainerParser) Parse(parent context.Context, b []byte, media string, extract bool) (Document, error) {
	out, err := p.run(parent, b, media, extract, false)
	return out.Document, err
}
func (p ContainerParser) run(parent context.Context, b []byte, media string, extract, render bool) (out parserOutput, err error) {
	if err := validParserImage(p.imageID); err != nil {
		return out, err
	}
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
	create := exec.CommandContext(createCtx, "docker", "create", "--pull=never", "--name", name, "--label", "webchat.owner="+name, "--network", "none", "--read-only", "--memory", "512m", "--memory-swap", "512m", "--cpus", "1", "--pids-limit", "32", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--user", "65534:65534", "--ulimit", "nofile=64:64", "--ulimit", "cpu=8:8", "-i", p.imageID)
	var diag boundedBuffer
	diag.max = 8192
	create.Stderr = &diag
	if e := create.Run(); e != nil {
		if createCtx.Err() != nil {
			// The daemon's create outcome is unknown even if rm observes absence.
			return out, fmt.Errorf("%w: create outcome unknown", ErrCleanupUnconfirmed)
		}
		// A transport failure can also lose a successful daemon acknowledgment.
		return out, fmt.Errorf("%w: create failed (build %s): %v", ErrCleanupUnconfirmed, p.imageID, e)
	}
	if e := ctx.Err(); e != nil {
		return out, e
	}
	raw, _ := json.Marshal(struct {
		Data    []byte `json:"data"`
		Media   string `json:"media"`
		Extract bool   `json:"extract"`
		Render  bool   `json:"render"`
	}{b, media, extract, render})
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
		return parserOutput{}, fmt.Errorf("parser protocol: %w", ErrInvalid)
	}
	if len([]rune(out.Text)) > 200000 || out.Pages < 0 || (out.Status != "completed" && out.Status != "unsupported") || (!extract && out.Text != "") {
		return parserOutput{}, ErrInvalid
	}
	if (!render && len(out.Raster) > 0) || len(out.Raster) > MaxBytes {
		return parserOutput{}, ErrInvalid
	}
	return out, ctx.Err()
}
