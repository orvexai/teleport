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

# Make interactive shells load the image-baked profile (PATH, prompt, completions,
# aliases). The profile lives in the image (/etc/profile.d/zz-teleport-dev.sh) so a
# rebuild updates it; ~/.bashrc on the home PVC just sources it. Idempotent each boot.
touch "${HOME_DIR}/.bashrc" "${HOME_DIR}/.bash_profile"
grep -qF 'zz-teleport-dev.sh' "${HOME_DIR}/.bashrc" \
    || printf '\n# teleport-dev: load image-baked profile\n[ -f /etc/profile.d/zz-teleport-dev.sh ] && . /etc/profile.d/zz-teleport-dev.sh\n' >> "${HOME_DIR}/.bashrc"
grep -qF '.bashrc' "${HOME_DIR}/.bash_profile" \
    || printf '[ -f ~/.bashrc ] && . ~/.bashrc\n' >> "${HOME_DIR}/.bash_profile"

cd "${REPO_DIR}" 2>/dev/null || cd "${WORKSPACE_DIR}"
exec "$@"
