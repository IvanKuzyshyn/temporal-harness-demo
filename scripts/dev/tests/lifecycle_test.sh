#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../.."
python3 -m unittest discover -s scripts/dev/tests -p 'test_*.py' -v
