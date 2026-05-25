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
        vim nano less htop jq tmux tree rsync \
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

COPY deploy/dev/entrypoint.sh /usr/local/bin/teleport-dev-entrypoint.sh
COPY deploy/dev/build-teleport.sh /usr/local/bin/build-teleport.sh
RUN chmod +x /usr/local/bin/teleport-dev-entrypoint.sh /usr/local/bin/build-teleport.sh

WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/teleport-dev-entrypoint.sh"]
CMD ["sleep", "infinity"]
