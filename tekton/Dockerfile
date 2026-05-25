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
# CARGO_TARGET_DIR puts compiled Rust artifacts on the persistent cache PVC so
# subsequent runs skip re-compiling the ironrdp WASM (and any other Rust code).
ENV GOPATH=/tmp/ci/go \
    GOCACHE=/tmp/ci/go/build \
    CARGO_HOME=/tmp/ci/rust \
    CARGO_TARGET_DIR=/tmp/ci/rust-target \
    HOME=/tmp/ci

WORKDIR /go/src/github.com/gravitational/teleport
COPY . .

# Server-side only: the `teleport` server binary + `tctl` (admin), with the embedded
# web UI + BPF. Skips the client tools (tsh/tbot) and the Rust desktop-access build
# (rdpclient) — not needed for the IDP server (OIDC/SSH/kube), much faster, Rust-free.
# build/teleport depends on ensure-webassets, so the web UI is still built + embedded.
RUN make build/teleport build/tctl OS=linux ARCH=${TELEPORT_ARCH} RDPCLIENT_SKIP_BUILD=1

# ---------------------------------------------------------------------------

FROM debian:12-slim AS teleport

# Runtime deps: PAM/audit/cap-ng are dynamically linked by the teleport binary;
# ca-certificates is required for OIDC (Keycloak) TLS; dumb-init reaps zombies.
# libfido2-1 is required by tctl (this build links libfido2 dynamically, unlike the
# upstream release which links it statically); apt pulls its deps (libcbor etc.).
# The buildbox is Debian-bookworm-based, so this matches the .so the binary linked
# against. Without it, tctl fails with "libfido2.so.1: cannot open shared object".
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        dumb-init \
        libpam0g \
        libaudit1 \
        libcap-ng0 \
        libfido2-1 \
    && rm -rf /var/lib/apt/lists/*

# Server-side binaries only — teleport (server) + tctl (admin, used by the connector
# job). tsh/tbot are client tools, not part of the server image.
ARG TELEPORT_SRC=/go/src/github.com/gravitational/teleport/build
COPY --from=builder ${TELEPORT_SRC}/teleport /usr/local/bin/teleport
COPY --from=builder ${TELEPORT_SRC}/tctl     /usr/local/bin/tctl

RUN mkdir -p /etc/teleport /var/lib/teleport

# tctl/tsh auto-update is meaningless inside an immutable image.
ENV TELEPORT_TOOLS_VERSION=off

# Graceful shutdown — see https://goteleport.com/docs/reference/signals/
STOPSIGNAL SIGQUIT

ENTRYPOINT ["/usr/bin/dumb-init", "teleport", "start", "-c", "/etc/teleport/teleport.yaml"]
