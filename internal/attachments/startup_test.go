package attachments

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConcurrentStartupWaitsForLayoutPublication(t *testing.T) {
	s, r, _ := fixtureStore(t)
	unlock, err := s.lock(context.Background())
	require.NoError(t, err)
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	// Simulate the first constructor paused between O_EXCL and marker fsync.
	require.NoError(t, s.root.Remove(".webchat-attachments-v1"))
	f, err := s.root.Create(".webchat-attachments-v1")
	require.NoError(t, err)
	defer f.Close()
	done := make(chan error, 1)
	go func() {
		other, e := NewStore(r.root, r, nil)
		if other != nil {
			other.Close()
		}
		done <- e
	}()
	select {
	case e := <-done:
		t.Fatalf("concurrent opener observed unpublished layout: %v", e)
	case <-time.After(50 * time.Millisecond):
	}
	_, err = f.WriteString("webchat-attachments-v1\n")
	require.NoError(t, err)
	require.NoError(t, f.Sync())
	require.NoError(t, f.Close())
	unlock()
	unlock = nil
	select {
	case e := <-done:
		require.NoError(t, e)
	case <-time.After(3 * time.Second):
		t.Fatal("constructor failed to join")
	}
}
