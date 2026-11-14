#!/usr/bin/env bash
# Only ephemeral, uniquely named test resources; never targets the developer stack.
set -euo pipefail
[[ ${1:-} == storage ]] || { echo 'usage: scripts/test-web-integration.sh storage' >&2; exit 2; }
for tool in docker mise python3; do command -v "$tool" >/dev/null || { echo "required: $tool" >&2; exit 1; }; done
docker info >/dev/null
docker compose version >/dev/null
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
work=$(mktemp -d "${TMPDIR:-/tmp}/webchat-storage.XXXXXXXX")
project="webchat-storage-$(python3 -c 'import uuid; print(uuid.uuid4().hex)')"
database="webchat_test_${project##*-}"
volume="${project}_postgres-data"
export WEBCHAT_TEST_DATABASE_NAME="$database"
cleanup() {
  status=$?
  trap - EXIT
  docker compose -p "$project" -f "$work/compose.yaml" down --timeout 10 || status=1
  if docker volume inspect "$volume" >/dev/null 2>&1; then
    owner=$(docker volume inspect --format '{{index .Labels "com.docker.compose.project"}}' "$volume")
    if [[ "$owner" == "$project" && "$project" == webchat-storage-* ]]; then
      docker volume rm "$volume" >/dev/null || status=1
    else echo "REFUSING cleanup of unowned volume $volume" >&2; status=1; fi
  fi
  rm -f "$work/compose.yaml" "$work/receipt"
  rmdir "$work" || true
  exit "$status"
}
python3 - "$root" "$work/compose.yaml" <<'PY'
import json,sys
from pathlib import Path
root=Path(sys.argv[1]); image=json.loads((root/'infra/runtime-pins.json').read_text())['images']['postgres']
# JSON is valid YAML. Docker chooses an unused loopback-only host port.
config={'services':{'postgres':{'image':image,'environment':{'POSTGRES_PASSWORD':'test-admin-only','WEBCHAT_DB_PASSWORD':'test-app-only','WEBCHAT_TEMPORAL_DB_PASSWORD':'test-temporal-only'},'ports':['127.0.0.1::5432'],'volumes':['postgres-data:/var/lib/postgresql/data',str(root/'infra/postgres-init.sql')+':/docker-entrypoint-initdb.d/init.sql:ro'],'healthcheck':{'test':['CMD-SHELL','pg_isready -U postgres -d postgres'],'interval':'1s','timeout':'2s','retries':60}}},'volumes':{'postgres-data':{}}}
Path(sys.argv[2]).write_text(json.dumps(config))
PY
trap cleanup EXIT
compose=(docker compose -p "$project" -f "$work/compose.yaml")
"${compose[@]}" up -d --wait --wait-timeout 90
[[ "$database" =~ ^webchat_test_[a-f0-9]{32}$ ]] || { echo 'unsafe database name' >&2; exit 1; }
"${compose[@]}" exec -T postgres psql -U postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE $database OWNER webchat" -c "REVOKE ALL ON DATABASE $database FROM PUBLIC"
port=$("${compose[@]}" port postgres 5432)
[[ "$port" =~ ^127\.0\.0\.1:[0-9]+$ ]] || { echo "unsafe target: $port" >&2; exit 1; }
export WEBCHAT_TEST_DATABASE_URL="postgres://webchat:test-app-only@$port/$database?sslmode=disable"
export WEBCHAT_TEST_TEMPORAL_DATABASE_URL="postgres://temporal:test-temporal-only@$port/temporal?sslmode=disable"
export WEBCHAT_TEST_RECEIPT="$work/receipt"
echo "Storage integration project=$project database=$database address=$port volume=$volume"
run() { mise exec -- go test -p 1 -count=1 -tags integration "$@" ./internal/storage/...; }
WEBCHAT_TEST_PHASE=migration run -v -run '^TestMigrationCycle$'
run -race -v -run 'Test(Accept|Command|Logical|History|Attachment|Control|Database|Projection|Schema)'
WEBCHAT_TEST_PHASE=restart-write run -v -run '^TestRestartWrite$'
before=$("${compose[@]}" exec -T postgres psql -U postgres -Atc 'SELECT pg_postmaster_start_time()')
"${compose[@]}" restart postgres
for i in $(seq 1 60); do
  if "${compose[@]}" exec -T postgres pg_isready -U postgres -d postgres >/dev/null 2>&1; then break; fi
  sleep 1
done
after=$("${compose[@]}" exec -T postgres psql -U postgres -Atc 'SELECT pg_postmaster_start_time()')
[[ -n "$after" && "$before" != "$after" ]] || { echo 'Postgres server restart not proven' >&2; exit 1; }
echo "Actual Postgres restart: $before -> $after"
# Docker may reassign an ephemeral published port on a genuine container restart.
port=$("${compose[@]}" port postgres 5432)
[[ "$port" =~ ^127\.0\.0\.1:[0-9]+$ ]] || { echo "unsafe restarted target: $port" >&2; exit 1; }
export WEBCHAT_TEST_DATABASE_URL="postgres://webchat:test-app-only@$port/$database?sslmode=disable"
echo "Restarted Postgres address=$port"
WEBCHAT_TEST_PHASE=restart-read run -v -run '^TestRestartRead$'
