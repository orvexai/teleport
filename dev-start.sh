#!/usr/bin/env bash
# Starts the dev Teleport: incremental `make build/teleport` + `build/tctl` (reuses
# embedded web assets), runs teleport standalone, applies the Keycloak connector.
# Re-running kills the existing instance and restarts. Daemonizes + logs to
# ~/logs/teleport.log. Mirrors docmost's dev-start.sh.

_SERVER_TAG="dev-start"
PID_FILE="/tmp/teleport-dev.pid"
LOG_DIR="${HOME:-/home/dev}/logs"
LOG_FILE="$LOG_DIR/teleport.log"

# shellcheck disable=SC1091
source "$(cd "$(dirname "$0")" && pwd)/_server-lib.sh"

# Self-daemonize: re-exec in background with logging if not already a daemon.
if [ "${_TELEPORT_DAEMON:-}" != "1" ]; then
    mkdir -p "$LOG_DIR"
    _TELEPORT_DAEMON=1 nohup "$0" "$@" 2>&1 | tee -a "$LOG_FILE" >> /proc/1/fd/1 &
    echo "[dev-start] Running in background (PID $!), logs at $LOG_FILE"
    exit 0
fi

_TELEPORT_PID=""
_shutdown() {
    echo "[dev-start] Shutting down..."
    [ -n "$_TELEPORT_PID" ] && _kill_tree "$_TELEPORT_PID"
    rm -f "$PID_FILE"
    exit 0
}
trap '_shutdown' TERM INT

_stop_daemon "$PID_FILE" "dev-start"
pkill -f "build/teleport start" 2>/dev/null || true
sleep 2

echo $$ > "$PID_FILE"
echo "[dev-start] Started (PID $$)"

_set_repo_dir
[ -d "$REPO_DIR" ] || { echo "[dev-start] ERROR: $REPO_DIR missing — staying alive"; while true; do sleep 60; done; }
cd "$REPO_DIR"
_load_dotenv
_set_git_sha
# TELEPORT_CONFIG_FILE = config *path*. NOT TELEPORT_CONFIG, which teleport/tctl treat
# as an inline base64 config string (→ "configuration should be base64 encoded").
export TELEPORT_CONFIG_FILE="${REPO_DIR}/deploy/dev/teleport.yaml"

echo "[dev-start] Building teleport + tctl (incremental; skip web + Rust RDP)..."
# RDPCLIENT_SKIP_BUILD=1: skip the Rust desktop-access (rdpclient) build — not needed
# for OIDC dev/testing, and it avoids the Rust toolchain entirely (faster, no override
# mismatch). build/teleport + build/tctl don't pull in fdpass, so this build is Rust-free.
#
# We build in two phases:
# 1. tctl + sessionhelper via make (no web asset dependency).
# 2. teleport binary via go build directly — this preserves any existing production
#    webassets/teleport/index.html (WEBASSETS_SKIP_BUILD=1 would overwrite it with the
#    Vite dev-server index.html that references /src/boot.tsx, leaving a blank UI).
make build/tctl session/reexec/embed/sessionhelper OS=linux ARCH=amd64 WEBASSETS_SKIP_BUILD=1 RDPCLIENT_SKIP_BUILD=1 2>&1 \
    || echo "[dev-start] WARN: tctl/sessionhelper build exited non-zero"

# Ensure production web assets are in place before embedding them.
_PROD_INDEX="${REPO_DIR}/webassets/teleport/index.html"
_PROD_JS="${REPO_DIR}/webassets/teleport/app/app.js.br"
if [ -f "$_PROD_JS" ] && grep -q '/web/app/app.js' "$_PROD_INDEX" 2>/dev/null; then
    echo "[dev-start] Production web assets present — embedding into teleport binary..."
else
    echo "[dev-start] WARN: no production web assets; UI will be blank. Run 'make build-ui' then dev-restart to fix."
    mkdir -p "${REPO_DIR}/webassets/teleport/app"
    cp "${REPO_DIR}/web/packages/teleport/index.html" "$_PROD_INDEX"
fi

# Match the Makefile's KUBECTL_SETVERSION (embedded kubectl version string).
_KUBECTL_VERSION=$(cd "$REPO_DIR" && go list -m -f '{{.Version}}' k8s.io/kubectl 2>/dev/null | sed 's/v0/v1/')
GOOS=linux GOARCH=amd64 CGO_ENABLED=1 CGO_LDFLAGS="-Wl,-Bstatic -Wl,-Bdynamic -Wl,--as-needed" \
    go build -tags "grpcnotrace webassets_embed pam bpf sessionhelper_embed kustomize_disable_go_plugin_support" \
    -o "${REPO_DIR}/build/teleport" \
    -ldflags "-w -s -X k8s.io/component-base/version.gitVersion=${_KUBECTL_VERSION:-v1.35.1}" \
    -trimpath -buildvcs=false \
    "${REPO_DIR}/tool/teleport" 2>&1 \
    || echo "[dev-start] WARN: teleport build exited non-zero (continuing with existing binary)"
[ -x "${REPO_DIR}/build/teleport" ] || { echo "[dev-start] ERROR: no teleport binary — staying alive"; while true; do sleep 60; done; }

echo "[dev-start] Starting teleport (web :3080, TLS terminated at the ingress)..."
"${REPO_DIR}/build/teleport" start -c "$TELEPORT_CONFIG_FILE" --insecure-no-tls &
_TELEPORT_PID=$!
echo "[dev-start] teleport PID: $_TELEPORT_PID"

_wait_web_ready "dev-start"
_apply_oidc_connector

wait "$_TELEPORT_PID" || echo "[dev-start] WARN: teleport exited non-zero"
echo "[dev-start] teleport stopped — staying alive. Run dev-restart.sh to rebuild+restart."
_TELEPORT_PID=""
while true; do sleep 60; done
