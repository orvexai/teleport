# Integration Architecture

How the 5 parts of the Teleport monorepo connect at runtime, at build time, and at the protocol level. **This document is about cross-part wiring** — see `architecture-<part>.md` for *within*-part design.

> Convention: when we say "Auth", we mean the Teleport Auth Service (a Go process — usually a binary built from `tool/teleport/` running with `auth_service.enabled=true` in its config). When we say "Proxy", we mean the Teleport Proxy Service (the same binary, with `proxy_service.enabled=true`). Most real deployments run them as separate processes.

---

## 1. Logical Topology

```
                      ┌───────────────────────────────────────────────────┐
                      │              CLIENT-SIDE                          │
                      │  ┌────────┐   ┌────────┐   ┌──────────────────┐   │
                      │  │  tsh   │   │  tctl  │   │  Teleport Connect│   │
                      │  │ (Go)   │   │ (Go)   │   │  (Electron + Go) │   │
                      │  └───┬────┘   └───┬────┘   └────────┬─────────┘   │
                      └──────┼────────────┼─────────────────┼─────────────┘
                             │            │                 │
                             │ TLS/SSH    │ TLS gRPC        │ TLS gRPC
                             │            │                 │
                ┌────────────▼────────────▼─────────────────▼─────┐
                │               TELEPORT PROXY                    │
                │  ┌──────────────────────────────────────────┐   │
                │  │  ALPN/SNI multiplexer (lib/srv/alpnproxy)│   │
                │  │  HTTPS  • SSH  • DB-TLS  • RDP-TLS  • …  │   │
                │  └──────────────────────────────────────────┘   │
                └────────────┬──────────────────┬─────────────────┘
                             │ mTLS gRPC        │ reverse tunnel
                ┌────────────▼─────────┐    ┌───▼─────────────────────────┐
                │     TELEPORT AUTH    │    │     AGENTS (behind NAT)      │
                │  • gRPC (api/proto)  │    │  ssh / db / app / kube /     │
                │  • Issues x509 + SSH │    │  desktop / mcp / git / disc. │
                │  • Stores in backend │    │  → register via lib/auth/join│
                │  • lib/services      │    │  → keep tunnel via           │
                │  • lib/cache         │    │     lib/reversetunnel        │
                │  • lib/events        │    └──────────────────────────────┘
                └─────────┬────────────┘
                          │
                ┌─────────▼─────────┐    ┌──────────────────────────────┐
                │  Storage backend  │    │  Audit / session-recording   │
                │  (lib/backend/*)  │    │  store (lib/events/*)        │
                │  etcd | Dynamo |  │    │  Athena | Dynamo | Postgres |│
                │  Firestore | PG | │    │  S3 | GCS | Azure Blob | FS  │
                │  Spanner | SQLite │    │                              │
                └───────────────────┘    └──────────────────────────────┘

                External listeners:
                ┌──────────────────────┐  ┌──────────────────────┐
                │ Operator (K8s)       │  │ Terraform provider   │  ← integrations/
                │ Access plugins       │  │ event-handler (SIEM) │
                │ (Slack/Jira/…)       │  │ kube-agent-updater   │
                └──────────────────────┘  └──────────────────────┘
                       all auth via tbot (MachineID) → Auth gRPC
```

The Web UI is **inside** the Proxy box: assets are embedded into the `teleport` binary via Go `embed.FS` (`webassets_embed.go`) and served from the proxy's HTTPS port. The browser talks to `lib/web/*` HTTP routes; `lib/web/*` calls into `lib/auth/*` over the local gRPC client.

---

## 2. Inventory of Cross-Part Seams

