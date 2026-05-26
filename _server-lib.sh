#!/usr/bin/env bash
# Shared helpers sourced by dev-start.sh / dev-restart.sh / prod-start.sh /
# prod-restart.sh. Do not execute directly. Ported from docmost's _server-lib.sh,
# adapted to build + run a single teleport process (Go) instead of node/vite.

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    echo "_server-lib.sh: source this file, do not run it directly." >&2
    exit 1
fi

# Recursively kill a process tree (leaves first).
_kill_tree() {
    local pid=$1 sig=${2:-TERM}
    [ -z "$pid" ] && return
    local child
    for child in $(pgrep -P "$pid" 2>/dev/null); do _kill_tree "$child" "$sig"; done
    kill -"$sig" "$pid" 2>/dev/null || true
}

# Stop a tracked daemon by PID file and wait for it to die.
_stop_daemon() {
    local pid_file=$1 tag=${2:-server}
    [ -f "$pid_file" ] || return 0
    local old_pid; old_pid=$(cat "$pid_file" 2>/dev/null || true)
    if [ -n "$old_pid" ] && kill -0 "$old_pid" 2>/dev/null; then
        echo "[$tag] Stopping daemon PID $old_pid..."
        _kill_tree "$old_pid"
        for _ in 1 2 3 4 5 6 7 8; do kill -0 "$old_pid" 2>/dev/null || break; sleep 1; done
        _kill_tree "$old_pid" KILL
    fi
    rm -f "$pid_file"
}

# Acquire a restart lock (flock fd 9), released on EXIT.
_acquire_lock() {
    local lock_file=$1 tag=${2:-server}
    exec 9>"$lock_file"
    if ! flock -w 60 9; then echo "[$tag] Could not acquire lock after 60s — aborting"; exit 1; fi
    trap 'flock -u 9' EXIT
}

# Detect REPO_DIR: pod path (/workspace/teleport) or local checkout.
_set_repo_dir() {
    if [ -d "${WORKSPACE_DIR:-}/teleport" ]; then
        REPO_DIR="${WORKSPACE_DIR}/teleport"
    else
        REPO_DIR="${WORKSPACE_DIR:-/home/daniel/repos/teleport}"
    fi
}

# Load .env overrides if present.
_load_dotenv() {
    local tag=${_SERVER_TAG:-server}
    if [ -f "${REPO_DIR}/.env" ]; then
        echo "[$tag] Loading .env from ${REPO_DIR}/.env"
        set -a; source "${REPO_DIR}/.env"; set +a
    fi
}

# AGPL §13: record the running source commit.
_set_git_sha() {
    local tag=${_SERVER_TAG:-server}
    export ORVEX_GIT_SHA; ORVEX_GIT_SHA=$(git -C "${REPO_DIR}" rev-parse HEAD 2>/dev/null || echo dev-unknown)
    echo "[$tag] ORVEX_GIT_SHA=${ORVEX_GIT_SHA}"
}

# Wait for teleport's web listener (3080) to accept connections.
_wait_web_ready() {
    local tag=${1:-server} max=${2:-180} w=0
    echo "[$tag] Waiting for teleport web :3080 (max ${max}s)..."
    until (echo >/dev/tcp/127.0.0.1/3080) 2>/dev/null || [ "$w" -ge "$max" ]; do sleep 1; w=$((w+1)); done
    [ "$w" -lt "$max" ] && echo "[$tag] web ready after ${w}s" || echo "[$tag] WARN: web not ready after ${max}s"
}

# Bootstrap the Keycloak OIDC connector on first run only (no --force).
# After initial creation, group→role mappings are managed via the Teleport UI
# so that UI edits survive restarts. To reset to defaults, delete the connector
# manually: tctl rm oidc/keycloak
_apply_oidc_connector() {
    local tag=${_SERVER_TAG:-server}
    [ -z "${OIDC_CLIENT_SECRET:-}" ] && { echo "[$tag] OIDC_CLIENT_SECRET unset — skipping connector"; return 0; }
    if "${REPO_DIR}/build/tctl" --config "${TELEPORT_CONFIG_FILE:-${REPO_DIR}/deploy/dev/teleport.yaml}" \
            get oidc/keycloak >/dev/null 2>&1; then
        echo "[$tag] Keycloak OIDC connector already exists — skipping (edit via UI or delete to reset)"
        _apply_login_rule "$tag"
        return 0
    fi
    echo "[$tag] Creating Keycloak OIDC connector (first run)..."
    "${REPO_DIR}/build/tctl" --config "${TELEPORT_CONFIG_FILE:-${REPO_DIR}/deploy/dev/teleport.yaml}" \
        create -f - <<EOF || echo "[$tag] WARN: connector apply failed"
kind: oidc
version: v3
metadata:
  name: keycloak
spec:
  display: "Keycloak (dev)"
  issuer_url: "https://${KEYCLOAK_HOST:-auth.eu-central-1.myidp.cloud}/realms/my-idp"
  client_id: teleport
  client_secret: "${OIDC_CLIENT_SECRET}"
  redirect_url: "https://${TELEPORT_DEV_HOST:-tp-dev.eu-central-1.myidp.cloud}/v1/webapi/oidc/callback"
  username_claim: preferred_username
  scope: ["openid", "email", "profile", "groups"]
  pkce_mode: "enabled"
  claims_to_roles:
    - claim: groups
      value: Administrators
      roles: ["editor", "access", "auditor"]
    - claim: groups
      value: "Platform Team"
      roles: ["editor", "access"]
    - claim: groups
      value: Developers
      roles: ["access"]
    - claim: groups
      value: "*"
      roles: ["access"]
EOF
    _apply_login_rule "$tag"
}

# Apply an Administrators role that grants SSH login as the OIDC preferred_username.
# On Teleport Enterprise you could use login_rule instead; on OSS we template the role.
_apply_login_rule() {
    local tag=${1:-server}
    echo "[$tag] Applying Administrators role (SSH login from preferred_username)..."
    "${REPO_DIR}/build/tctl" --config "${TELEPORT_CONFIG_FILE:-${REPO_DIR}/deploy/dev/teleport.yaml}" \
        create -f - --force <<'LREOF' || echo "[$tag] WARN: role apply failed"
kind: role
version: v7
metadata:
  name: Administrators
spec:
  allow:
    logins:
      - "{{external.preferred_username}}"
      - "{{internal.logins}}"
    kubernetes_groups:
      - "system:masters"
    kubernetes_labels:
      "*": "*"
    kubernetes_resources:
      - kind: "*"
        name: "*"
        verbs: ["*"]
    rules:
      - resources: ["*"]
        verbs: ["*"]
LREOF
}
