#!/usr/bin/env bash
# Starts the dev pod's Teleport with a FULL build (`make full` — web UI + all build
# tags), for a production-equivalent test of the fork. Daemonizes + logs to
# ~/logs/teleport-prod.log. Mirrors docmost's prod-start.sh.

_SERVER_TAG="prod-start"
PID_FILE="/tmp/teleport-prod.pid"
LOG_DIR="${HOME:-/home/dev}/logs"
LOG_FILE="$LOG_DIR/teleport-prod.log"

# shellcheck disable=SC1091
source "$(cd "$(dirname "$0")" && pwd)/_server-lib.sh"

if [ "${_TELEPORT_PROD_DAEMON:-}" != "1" ]; then
    mkdir -p "$LOG_DIR"
    _TELEPORT_PROD_DAEMON=1 nohup "$0" "$@" 2>&1 | tee -a "$LOG_FILE" >> /proc/1/fd/1 &
    echo "[prod-start] Running in background (PID $!), logs at $LOG_FILE"
    exit 0
fi

_TELEPORT_PID=""
_shutdown() {
    echo "[prod-start] Shutting down..."
    [ -n "$_TELEPORT_PID" ] && _kill_tree "$_TELEPORT_PID"
    rm -f "$PID_FILE"
    exit 0
}
trap '_shutdown' TERM INT

_stop_daemon "$PID_FILE" "prod-start"
pkill -f "build/teleport start" 2>/dev/null || true
sleep 2

echo $$ > "$PID_FILE"
echo "[prod-start] Started (PID $$)"

_set_repo_dir
[ -d "$REPO_DIR" ] || { echo "[prod-start] ERROR: $REPO_DIR missing — staying alive"; while true; do sleep 60; done; }
cd "$REPO_DIR"
_load_dotenv
_set_git_sha
# TELEPORT_CONFIG_FILE = config *path*. NOT TELEPORT_CONFIG, which teleport/tctl treat
# as an inline base64 config string (→ "configuration should be base64 encoded").
export TELEPORT_CONFIG_FILE="${REPO_DIR}/deploy/dev/teleport.yaml"

echo "[prod-start] Server build (teleport + tctl + fresh web UI; no Rust/client tools)..."
# Server-side only (matches the CI image): teleport + tctl + embedded web UI, skipping
# tsh/tbot and the Rust RDP client. Unlike dev-start, the web UI is rebuilt (no
# WEBASSETS_SKIP_BUILD) for a clean prod-equivalent build.
make build/teleport build/tctl OS=linux ARCH=amd64 RDPCLIENT_SKIP_BUILD=1 2>&1 || echo "[prod-start] WARN: build exited non-zero (continuing)"
[ -x "${REPO_DIR}/build/teleport" ] || { echo "[prod-start] ERROR: no teleport binary — staying alive"; while true; do sleep 60; done; }

echo "[prod-start] Starting teleport (web :3080, TLS at the ingress)..."
"${REPO_DIR}/build/teleport" start -c "$TELEPORT_CONFIG_FILE" --insecure-no-tls &
_TELEPORT_PID=$!
echo "[prod-start] teleport PID: $_TELEPORT_PID"

_wait_web_ready "prod-start"
_apply_oidc_connector

wait "$_TELEPORT_PID" || echo "[prod-start] WARN: teleport exited non-zero"
echo "[prod-start] teleport stopped — staying alive. Run prod-restart.sh to rebuild+restart."
_TELEPORT_PID=""
while true; do sleep 60; done
