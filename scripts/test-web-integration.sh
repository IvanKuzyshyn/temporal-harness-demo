#!/usr/bin/env bash
# Only ephemeral, uniquely named test resources; never targets the developer stack.
set -euo pipefail
mode=${1:-}
[[ "$mode" == storage || "$mode" == api || "$mode" == chat || "$mode" == replacement || "$mode" == approval || "$mode" == fetch || "$mode" == attachments || "$mode" == analysis || "$mode" == context || "$mode" == stream ]] || { echo 'usage: scripts/test-web-integration.sh storage|api|chat|replacement|approval|fetch|attachments|analysis|context|stream' >&2; exit 2; }
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
# Valid runtime credentials so api mode can exercise the real executable too.
export WEBCHAT_TEST_APP_PASSWORD
WEBCHAT_TEST_APP_PASSWORD=$(python3 -c 'import secrets; print(secrets.token_hex(32))')
temporal_pid=
test_pid=
cleanup() {
  status=$?
  trap - EXIT
  if [[ -n "$test_pid" ]] && kill -0 "$test_pid" 2>/dev/null; then
    kill -TERM "$test_pid" 2>/dev/null || true
    wait "$test_pid" || true
  fi
  if [[ -n "$temporal_pid" ]]; then
    kill -TERM "$temporal_pid" 2>/dev/null || true
    wait "$temporal_pid" || true
    if [[ "$status" != 0 ]]; then tail -60 "$work/temporal.log"; fi
  fi
  # Explicit retention mode for acceptance sessions that must not delete data.
  # Stops only this test-owned stack; keeps SQL volume, receipts and server DB.
  if [[ "${WEBCHAT_TEST_RETAIN:-0}" == 1 ]]; then
    docker compose -p "$project" -f "$work/compose.yaml" stop --timeout 10 || status=1
    echo "Retained test artifacts: work=$work project=$project volume=$volume"
    exit "$status"
  fi
  docker compose -p "$project" -f "$work/compose.yaml" down --timeout 10 || status=1
  if docker volume inspect "$volume" >/dev/null 2>&1; then
    owner=$(docker volume inspect --format '{{index .Labels "com.docker.compose.project"}}' "$volume")
    if [[ "$owner" == "$project" && "$project" == webchat-storage-* ]]; then
      docker volume rm "$volume" >/dev/null || status=1
    else echo "REFUSING cleanup of unowned volume $volume" >&2; status=1; fi
  fi
  rm -f "$work/compose.yaml" "$work/receipt" "$work/temporal.log" "$work/temporal.db" "$work/temporal.db-shm" "$work/temporal.db-wal" "$work/restart.request" "$work/restart.request.done"
  rmdir "$work" || true
  exit "$status"
}
python3 - "$root" "$work/compose.yaml" <<'PY'
import json,sys,os
from pathlib import Path
root=Path(sys.argv[1]); image=json.loads((root/'infra/runtime-pins.json').read_text())['images']['postgres']
# JSON is valid YAML. Docker chooses an unused loopback-only host port.
config={'services':{'postgres':{'image':image,'environment':{'POSTGRES_PASSWORD':'test-admin-only','WEBCHAT_DB_PASSWORD':os.environ['WEBCHAT_TEST_APP_PASSWORD'],'WEBCHAT_TEMPORAL_DB_PASSWORD':'test-temporal-only'},'ports':['127.0.0.1::5432'],'volumes':['postgres-data:/var/lib/postgresql/data',str(root/'infra/postgres-init.sql')+':/docker-entrypoint-initdb.d/init.sql:ro'],'healthcheck':{'test':['CMD-SHELL','pg_isready -h 127.0.0.1 -U postgres -d postgres'],'interval':'1s','timeout':'2s','retries':60}}},'volumes':{'postgres-data':{}}}
Path(sys.argv[2]).write_text(json.dumps(config))
PY
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
compose=(docker compose -p "$project" -f "$work/compose.yaml")
"${compose[@]}" up -d --wait --wait-timeout 90
[[ "$database" =~ ^webchat_test_[a-f0-9]{32}$ ]] || { echo 'unsafe database name' >&2; exit 1; }
"${compose[@]}" exec -T postgres psql -U postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE $database OWNER webchat" -c "REVOKE ALL ON DATABASE $database FROM PUBLIC"
port=$("${compose[@]}" port postgres 5432)
[[ "$port" =~ ^127\.0\.0\.1:[0-9]+$ ]] || { echo "unsafe target: $port" >&2; exit 1; }
export WEBCHAT_TEST_DATABASE_URL="postgres://webchat:$WEBCHAT_TEST_APP_PASSWORD@$port/$database?sslmode=disable"
export WEBCHAT_TEST_TEMPORAL_DATABASE_URL="postgres://temporal:test-temporal-only@$port/temporal?sslmode=disable"
export WEBCHAT_TEST_RECEIPT="$work/receipt"
echo "Storage integration project=$project database=$database address=$port volume=$volume"
run() { mise exec -- go test -p 1 -count=1 -timeout 3m -tags integration "$@" ./internal/storage/...; }
WEBCHAT_TEST_PHASE=migration run -v -run '^TestMigration(Cycle|DispatchUpgrade|ChatCleanupUpgrade)$'
run -race -v -run "${WEBCHAT_TEST_STORAGE_RUN:-Test(Accept|Command|Logical|History|Attachment|Control|Database|Projection|Schema|WebChat|ModelAttemptRetry|MissingDisplay|ChatAdmission|ChatResultAttempt|ChatAttempt|ChatCleanup|ChatFailed|ChatOverlapping|ChatCandidate|Context|CancelledDraft|ReplacementPrompt|WebModelSteps|WebApproval|WebTool)}"
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
export WEBCHAT_TEST_DATABASE_URL="postgres://webchat:$WEBCHAT_TEST_APP_PASSWORD@$port/$database?sslmode=disable"
echo "Restarted Postgres address=$port"
WEBCHAT_TEST_PHASE=restart-read run -v -run '^TestRestartRead$'
if [[ "$mode" == attachments || "$mode" == analysis || "$mode" == context ]]; then
  docker build -t webchat-attachment-parser:1 infra/attachment-parser
  export WEBCHAT_TEST_PARSERS=1
  mise exec -- go test -p 1 -count=1 -timeout 3m -race -tags integration -v ./internal/attachments/...
