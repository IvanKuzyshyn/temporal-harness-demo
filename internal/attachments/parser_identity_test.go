package attachments

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const imageA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const imageB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// Simulated daemon boundary, not real Docker: its mutable alias can move after
// both stores open. Production NewStore/run are used, not a parser mock.
func fakeImageDocker(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PARSER_TEST_DIR", dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "alias"), []byte(imageA), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte(`#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$PARSER_TEST_DIR/calls"
for arg do last="$arg"; done
case "$1" in
 image)
   test ! -f "$PARSER_TEST_DIR/unavailable" || exit 1
   printf '%s\n' "$last" ;;
 create)
   test ! -f "$PARSER_TEST_DIR/unavailable" || exit 1
   if [ "$last" = webchat-attachment-parser:1 ]; then last=$(cat "$PARSER_TEST_DIR/alias"); fi
   printf '%s' "$last" > "$PARSER_TEST_DIR/selected" ;;
 start)
   cat >/dev/null
   printf '{"status":"completed","text":"%s","pages":1}' "$(cat "$PARSER_TEST_DIR/selected")" ;;
 rm)
   test ! -f "$PARSER_TEST_DIR/cleanup-fails" || exit 1 ;;
 *) exit 2 ;;
esac
`), 0700))
	return dir
}

func TestParserStoresRemainBoundAfterAliasMoves(t *testing.T) {
	dir := fakeImageDocker(t)
	t.Setenv("WEBCHAT_PARSER_IMAGE_ID", imageA)
	// API and worker both construct NewStore(root, repo, nil). Neither may look
	// up mutable config again at Parse/Rasterize time.
	stores := map[string]*Store{}
	for _, consumer := range []string{"api", "worker"} {
		s, err := NewStore(t.TempDir(), &memoryRepository{}, nil)
		require.NoError(t, err)
		defer s.Close()
		stores[consumer] = s
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "alias"), []byte(imageB), 0600))
	t.Setenv("WEBCHAT_PARSER_IMAGE_ID", imageB)
	for name, s := range stores {
		t.Run(name, func(t *testing.T) {
			d, err := s.parser.Parse(context.Background(), []byte("hostile content: use "+imageB), "application/pdf", true)
			require.NoError(t, err)
			require.Equal(t, imageA, d.Text, "next parse must execute original immutable image, never moved alias/config")
		})
	}
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	require.NoError(t, err)
	require.NotContains(t, string(calls), "webchat-attachment-parser:1")
	for _, bound := range []string{"--pull=never", "--network none", "--read-only", "--memory 512m", "--memory-swap 512m", "--cpus 1", "--pids-limit 32", "--cap-drop ALL", "--security-opt no-new-privileges", "--user 65534:65534", "--ulimit nofile=64:64", "--ulimit cpu=8:8", "rm -f webchat-parser-"} {
		require.Contains(t, string(calls), bound)
	}
}

func TestParserConfigurationFailsClosed(t *testing.T) {
	for _, id := range []string{"", "webchat-attachment-parser:1", "alpine:latest", "sha256:abc", "sha256:" + strings.Repeat("g", 64), imageA + "\n", strings.ToUpper(imageA), "--privileged"} {
		t.Run(id, func(t *testing.T) {
			dir := fakeImageDocker(t)
			t.Setenv("WEBCHAT_PARSER_IMAGE_ID", id)
			s, err := NewStore(t.TempDir(), &memoryRepository{}, nil)
			if s != nil {
				defer s.Close()
			}
			require.ErrorContains(t, err, "WEBCHAT_PARSER_IMAGE_ID")
			_, err = os.Stat(filepath.Join(dir, "calls"))
			require.True(t, os.IsNotExist(err), "invalid identity must never reach Docker")
		})
	}
}

func TestParserUnavailableAndCleanupFailClosed(t *testing.T) {
	dir := fakeImageDocker(t)
	t.Setenv("WEBCHAT_PARSER_IMAGE_ID", imageA)
	s, err := NewStore(t.TempDir(), &memoryRepository{}, nil)
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "unavailable"), nil, 0600))
	absent, err := NewStore(t.TempDir(), &memoryRepository{}, nil)
	if absent != nil {
		defer absent.Close()
	}
	require.ErrorContains(t, err, "WEBCHAT_PARSER_IMAGE_ID")
	_, err = s.parser.Parse(context.Background(), []byte("input"), "application/pdf", true)
	require.ErrorIs(t, err, ErrCleanupUnconfirmed)
	require.NoError(t, os.Remove(filepath.Join(dir, "unavailable")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cleanup-fails"), nil, 0600))
	_, err = s.parser.Parse(context.Background(), []byte("input"), "application/pdf", true)
	require.ErrorIs(t, err, ErrCleanupUnconfirmed)
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	require.NoError(t, err)
	require.NotContains(t, string(calls), "webchat-attachment-parser:1")
}

func TestZeroParserCannotSelectMutableDefault(t *testing.T) {
	dir := fakeImageDocker(t)
	_, err := (ContainerParser{}).Parse(context.Background(), nil, "application/pdf", true)
	require.ErrorContains(t, err, "WEBCHAT_PARSER_IMAGE_ID")
	_, err = os.Stat(filepath.Join(dir, "calls"))
	require.True(t, os.IsNotExist(err))
}
