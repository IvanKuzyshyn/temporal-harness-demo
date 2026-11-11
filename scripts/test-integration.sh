#!/usr/bin/env bash
# Explicitly opt in to a real, private Temporal server and the mock LLM.
# Tools must already be installed: resolution never installs dependencies.
set -euo pipefail
cd "$(dirname "$0")/.."
export HARNESS_INTEGRATION=1 HARNESS_MODEL=mock
# No caller-supplied server is used. Every run allocates its own loopback ports,
# SQLite DB, workers, generated project, copied sample, and data directory.
exec go test -count=1 -p=1 -parallel=1 -timeout=6m "$@" -v ./internal/integration
