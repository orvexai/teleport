#!/usr/bin/env bash
# Restarts the dev Teleport: kills the running instance (and orphans), then starts
# fresh via dev-start.sh (incremental build + run + connector). Mirrors docmost.

PID_FILE="/tmp/teleport-dev.pid"
RESTART_LOCK="/tmp/teleport-dev-restart.lock"

# shellcheck disable=SC1091
source "$(cd "$(dirname "$0")" && pwd)/_server-lib.sh"

_acquire_lock "$RESTART_LOCK" "dev-restart"

echo "[dev-restart] Killing existing teleport processes..."
_stop_daemon "$PID_FILE" "dev-restart"
_stop_daemon "/tmp/teleport-prod.pid" "dev-restart"
pkill -f "build/teleport start" 2>/dev/null || true
sleep 2

echo "[dev-restart] Starting fresh..."
"$(dirname "$0")/dev-start.sh"