| # | From part / file | To part / file | Transport | Notes |
| --- | --- | --- | --- | --- |
| **S1** | `web/packages/teleport/src/services/*.ts` (browser) | `lib/web/apiserver.go` + sub-handlers (backend) | HTTPS REST + WebSockets over the same `:3080` port | Browser-friendly JSON shapes; not raw protobuf. Some endpoints upgrade to WS (terminal, desktop, kube exec, recording playback). |
| **S2** | `web/packages/teleport/src/services/*.ts` (browser) — proto endpoints | `lib/web/*` Connect handlers + downstream `lib/auth/*` v1 services | HTTPS POST with `application/proto`+`application/connect+json` via `@protobuf-ts` | Used for newer resource APIs. |
| **S3** | `web/packages/teleterm/src/services/` (Electron renderer / main) | `lib/teleterm/apiserver/` (the `tshd` Go daemon) | gRPC over a **local** Unix socket (Linux/macOS) or named pipe (Windows). Local-only — no network. | Renderer never talks to a Teleport cluster directly; everything goes through `tshd`. |
| **S4** | `lib/teleterm/` (Go) | Teleport cluster's Proxy | mTLS gRPC + standard Proxy connections | tshd is essentially an embedded `tsh` + connection multiplexer. |
| **S5** | `lib/srv/desktop/rdp/rdpclient/client.go` (Go) | `lib/srv/desktop/rdp/rdpclient/src/lib.rs` (Rust) | **CGo** static linking + `cbindgen`-generated C header | Go ↔ Rust FFI. Rust crate is compiled to a `staticlib` and linked into the `teleport` binary on `linux`/`windows`/`macos`. |
| **S6** | `web/packages/shared/libs/ironrdp/*.ts` (browser) | `web/packages/shared/libs/ironrdp/src/lib.rs` (Rust → WASM) | `wasm-bindgen` (cdylib → JS bindings) | Built by `make build-ironrdp-wasm`; bundled by Vite. Decodes RDP graphics in the browser; receives RDP frames over WebSocket from `lib/srv/desktop` via the proxy. |
| **S7** | `lib/bpf/*.go` (Go) | `bpf/*.bpf.c` (eBPF C → compiled) | `cilium/ebpf` loader; programs attached to per-cgroup hooks | Captures SSH session-recording auxiliary events (process exec, network conns, file accesses). Output flows through `lib/events`. |
| **S8** | `tool/tsh/common/*.go` (Go) | `tool/fdpass-teleport/src/*.rs` (standalone Rust binary) | `tsh` spawns `fdpass-teleport` as a child; communicates over an inherited Unix socket; passes file descriptors via `nix` `SCM_RIGHTS` | Used for PIV/FIPS scenarios where the FD lifetime must outlive the parent Go process. |
| **S9** | `integrations/operator/controllers/*.go` (Go controller) | Auth gRPC (`lib/auth`) via `api/client` | gRPC, auth = identity file from embedded `tbot` | Operator joins as a MachineID bot. Reconciles K8s CRDs ↔ Teleport resources. |
| **S10** | `integrations/terraform/provider/*.go` (Go provider) | Auth gRPC via `api/client` | gRPC, auth = identity file passed via provider config | Resources defined in `tfschema/*` are code-generated from Teleport protos. |
| **S11** | `integrations/access/<plugin>/cmd/teleport-<plugin>/*.go` | Auth gRPC: subscribes to `access_request` events via watcher | gRPC stream, auth = MachineID identity | When an event arrives, the plugin posts to the vendor API (Slack/PagerDuty/etc.). On vendor callback, plugin updates the request via `api/client`. |
| **S12** | `integrations/event-handler/main.go` | Auth gRPC: streams audit events | gRPC stream, auth = identity file | Forwards events out to Splunk/Datadog/Elastic/Fluentd over the configured sink. |
| **S13** | `integrations/kube-agent-updater/cmd/*.go` | K8s API (not Teleport's) + Teleport version registry | controller-runtime watch on `Deployment` resources | Rolls deployed `teleport-kube-agent` pods when a new version is published. |
| **S14** | `lib/proxy/` (one proxy) | `lib/proxy/peer/` → another `lib/proxy/` | Proxy-to-proxy gRPC peering. QUIC variant defined in `proto/teleport/quicpeering/`. | Lets a multi-proxy cluster route a connection to whichever proxy holds the reverse tunnel to the target agent. |
| **S15** | `lib/reversetunnel/` (agent side) | `lib/proxy/` (reverse tunnel server) | SSH (transport) carrying gRPC frames | The reverse-tunnel mesh: agents dial **out** to a proxy and the proxy uses the tunnel to deliver inbound requests. Trusted clusters reuse this mechanism between root and leaf. |
| **S16** | `lib/auth/trust/` (root Auth) | `lib/auth/trust/` (leaf Auth) | gRPC over reverse tunnel | Trusted-cluster RPC — exchanges signed certificates, lets root see leaf resources. |
| **S17** | `lib/srv/desktop/` (Go) | Browser session (via `lib/web/desktop`) | WebSocket carrying TDP (Teleport Desktop Protocol) frames | Browser-side WASM decoder (`ironrdp`) renders frames. |
| **S18** | `lib/srv/db/<engine>/` | Upstream database (Postgres / MySQL / Mongo / Redis / Snowflake / DynamoDB / …) | Native DB protocol (mTLS where supported); IAM auth where applicable (RDS IAM, Cloud SQL IAM, Azure AD) | Each engine implements `lib/srv/db/common.Engine`. |
| **S19** | `lib/srv/app/` | Upstream HTTP app | Plain HTTP/HTTPS, JWT in headers for app to verify (`lib/jwt`); AWS console SAML/STS dance for AWS, Azure CLI flow for Azure | App emits a per-user JWT the upstream can verify against `/.well-known/jwks.json`. |
| **S20** | `examples/chart/teleport-cluster/` (Helm) | `tool/teleport` binary, packaged in `Dockerfile` | Helm renders k8s manifests; `Dockerfile` ships the binary | This is how *most* users deploy. |
| **S21** | `lib/autoupdate/agent/` + `tool/teleport-update/` | A Teleport version registry (HTTPS) | HTTPS GET on a manifest URL; integrity-checked download | Auto-updater rolls the local binary forward without restart of the daemon's running connections. |
| **S22** | `docs/pages/` (Docusaurus MDX) | gen'd by `build.assets/tooling/cmd/{render-helm-ref,resource-ref-generator}` from in-tree source | build-time codegen | Helm reference and resource reference docs are generated from source — *edit the generators, not the rendered output*. |
| **S23** | `gen/preset-roles.json` | `docs/pages/reference/access-controls/roles.mdx` (via doc generator) | build-time codegen via `build.assets/dump-preset-roles/` | Preset roles are baked at build time so docs stay in sync. |
| **S24** | `e/` (enterprise submodule) | Various root-module files (`e_imports.go`, `webassets_embed_ent.go`, certain `lib/*` packages with build-tag-gated files) | Go build tags (`ent`, `oss`) | OSS build is self-contained; Enterprise build needs `e/` cloned and selects the `ent` build tag. |

---

## 3. Process-Level Topology (a real cluster)

A production Teleport cluster is usually 3+ `teleport` processes, each running a *subset* of services from the same binary:

| Role | Services enabled | Where it runs |
| --- | --- | --- |
| **Auth** | `auth_service` | Central — usually HA behind a network load balancer. Stateful (writes to backend). |
| **Proxy** | `proxy_service` | Edge — accepts user traffic. Stateless. Multiple replicas behind a network LB. |
| **Node** | `ssh_service` (`lib/srv/regular`) | Per host. |
| **Application** | `app_service` (`lib/srv/app`) | One or more per deployment. |
| **Database** | `db_service` (`lib/srv/db`) | One or more per deployment. |
| **Kubernetes** | `kubernetes_service` (`lib/kube`) | Usually deployed via Helm (`teleport-kube-agent`). |
| **Desktop** | `windows_desktop_service` (`lib/srv/desktop`) | On a host with line-of-sight to Windows hosts. |
| **Discovery** | `discovery_service` (`lib/srv/discovery`) | Polls AWS/Azure/GCP for new infra. |
| **Git** | `git_service` (`lib/srv/git`) | Proxies Git over Teleport. |
| **MCP** | `mcp_service` (`lib/srv/mcp`) | Proxies MCP servers. |
| **Relay** | `relay_service` | New transport relay layer (see `lib/relaytunnel/`, `lib/relaypeer/`, `proto/teleport/relaytunnel/`). |

All of these are wired together at startup by `lib/service/service.go` (the supervisor). Each service heart-beats inventory back to Auth (`lib/inventory/heartbeatv2.go`).

---

## 4. Build-Time Cross-Part Wiring

Steps a single `make full` runs across parts:

1. **Web** — `pnpm install` → `pnpm build-ui-oss` → produces `web/packages/teleport/dist/` (and `…/teleterm/dist/`).
2. **Rust WASM** — `make build-ironrdp-wasm` → invokes `wasm-pack` → outputs into `web/packages/shared/libs/ironrdp/` to be picked up by Vite during the web build (if not already built it would block step 1).
3. **Rust staticlib** — `make rdpclient` + `make rdpdecoder` → outputs `.a` files under `target/` for CGo linking.
4. **Proto codegen** — usually run as a precondition (`make grpc`) — outputs in `gen/` and `api/gen/`; checked into VCS.
5. **eBPF** — `make bpf-bytecode` builds `.o` files under `bpf/` (Linux only).
6. **Go** — `make build/teleport build/tctl build/tsh build/tbot build/teleport-update build/fdpass-teleport`. The `teleport` binary embeds the web assets (step 1) via `webassets_embed.go` and CGo-links the Rust libs (step 3) + BPF objects (step 5).
7. **Operator CRDs** — `make -C integrations/operator crd` regenerates from protos. Output → `integrations/operator/config/crd/bases/`.
8. **Terraform schema** — `make -C integrations/terraform gen` regenerates `tfschema/*`. Output → `integrations/terraform/tfschema/<resource>/v1/`.
9. **Audit-event docs** — `pnpm --filter=@gravitational/teleport event-reference` regenerates `docs/pages/reference/audit-events.mdx` from the TS service definitions.
10. **Preset roles** — `build.assets/dump-preset-roles/` regenerates `gen/preset-roles.json` from the Go source.

---

## 5. Why the Split Matters

- **`api/` is a separate Go module** so external consumers can `go get github.com/gravitational/teleport/api` without pulling the entire backend's 277-dep transitive closure. Anything backwards-incompatible in `api/` is a public API break.

- **`integrations/terraform` and `integrations/terraform-mwi` are separate modules** so they can release on independent cadences and avoid Terraform's notoriously narrow plugin-SDK version constraints leaking into the rest of the codebase.

- **`integrations/event-handler` is a separate module** because it's an external utility that's released as a separately-versioned binary (and runs on much smaller infrastructure than a Teleport cluster).

- **The Rust workspace** (in the root `Cargo.toml`) keeps `rdpclient`, `decoder`, and `ironrdp` (WASM) in lockstep on IronRDP version. Pinning is by **git rev** (`a0a3e750c…`), not crates.io, because IronRDP is moving fast and Teleport carries patches via the Devolutions fork.

- **`tool/fdpass-teleport` is its own standalone Cargo crate** (`[workspace]` declared empty in its `Cargo.toml`) so the unrelated FD-passing helper doesn't drag in the heavy IronRDP/BoringSSL workspace deps.

- **`e/` is a submodule, not a directory** so enterprise code stays in a private repo while the OSS source is wholly public and self-contained.

- **The web UI lives in pnpm workspace packages** instead of one app folder so the design system (`@gravitational/design`) and shared utilities (`@gravitational/shared`) can also be consumed by the Electron app (`@gravitational/teleterm`) and the e2e harness (`@gravitational/e2e`) without code duplication.
