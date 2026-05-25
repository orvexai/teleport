#!/usr/bin/env bash
# Fast inner loop — run from inside the teleport-dev pod.
# Rebuilds ONLY the teleport Go binary from /workspace/teleport (skips the slow web/
# Rust/BPF rebuild by reusing the already-embedded web assets) and hot-swaps it into
# the running teleport auth+proxy pods, then triggers Teleport's graceful reload
# (SIGHUP re-execs the on-disk binary — no container restart, so the swap sticks until
# the pod is recreated). ~1-3 min vs a ~30-min full image rebuild.
#
#   build-teleport.sh            # build + swap into the live teleport ns pods
#   build-teleport.sh --build    # build only (binary at build/teleport)
set -euo pipefail

REPO="${REPO_DIR:-/workspace/teleport}"
NS="${TELEPORT_NS:-teleport}"
cd "$REPO"

echo "[build] make build/teleport (WEBASSETS_SKIP_BUILD=1) ..."
make build/teleport OS=linux ARCH=amd64 WEBASSETS_SKIP_BUILD=1
BIN="$REPO/build/teleport"
[ -x "$BIN" ] || { echo "[build] FAILED — no binary at $BIN" >&2; exit 1; }
echo "[build] ok: $($BIN version | head -1)"

[ "${1:-}" = "--build" ] && { echo "[build] --build only; skipping swap"; exit 0; }

for comp in auth proxy; do
  for pod in $(kubectl get pods -n "$NS" \
        -l app.kubernetes.io/instance=teleport,app.kubernetes.io/component=$comp \
        --field-selector status.phase=Running -o jsonpath='{.items[*].metadata.name}'); do
    echo "[swap] $pod: cp binary + SIGHUP (graceful re-exec)"
    kubectl cp "$BIN" "$NS/$pod:/usr/local/bin/teleport" -c teleport
    kubectl exec -n "$NS" "$pod" -c teleport -- kill -HUP 1
  done
done
echo "[done] swapped. tail logs: kubectl logs -n $NS -l app.kubernetes.io/component=auth -f"
