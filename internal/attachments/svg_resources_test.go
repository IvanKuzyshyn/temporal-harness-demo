package attachments

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Observe the actual daemon-created container before starting the pinned helper.
// This wrapper forwards every production argument unchanged; no parser, Docker
// response, resource boundary or image is mocked.
func TestSVGContainerResourceBoundary(t *testing.T) {
	p := realParser(t)
	docker, e := exec.LookPath("docker")
	require.NoError(t, e)
	dir := t.TempDir()
	evidence := filepath.Join(dir, "inspect.json")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = create ]; then\n %q \"$@\" || exit $?\n %q inspect \"$4\" > %q\nelse\n exec %q \"$@\"\nfi\n", docker, docker, evidence, docker)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, e = p.Rasterize(context.Background(), []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"><rect width="16" height="16"/></svg>`))
	require.NoError(t, e)
	raw, e := os.ReadFile(evidence)
	require.NoError(t, e)
	var inspect []struct {
		Config     struct{ User string }
		HostConfig struct {
			NetworkMode                             string
			ReadonlyRootfs                          bool
			Memory, MemorySwap, NanoCpus, PidsLimit int64
			CapDrop, SecurityOpt                    []string
			Binds                                   []string
			Ulimits                                 []struct {
				Name       string
				Soft, Hard int64
			}
		}
	}
	require.NoError(t, json.Unmarshal(raw, &inspect))
	require.Len(t, inspect, 1)
	v := inspect[0]
	require.Equal(t, "65534:65534", v.Config.User)
	require.Equal(t, "none", v.HostConfig.NetworkMode)
	require.True(t, v.HostConfig.ReadonlyRootfs)
	require.EqualValues(t, 512*1024*1024, v.HostConfig.Memory)
	require.Equal(t, v.HostConfig.Memory, v.HostConfig.MemorySwap)
	require.EqualValues(t, 1000000000, v.HostConfig.NanoCpus)
	require.EqualValues(t, 32, v.HostConfig.PidsLimit)
	require.Contains(t, v.HostConfig.CapDrop, "ALL")
	require.Contains(t, v.HostConfig.SecurityOpt, "no-new-privileges")
	require.Empty(t, v.HostConfig.Binds)
	cpu := false
	for _, u := range v.HostConfig.Ulimits {
		if u.Name == "cpu" {
			cpu = true
			require.EqualValues(t, 8, u.Soft)
			require.EqualValues(t, 8, u.Hard)
		}
	}
	require.True(t, cpu)
	t.Log("actual Docker inspect: network none, read-only/nonroot/no mounts, memory+swap512MiB, CPU1/8s, pids32, cap-drop ALL, no-new-privileges; pinned helper rendered PNG and container removed")
}
