//go:build (darwin || linux) && race

package integration

import "testing"

// In addition to deterministic synthetic reports, exercise the actual Go race
// runtime in a grandchild. The intentional race never runs in this test process.
func TestProcessGateRealRace(t *testing.T) {
	for _, mode := range []string{"realrace-normal", "realrace-kill"} {
		t.Run(mode, func(t *testing.T) { checkProcessGate(t, mode) })
	}
}
