# teleport-dev — an exec-in dev environment for iterating on the OIDC fork without
# the ~30-min full image rebuild loop. You `kubectl exec` in and run Claude Code +
# `go build` here, like the docmost-dev pod.
#
# Base: the Teleport buildbox (Go, Rust, node/yarn, libbpf, libfido2, PAM, protoc,
# cross toolchain) — the exact toolchain `make`/`go build` need, so an incremental
# `go build ./tool/teleport` here is ~1-3 min vs a 30-min full image build.
# Layered on top: Claude Code, kubectl, gh, and shell conveniences.
#
# Mirrors docmost's deploy/docker/dev/Dockerfile. Built by deploy/dev/pipelinerun.yaml.
ARG BUILDBOX=ghcr.io/gravitational/teleport-buildbox:teleport19
FROM ${BUILDBOX}

USER root
ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

RUN apt-get update && apt-get install -y --no-install-recommends \
        vim nano less htop jq tmux tree rsync sudo \
        postgresql-client iputils-ping dnsutils socat bash-completion \
    && rm -rf /var/lib/apt/lists/*

# kubectl (talk to the cluster: cp the freshly-built binary into the teleport pods, exec, logs)
RUN curl -fsSL https://dl.k8s.io/release/v1.34.4/bin/linux/amd64/kubectl -o /usr/local/bin/kubectl \
    && chmod +x /usr/local/bin/kubectl

# gh CLI
RUN curl -fsSL https://github.com/cli/cli/releases/download/v2.65.0/gh_2.65.0_linux_amd64.tar.gz \
        | tar -xz -C /usr/local --strip-components=1 gh_2.65.0_linux_amd64/bin/gh

# Claude Code CLI (the buildbox ships node/npm for the web UI build)
RUN npm install -g @anthropic-ai/claude-code

# Non-root dev user (uid/gid 1000) with passwordless sudo (so `sudo apt-get install`
# works in the box). The buildbox runs as root for CI builds, but this exec-in box
# should be a normal user so files created on the home/workspace PVCs aren't root-owned.
# Existing root-owned PVC content is chowned to 1000:1000 by an initContainer
# (see deploy/dev/k8s.yaml).
RUN groupadd -g 1000 dev \
    && useradd -u 1000 -g 1000 -m -d /home/dev -s /bin/bash dev \
    && printf 'dev ALL=(ALL) NOPASSWD:ALL\n' > /etc/sudoers.d/dev \
    && chmod 0440 /etc/sudoers.d/dev

# Login profile (PATH, prompt, completions, aliases) — baked in the image so a rebuild
# updates it; ~/.bashrc just sources it (see entrypoint.sh).
COPY deploy/dev/profile.sh /etc/profile.d/zz-teleport-dev.sh
RUN chmod 0644 /etc/profile.d/zz-teleport-dev.sh

# Only the entrypoint is baked in; dev-start.sh / dev-restart.sh / prod-*.sh +
# _server-lib.sh live at the repo root and arrive via the clone into /workspace.
COPY deploy/dev/entrypoint.sh /usr/local/bin/teleport-dev-entrypoint.sh
RUN chmod +x /usr/local/bin/teleport-dev-entrypoint.sh

WORKDIR /workspace
# Default to the non-root dev user (k8s also pins this via securityContext).
USER dev
ENTRYPOINT ["/usr/local/bin/teleport-dev-entrypoint.sh"]
CMD ["sleep", "infinity"]
