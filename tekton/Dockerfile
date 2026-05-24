# Multi-stage build for a full Teleport server image from this fork's source.
#
# Stage 1 compiles everything inside Gravitational's official buildbox (Go, Rust,
# Node/Yarn, libbpf, libfido2, PAM, protoc, the cross toolchain). `make full`
# builds the web UI assets and then the binaries with the embedded-webassets,
# BPF and RDP build tags, so the resulting `teleport` binary serves the web UI
# and supports desktop access.
#
# Stage 2 is a slim Debian runtime with only the shared libs the binary links
# against at runtime (PAM / audit / cap-ng) plus CA certs (needed for the OIDC
# discovery + token calls to Keycloak) and dumb-init for clean signal handling.
#
# Built by deploy/tekton/teleport-build-pipeline.yaml via the buildah-with-args
# Task. Context is the repo root; this file is passed with -f.
#
# NOTE (first-run shakeout): a from-source Teleport build is heavy — expect tens
# of minutes and several GB of RAM/disk. The exact `make full` invocation inside
# the buildbox (cross toolchain selection, whether ensure-webassets needs extra
# flags) may need a tweak on the first real pipeline run. The image plumbing
# around it (registry, tags, runtime libs) is the stable part.

# Pinned to the buildbox version this branch targets (build.assets/images.mk:
# BUILDBOX_VERSION). Bump in lockstep with that file when you rebase upstream.
ARG BUILDBOX=ghcr.io/gravitational/teleport-buildbox:teleport19

FROM ${BUILDBOX} AS builder

# Native build for the cluster's architecture. Override via --build-arg for arm64.
ARG TELEPORT_ARCH=amd64

# Run as root so the COPYed source tree and the build/ output dir are writable
# regardless of the buildbox's default (teleport:teleport) user.
USER root

# Keep Go/Cargo caches on writable, root-owned paths (the buildbox defaults live
# under /tmp/build owned by the teleport user).
ENV GOPATH=/tmp/ci/go \
    GOCACHE=/tmp/ci/go/build \
    CARGO_HOME=/tmp/ci/rust \
    HOME=/tmp/ci

WORKDIR /go/src/github.com/gravitational/teleport
COPY . .

# `make full` == ensure-webassets (yarn build of the web UI) + `make all`
# (teleport, tctl, tsh, tbot, ... into ./build with webassets_embed + bpf + rdp).
RUN make full OS=linux ARCH=${TELEPORT_ARCH}

# ---------------------------------------------------------------------------

FROM debian:12-slim AS teleport

# Runtime deps: PAM/audit/cap-ng are dynamically linked by the teleport binary;
# ca-certificates is required for OIDC (Keycloak) TLS; dumb-init reaps zombies.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        dumb-init \
        libpam0g \
        libaudit1 \
        libcap-ng0 \
    && rm -rf /var/lib/apt/lists/*

ARG TELEPORT_SRC=/go/src/github.com/gravitational/teleport/build
COPY --from=builder ${TELEPORT_SRC}/teleport /usr/local/bin/teleport
COPY --from=builder ${TELEPORT_SRC}/tctl     /usr/local/bin/tctl
COPY --from=builder ${TELEPORT_SRC}/tsh      /usr/local/bin/tsh
COPY --from=builder ${TELEPORT_SRC}/tbot     /usr/local/bin/tbot

RUN mkdir -p /etc/teleport /var/lib/teleport

# tctl/tsh auto-update is meaningless inside an immutable image.
ENV TELEPORT_TOOLS_VERSION=off

# Graceful shutdown — see https://goteleport.com/docs/reference/signals/
STOPSIGNAL SIGQUIT

ENTRYPOINT ["/usr/bin/dumb-init", "teleport", "start", "-c", "/etc/teleport/teleport.yaml"]
