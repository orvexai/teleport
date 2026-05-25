#!/usr/bin/env bash
# Restarts the dev pod's Teleport with a full build: kills the running instance,
# then starts fresh via prod-start.sh (make full + run + connector). Mirrors docmost.

PID_FILE="/tmp/teleport-prod.pid"
RESTART_LOCK="/tmp/teleport-prod-restart.lock"

# shellcheck disable=SC1091
source "$(cd "$(dirname "$0")" && pwd)/_server-lib.sh"

_acquire_lock "$RESTART_LOCK" "prod-restart"

echo "[prod-restart] Killing existing teleport processes..."
_stop_daemon "$PID_FILE" "prod-restart"
_stop_daemon "/tmp/teleport-dev.pid" "prod-restart"
pkill -f "build/teleport start" 2>/dev/null || true
sleep 2

echo "[prod-restart] Starting fresh..."
"$(dirname "$0")/prod-start.sh"
