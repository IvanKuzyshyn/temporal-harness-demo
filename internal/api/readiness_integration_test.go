//go:build integration

package api_test

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/api"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReadinessRequiresReconciliationAndWorkers(t *testing.T) {
	r, _ := database(t)
	transport, workers, reconcile := true, false, true
	h, err := api.NewServer(r, api.Config{ListenAddress: "127.0.0.1:18081", ProxySecret: secret, BrowserOrigin: "http://localhost:3000", GatewayConnected: func() bool { return transport }, WorkersPolling: func() bool { return workers }, ReconciliationReady: func() bool { return reconcile }})
	require.NoError(t, err)
	require.Equal(t, 503, request(h, "GET", "/readyz", "", nil).Code)
	workers = true
	require.Equal(t, 200, request(h, "GET", "/readyz", "", nil).Code)
	reconcile = false
	require.Equal(t, 503, request(h, "GET", "/readyz", "", nil).Code)
	reconcile = true
	transport = false
	require.Equal(t, 503, request(h, "GET", "/readyz", "", nil).Code)
}
