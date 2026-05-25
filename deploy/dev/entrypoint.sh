#!/usr/bin/env bash
# teleport-dev entrypoint: clone the fork into /workspace on first boot, bootstrap a
# friendly home (persisted on the home PVC), then exec the CMD (sleep infinity) so the
# pod stays up for `kubectl exec`. Mirrors docmost's dev entrypoint.
set -euo pipefail

WORKSPACE_DIR="${WORKSPACE_DIR:-/workspace}"
REPO_DIR="${WORKSPACE_DIR}/teleport"
GIT_REPO="${GIT_REPO:-https://github.com/orvexai/teleport.git}"
GIT_BRANCH="${GIT_BRANCH:-orvex}"
HOME_DIR="${HOME:-/root}"

mkdir -p "${WORKSPACE_DIR}"

if [ ! -d "${REPO_DIR}/.git" ]; then
    echo "[entrypoint] Cloning ${GIT_REPO}#${GIT_BRANCH} -> ${REPO_DIR}"
    if [ -n "${GIT_TOKEN:-}" ]; then
        CLONE_URL="${GIT_REPO/https:\/\//https://x-access-token:${GIT_TOKEN}@}"
    else
        CLONE_URL="${GIT_REPO}"
    fi
    git clone --branch "${GIT_BRANCH}" "${CLONE_URL}" "${REPO_DIR}" \
        || echo "[entrypoint] clone failed (private repo?) — exec in and clone manually (gh auth login)"
else
    echo "[entrypoint] Existing repo at ${REPO_DIR} — leaving in place"
fi

if [ ! -f "${HOME_DIR}/.bashrc.teleport-dev-bootstrapped" ]; then
    echo "[entrypoint] First-time home bootstrap into ${HOME_DIR}"
    cat >> "${HOME_DIR}/.bashrc" <<'EOF'

# teleport-dev pod defaults
export PS1='\[\e[35m\]teleport-dev\[\e[0m\]:\[\e[36m\]\w\[\e[0m\]$ '
alias ll='ls -lah'
cd /workspace/teleport 2>/dev/null || true
EOF
    cat >> "${HOME_DIR}/.bash_profile" <<'EOF'
[ -f ~/.bashrc ] && . ~/.bashrc
EOF
    touch "${HOME_DIR}/.bashrc.teleport-dev-bootstrapped"
fi

cd "${REPO_DIR}" 2>/dev/null || cd "${WORKSPACE_DIR}"
exec "$@"
