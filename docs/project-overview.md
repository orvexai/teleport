# Project Overview — Teleport

> **One-paragraph orientation:** Teleport is an identity-aware access platform for infrastructure. It puts a single, certificate-issuing control plane in front of SSH nodes, Kubernetes clusters, databases, internal web apps, Windows desktops, Git repos, and MCP servers. There are no long-lived shared secrets — every access uses a short-lived x509 / SSH cert that the Teleport Auth Service mints. The codebase is a 5-part monorepo: a Go backend (the daemon and all CLIs), an integrations layer (operator + Terraform + access plugins + event handler), a React web UI, an Electron desktop app, and a Rust workspace for RDP.

---

## Identity Card

| | |
| --- | --- |
| **Name** | Teleport (`github.com/gravitational/teleport`) |
| **Vendor** | Gravitational, Inc. (gravitational.com / goteleport.com) |
| **Repository type** | Monorepo, 5 parts |
| **License (core)** | AGPL-3.0 |
| **License (web UI / teleterm)** | Apache-2.0 |
| **Languages** | Go (5 893 files) · TypeScript (2 815) · Rust (21 + 1 standalone crate) · C eBPF (~10) · Protobuf (238) · Helm / HCL / YAML / shell |
| **Public Go SDK** | `github.com/gravitational/teleport/api` — separate module |
| **Public site** | <https://goteleport.com> · docs at <https://goteleport.com/docs> |
| **Wire formats** | mTLS gRPC (internal RPC), HTTPS REST + WebSockets (web/UI), SSH, native DB protocols, RDP, MCP |
| **Storage backends** | etcd · DynamoDB · Firestore · Postgres · Cloud Spanner · SQLite · Kubernetes ConfigMaps · in-memory |
| **Audit / recording stores** | Athena (S3-backed) · DynamoDB · Firestore · Postgres · S3 · GCS · Azure Blob · filesystem |
| **Auth methods (issuance)** | x509 (TLS) + SSH certs minted by an internal CA; supports HSM via PKCS#11 (`ThalesIgnite/crypto11`) and FIPS via BoringSSL |
| **Auth methods (login)** | Local users (with WebAuthn TOTP) · SSO via OIDC / SAML / GitHub · MachineID (bot) · Passwordless WebAuthn · Headless SSH · Hardware-key (PIV / Touch ID) |
| **MFA** | WebAuthn (FIDO2 incl. platform authenticators), TOTP, SSO-MFA passthrough |
| **Device trust** | TPM 2.0 attestation, macOS Touch ID, device-aware roles |
| **Deployment shapes** | Helm (`examples/chart/teleport-cluster`), Terraform AWS modules (`examples/aws/terraform/`), systemd (`examples/systemd/`), launchd (`examples/launchd/`), Docker, Teleport Cloud (hosted) |
| **CI/CD** | 51 GitHub workflows (`.github/workflows/`) + custom composite actions (`.github/actions/`) + buildbox containers (`build.assets/Dockerfile*`) |
| **Design-doc archive** | 230 RFDs in `rfd/` |
| **Enterprise overlay** | `e/` (git submodule, not present in this workspace) |

---

## The 5 Parts at a Glance

| Part | Path | Primary language | Role |
| --- | --- | --- | --- |
| **backend** | `api/`, `lib/`, `tool/`, `proto/`, `gen/`, `integration/`, `bpf/` | **Go 1.25** | The `teleport` daemon (auth/proxy/agents) plus client CLIs (`tsh`, `tctl`, `tbot`, `teleport-update`, `fdpass-teleport`). |
| **integrations** | `integrations/` | **Go** | Sidecar programs — Kubernetes operator, two Terraform providers, audit event-handler, access plugins (Slack/Jira/PagerDuty/…), kube-agent-updater. |
| **web** | `web/packages/{teleport,design,shared,build}/` | **TypeScript / React 19** | Browser UI served by the Proxy at `https://<cluster>/web`. |
| **teleterm** | `web/packages/teleterm/` + `lib/teleterm/` | **TypeScript + Go** | "Teleport Connect" — Electron desktop app talking to a local `tshd` Go daemon over gRPC. |
| **rust-rdp** | `lib/srv/desktop/rdp/{rdpclient,decoder}/` + `web/packages/shared/libs/ironrdp/` | **Rust 1.94** | Two-shape RDP crate: a `staticlib` CGo-linked into the desktop service, and a `cdylib` compiled to WASM for in-browser frame decode. |

---

## Architecture in One Page