fi
if [[ "$mode" == api || "$mode" == chat || "$mode" == replacement || "$mode" == approval || "$mode" == fetch || "$mode" == attachments || "$mode" == analysis || "$mode" == context || "$mode" == stream ]]; then
  # Serial packages: dispatcher scenarios deliberately reschedule commands in
  # this unique test database, never any developer/shared application database.
  mise exec -- go test -p 1 -count=1 -timeout 3m -race -tags integration -v ./cmd/web-api/... ./internal/api/... ./internal/dispatch/...
fi
if [[ "$mode" == chat || "$mode" == replacement || "$mode" == approval || "$mode" == fetch || "$mode" == attachments || "$mode" == analysis || "$mode" == context || "$mode" == stream ]]; then
  # Launch the pinned actual server as an owned child, never inherited services.
  read -r grpc_port http_port metrics_port < <(python3 - <<'PY'
import socket
sockets=[]
for _ in range(3):
 s=socket.socket(); s.bind(('127.0.0.1',0)); sockets.append(s)
print(*(s.getsockname()[1] for s in sockets))
PY
)
  temporal_bin=$(mise which temporal)
  start_temporal() {
    "$temporal_bin" server start-dev --ip 127.0.0.1 --port "$grpc_port" --http-port "$http_port" --metrics-port "$metrics_port" --headless --db-filename "$work/temporal.db" >>"$work/temporal.log" 2>&1 &
    temporal_pid=$!
  }
  start_temporal
  export WEBCHAT_TEST_TEMPORAL_ADDRESS="127.0.0.1:$grpc_port"
  export WEBCHAT_TEST_POSTGRES_CONTAINER="${project}-postgres-1"
  for i in $(seq 1 60); do
    kill -0 "$temporal_pid" 2>/dev/null || { echo 'test Temporal exited'; exit 1; }
    if "$temporal_bin" operator cluster health --address "$WEBCHAT_TEST_TEMPORAL_ADDRESS" >/dev/null 2>&1; then break; fi
    sleep 1
  done
  echo "Actual isolated Temporal address=$WEBCHAT_TEST_TEMPORAL_ADDRESS pid=$temporal_pid"
  export WEBCHAT_TEST_RESTART_REQUEST="$work/restart.request"
  default_run=.
  if [[ "$mode" == stream ]]; then default_run='^TestStream'; fi
  if [[ "$mode" == context ]]; then default_run='Test(Context|Document|ImageApplication|ImageChild|ImageScope|Replacement|OrdinaryHardWorkerDeath|ContinueAsNewCommandDedup)'; fi
  if [[ "$mode" == analysis ]]; then default_run='Test(Document|AnalysisChild|ApprovedImage|ImageApplication|ImageScope|ImageChild|ParentCancellationJoinsDocumentChild|Attachment|WebToolTimedOutBodyBlocksAcceptedReplacement|ReplacementAfterTimedOutAttemptDoesNotBypassCleanup|OrdinaryHardWorkerDeathRecoversCanonicalResponseAndFollowUp)'; fi
  if [[ "$mode" == replacement ]]; then default_run='Test(Replacement|ClosedUnreceiptedControl|OrdinaryHardWorkerDeath)'; fi
  if [[ "$mode" == approval ]]; then default_run='Test(Approval|Approve|Revoke|StaleDecision|Expiry|NewTool|WebTool)'; fi
  if [[ "$mode" == fetch ]]; then default_run='Test(Fetch|WebToolTimedOutBodyBlocksAcceptedReplacement|ApproveAllSurvivesRollover|StaleDecisionAfterReplacement)'; fi
  if [[ "$mode" == attachments ]]; then default_run='Test(Attachment|FetchReplacement|WebToolTimedOutBodyBlocksAcceptedReplacement|ApproveAllSurvivesRollover|StaleDecisionAfterReplacement)'; fi
  mise exec -- go test -p 1 -count=1 -race -tags integration -v -timeout 15m -run "${WEBCHAT_TEST_CHAT_RUN:-$default_run}" ./internal/webintegration/... &
  test_pid=$!
  # Only this parent restarts its owned children/Compose project. Tests cannot
  # supply a PID/container/path to the restart protocol.
  while kill -0 "$test_pid" 2>/dev/null; do
    if [[ -f "$WEBCHAT_TEST_RESTART_REQUEST" ]]; then
      rm "$WEBCHAT_TEST_RESTART_REQUEST"
      old_pid=$temporal_pid
      kill -TERM "$temporal_pid"
      wait "$temporal_pid" || true
      temporal_pid=
      "${compose[@]}" restart postgres
      for i in $(seq 1 60); do
        if "${compose[@]}" exec -T postgres pg_isready -h 127.0.0.1 -U postgres -d postgres >/dev/null 2>&1; then break; fi
        sleep 1
      done
      port=$("${compose[@]}" port postgres 5432)
      [[ "$port" =~ ^127\.0\.0\.1:[0-9]+$ ]] || { echo 'unsafe restarted target' >&2; exit 1; }
      start_temporal
      for i in $(seq 1 60); do
        kill -0 "$temporal_pid" 2>/dev/null || { echo 'restarted Temporal exited'; exit 1; }
        if "$temporal_bin" operator cluster health --address "$WEBCHAT_TEST_TEMPORAL_ADDRESS" >/dev/null 2>&1; then break; fi
        sleep 1
      done
      "$temporal_bin" operator cluster health --address "$WEBCHAT_TEST_TEMPORAL_ADDRESS" >/dev/null
      echo "Actual isolated Temporal restart pid=$old_pid -> $temporal_pid; Postgres address=$port"
      printf '%s\n' "$port" >"$WEBCHAT_TEST_RESTART_REQUEST.done"
    fi
    sleep 0.1
  done
  wait "$test_pid"
  test_pid=
fi
