package main

import (
	"bytes"
	"encoding/json"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Invokes the exact preparation helper used by the opt-in live experiment,
// never TestTask9LiveSmallImageExperiments or qualifyChat (zero model requests).
func TestTask9ImagePreparation(t *testing.T) {
	var calls string
	if os.Getenv("WEBCHAT_TEST_PARSERS") != "1" {
		// Only the Docker boundary is simulated in the default suite. The PNG/JPEG/
		// static-SVG preparation and production parser constructor/run are real.
		dir := t.TempDir()
		imageID := "sha256:" + strings.Repeat("a", 64)
		t.Setenv("WEBCHAT_PARSER_IMAGE_ID", imageID)
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("TASK9_PREP_DIR", dir)
		png, err := os.ReadFile("testdata/bonjour.png")
		require.NoError(t, err)
		output, err := json.Marshal(map[string]any{"status": "completed", "raster": png})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "response"), output, 0600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte(`#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$TASK9_PREP_DIR/calls"
for arg do last="$arg"; done
case "$1" in
 image) printf '%s\n' "$last" ;;
 create) test "$last" = "$WEBCHAT_PARSER_IMAGE_ID" ;;
 start) cat > "$TASK9_PREP_DIR/request"; cat "$TASK9_PREP_DIR/response" ;;
 rm) : ;;
 *) exit 2 ;;
esac
`), 0700))
		calls = filepath.Join(dir, "calls")
		t.Cleanup(func() {
			if t.Failed() {
				return
			}
			b, err := os.ReadFile(calls)
			require.NoError(t, err)
			require.Contains(t, string(b), "image inspect --format {{.Id}} "+imageID)
			require.Contains(t, string(b), "-i "+imageID)
			require.NotContains(t, string(b), "webchat-attachment-parser:1")
			require.Equal(t, 1, strings.Count(string(b), "create --pull=never"))
			require.Contains(t, string(b), "rm -f webchat-parser-")
			b, err = os.ReadFile(filepath.Join(dir, "request"))
			require.NoError(t, err)
			var request struct {
				Data            []byte
				Media           string
				Extract, Render bool
			}
			require.NoError(t, json.Unmarshal(b, &request))
			require.Equal(t, "image/svg+xml", request.Media)
			require.False(t, request.Extract)
			require.True(t, request.Render)
			require.LessOrEqual(t, len(request.Data), 1048576)
			require.Contains(t, string(request.Data), `width="640" height="160"`)
		})
	} else {
		t.Log("actual explicitly configured immutable Docker helper; zero inference")
	}
	fixtures := task9ImageFixtures(t)
	require.Len(t, fixtures, 3)
	for i, want := range []string{"png", "jpeg", "safe-svg-raster"} {
		require.Equal(t, want, fixtures[i].name)
		require.NotEmpty(t, fixtures[i].data)
		require.LessOrEqual(t, len(fixtures[i].data), 1048576)
		img, _, err := image.Decode(bytes.NewReader(fixtures[i].data))
		require.NoError(t, err)
		require.Equal(t, image.Rect(0, 0, 640, 160), img.Bounds())
	}
}