```
                  ┌──────────────────────────────────────────────────────┐
                  │   USERS:  tsh • tctl • Teleport Connect • Browser UI │
                  └────────────────────────┬─────────────────────────────┘
                                           │
                                           │  TLS / SSH
                                           ▼
                       ┌──────────────────────────────────┐
                       │           PROXY SERVICE           │  ← lib/proxy
                       │  ALPN/SNI mux on one TCP port     │  ← lib/srv/alpnproxy
                       │  Serves the React UI (embedded)   │  ← lib/web + web/packages/teleport
                       │  Terminates user TLS / SSH        │
                       │  Bridges to agents via tunnel     │  ← lib/reversetunnel
                       └────────────────┬──────────────────┘
                                        │  mTLS gRPC
                                        ▼
                       ┌──────────────────────────────────┐
                       │           AUTH SERVICE            │  ← lib/auth
                       │  • Mints x509 / SSH certs         │
                       │  • Verifies MFA (WebAuthn)        │
                       │  • Evaluates SSO (OIDC/SAML/GH)   │
                       │  • Owns RBAC (lib/authz)          │
                       │  • Issues v1 gRPC services per    │
                       │    resource (lib/auth/<resource>) │
                       └──┬──────────────────────┬────────┘
                          │                      │
            ┌─────────────▼──────┐  ┌────────────▼─────────────┐
            │  KV / metadata     │  │  Audit & session record  │
            │  lib/backend/*     │  │  lib/events/*            │
            │  Dynamo / etcd /   │  │  Athena / S3 / Dynamo /  │
            │  Firestore / PG /  │  │  GCS / Azure Blob /      │
            │  Spanner / SQLite  │  │  Postgres / FS           │
            └────────────────────┘  └──────────────────────────┘
                                        ▲
                                        │
                       ┌────────────────┴──────────────────┐
                       │            AGENTS                  │  ← reverse-tunnel out
                       │  ssh • db • app • kube • desktop   │  ← lib/srv/{regular,db,app,…}
                       │  mcp • git • discovery             │
                       │  ↓ to upstream workloads via       │
                       │    native protocol                 │
                       └────────────────────────────────────┘

External integrations (separate binaries, MachineID auth):
   • integrations/operator/                  ← K8s CRDs ↔ Teleport resources
   • integrations/terraform/  + terraform-mwi ← Terraform providers
   • integrations/access/<plugin>/           ← Slack/Jira/PagerDuty/etc.
   • integrations/event-handler/             ← Audit-event egress to SIEMs
   • integrations/kube-agent-updater/        ← Rolls agent versions in K8s
```

For the cross-part wire-up details (transports, file references, build-time codegen), see [`integration-architecture.md`](./integration-architecture.md).

For the annotated directory layout, see [`source-tree-analysis.md`](./source-tree-analysis.md).

For the toolchain and version table, see [`technology-stack.md`](./technology-stack.md).

---

## Tech-Stack Summary Table

