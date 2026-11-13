#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
command -v mise >/dev/null || { echo 'Install mise first: https://mise.jdx.dev/' >&2; exit 1; }
command -v docker >/dev/null || { echo 'Install Docker with Compose and start its daemon first.' >&2; exit 1; }
docker info >/dev/null || { echo 'Start the Docker daemon, then retry.' >&2; exit 1; }
mise install
exec mise exec -- python3 scripts/dev/lifecycle.py setup
