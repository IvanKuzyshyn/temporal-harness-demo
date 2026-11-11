package dispatch_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/dispatch"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/stretchr/testify/require"
)

type unavailableStorage struct{}

func (unavailableStorage) ClaimCommands(ctx context.Context, _ int, _ time.Duration) ([]storage.CommandClaim, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(200 * time.Millisecond):
		return nil, errors.New("unbounded storage call")
	}
}
func (unavailableStorage) SettleClaim(context.Context, storage.CommandClaim, string, []byte, time.Duration) error {
	return nil
}

type unusedGateway struct{}

func (unusedGateway) Submit(context.Context, dispatch.Command) (dispatch.CommandResult, error) {
	return dispatch.CommandResult{}, errors.New("must not submit without claim")
}
func TestDispatcherRejectsOverflowingDeadline(t *testing.T) {
	_, err := dispatch.New(unavailableStorage{}, unusedGateway{}, dispatch.Options{StorageTimeout: time.Duration(1 << 62)})
	require.Error(t, err, "overflow must not bypass the lease budget")
}

func TestDispatcherStorageDeadline(t *testing.T) {
	d, e := dispatch.New(unavailableStorage{}, unusedGateway{}, dispatch.Options{StorageTimeout: 10 * time.Millisecond})
	require.NoError(t, e)
	worked, e := d.RunOnce(context.Background())
	require.False(t, worked)
	require.ErrorIs(t, e, context.DeadlineExceeded)
}