| Layer | Choice |
| --- | --- |
| Backend language | **Go 1.25.10** (root + 5 sub-modules) |
| Backend RPC | **gRPC** + **ConnectRPC**; protos under `proto/` and `api/proto/` |
| Backend deps | ~277 direct go.mod deps — heavy on AWS SDK v2, Azure SDK, GCP cloud libs, Kubernetes client-go, controller-runtime, OpenTelemetry |
| Backend errors | `gravitational/trace` (HTTP/gRPC-aware wrapped errors) |
| Backend time | `jonboulle/clockwork` (fake clocks in tests) |
| Backend logging | Go `log/slog` + OS-specific writers in `lib/utils/log/{eventlog,oslog}` |
| Crypto | x/crypto + `crypto11` (HSM/PKCS#11) + `go-piv/piv-go` (PIV) + `russellhaering/gosaml2` (SAML) + `filippo.io/age` (recording encryption) + BoringSSL via Rust under FIPS |
| Audit | OpenTelemetry traces + structured events via `lib/events` |
| Web language | **TypeScript** (transitioning to `tsgo`/`@typescript/native-preview 7`) |
| Web framework | **React 19** + `react-router 7` + `@tanstack/react-query 5` + `react-hook-form` + `zod` + `styled-components 6` |
| Web build | **Vite 8** + SWC + `vite-plugin-wasm` |
| Web lint/format | **`oxlint` + `oxfmt`** (NOT eslint/prettier) |
| Web tests | Jest 30 + `@testing-library/react 16` + MSW + Playwright + Storybook 10 |
| Desktop shell | **Electron 41** + `electron-vite` + `electron-builder` + `electron-updater` |
| Rust | Edition 2021, toolchain **1.94.0**, workspace resolver 2, IronRDP pinned to git rev `a0a3e750c…` |
| Package mgmt | `go` modules · `cargo` workspace · `pnpm 10.32` workspace (Node 24) |

---

## Build & Test At-a-Glance

| Goal | Command |
| --- | --- |
| Full build (dev) | `make all` |
| Full build (production) | `make full` |
| Build a binary | `make build/teleport` (or `tctl` / `tsh` / `tbot` / `teleport-update` / `fdpass-teleport`) |
| Hot-reload daemon | `make teleport-hot-reload` (needs `CompileDaemon`) |
| Regenerate protos | `make grpc` |
| Build Rust RDP | `make rdpclient` + `make rdpdecoder` |
| Build browser-RDP WASM | `make build-ironrdp-wasm` |
| Full test suite | `make test` (runs helm + sh + api + go + rust + operator + terraform-provider) |
| Go tests only | `make test-go` |
| Web tests | `pnpm test` |
| Web dev server | `pnpm start-teleport` (or `pnpm start-term` for Teleterm) |
| Lint all | `make lint` + `pnpm lint` |
| Release tarballs | `make release-unix` / `release-darwin` / `release-windows` / `release-connect` |

See [`development-guide.md`](./development-guide.md) for the full developer onboarding, prereqs, code conventions, and contribution flow.

---

## Where to Look When You Need…

| If you need to… | Start at… |
| --- | --- |
| Understand auth/identity flow | `architecture-backend.md` § Auth + `findings/backend-auth-security.md` + `rfd/0040-webauthn-support.md` |
| Understand a protocol service (ssh/db/app/kube/desktop/mcp/git) | `findings/backend-protocols.md` + `lib/srv/<protocol>/` |
| Understand a resource (role, user, app, db, …) | `api/types/<resource>.go` + `lib/services/local/<resource>.go` |
| Understand storage / audit / cache | `findings/backend-data-storage.md` + `lib/backend/` + `lib/events/` + `lib/cache/` |
| Understand the proto / public API | `findings/backend-api-surface.md` + `proto/teleport/` + `api/proto/teleport/` |
| Understand the REST endpoints the web UI calls | `findings/backend-api-surface.md` § HTTP REST + `lib/web/apiserver.go` |
| Understand the React UI | `architecture-web.md` + `findings/web-and-teleterm.md` + `web/packages/teleport/src/` |
| Understand Teleport Connect | `architecture-teleterm.md` + `findings/web-and-teleterm.md` + `lib/teleterm/` + `web/packages/teleterm/` |
| Understand the operator | `architecture-integrations.md` + `findings/integrations-rust-bpf-tools.md` + `integrations/operator/README.md` |
| Understand the Terraform provider | `architecture-integrations.md` + `integrations/terraform/DOCS.md` |
| Understand RDP (Go + Rust + WASM) | `architecture-rust-rdp.md` + `findings/integrations-rust-bpf-tools.md` + `lib/srv/desktop/` |
| Build / package / deploy | `development-guide.md` + `findings/build-deploy-ops.md` + `examples/chart/` |
| Understand cross-part wiring | `integration-architecture.md` |
| Search 230 design docs | `grep -l <keyword> rfd/*.md` |
| Find user-facing docs for a feature | `docs/pages/` (also published at `https://goteleport.com/docs`) |

---

## Notable Repository Conventions

- **No top-level `ARCHITECTURE.md`** — that role is split between `rfd/` (history of decisions) and the per-part `architecture-*.md` files generated by this workflow.
- **No top-level `DEPLOYMENT.md`** — Helm charts under `examples/chart/`, Terraform under `examples/aws/terraform/` and `examples/terraform/`, and `examples/systemd/` cover deployment.
- **101 packages under `lib/`**, only 13 have a `README.md`. Per-area documentation tends to live in code comments and RFDs.
- **Every cluster-state resource type lives in `api/types/`**, is defined via proto in `api/proto/teleport/<resource>/`, and is stored/served via the `lib/services/local/<resource>.go` ↔ `lib/cache/<resource>.go` ↔ `lib/auth/<resource>/` chain.
- **Anything described in the user docs that you can't find in the source** is probably in the `e/` enterprise submodule (not present in this workspace).
- **AI reviewers (per `AGENTS.md`)** must focus on auth bypass, secret leakage, injection, crypto/key handling, privilege escalation, durability, concurrency, and reliability regressions — **not** style/perf/readability nits.
