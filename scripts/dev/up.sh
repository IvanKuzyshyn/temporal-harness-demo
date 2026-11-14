#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
exec mise exec -- python3 scripts/dev/lifecycle.py up
