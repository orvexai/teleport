# Technology Stack & Architecture Patterns

Snapshot of the dependency surface and architectural style of each part as of `2026-05-24`. The exact pinned versions are in `go.mod`, `pnpm-lock.yaml`, and `Cargo.lock`; this file documents the *shape* of the stack and the *load-bearing choices*.

---

## Part 1 — Backend (Go core daemon)

| Category | Choice | Version | Why it matters |
| --- | --- | --- | --- |
| Language | Go | 1.25.10 | Same toolchain for `teleport`, all `tool/*` binaries, `lib/`, `api/`, and the in-tree integrations (operator, access plugins, kube-agent-updater). |
| Build | `go build` + `make` + `common.mk` + `Makefile` | — | Top-level `Makefile` orchestrates Go, Rust (RDP), and webassets builds. `common.mk` defines shared targets. |
| Module layout | Two Go modules: root + `api/` | — | The `api/` module is published independently for third-party clients (`github.com/gravitational/teleport/api`). Root module *imports* `api` via a `replace` directive in dev. |
| RPC | gRPC + ConnectRPC | `google.golang.org/grpc v1.80.0`, `connectrpc.com/connect v1.19.1` | gRPC for internal Auth API (proto-defined); Connect for some HTTP/JSON-friendly endpoints. `gogo/protobuf v1.3.2` is still used in legacy paths. |
| Public IDL | `proto/teleport/**/*.proto` + `api/proto/**/*.proto` | 238 proto files total | Generated bindings live in `api/gen/proto/`, `gen/proto/`, `gen/go/`. `buf` (see `buf.yaml`, `buf-*.gen.yaml`) is the canonical generator. |
| Auth/identity stack | x509 + SSH certs + WebAuthn + SAML/OIDC + HSM (PKCS#11) | — | x/crypto, `ThalesIgnite/crypto11` (HSM), `go-piv/piv-go/v2` (PIV smartcards), `russellhaering/gosaml2`, `filippo.io/age` (audit encryption). |
| Backends | etcd / DynamoDB / Firestore / Postgres / Spanner / SQLite | — | See `lib/backend/{etcdbk, dynamo, firestore, postgres, spanner, lite}/`. Production is most commonly DynamoDB (with `lib/events/athena`) or Postgres. |
| Cloud SDKs | AWS SDK v2 (~60 services) + Azure SDK + GCP cloud libraries | — | The `go.mod` lists ~277 direct deps — most of the bulk is cloud SDK fan-out for resource discovery, DB IAM, KMS, etc. |
| K8s | `k8s.io/client-go`, `sigs.k8s.io/controller-runtime` | — | Used by `lib/kube/`, the operator (`integrations/operator`), and `kube-agent-updater`. |
| eBPF | `cilium/ebpf` + C BPF programs in `bpf/` | — | Process / network / file observability for SSH session recording (`lib/bpf/`). Compiled with `Dockerfile-bpf`. |
| Web / WebSocket | `gobwas/ws v1.4.0` | — | Used by the proxy and web UI for streaming session data. |
| Observability | OpenTelemetry (1.43) — traces, metrics, logs | — | `go.opentelemetry.io/otel/*` + `otlp/otlptrace[grpc]`. Tracing is wired through gRPC interceptors. |
| Logging / errors | `gravitational/trace` v1.5.4 (errors with stack/HTTP code mapping) + `log/slog` (stdlib) | — | All public APIs return `trace.Errorf(...)`-wrapped errors. Don't replace with `errors.New`. |
| CLI flag parsing | `alecthomas/kingpin/v2 v2.4.0` | — | Used by every `tool/*` binary. |
| Testing | `stretchr/testify`, `alicebob/miniredis/v2`, `jonboulle/clockwork` | — | `clockwork.NewFakeClock()` is the standard pattern for time control. |
| License (core) | AGPL-3.0 | — | Some sub-trees relicense (e.g. teleterm = Apache-2.0). Verify `LICENSE` before reusing. |

**Architecture pattern (backend):** **Service-oriented monolith** packaged as a single binary (`teleport`) with multiple runtime *services* (auth, proxy, ssh, kube, app, db, desktop, discovery, etc.) selected at startup by configuration. Communicates internally and externally via gRPC over mTLS. `lib/service` is the supervisor that wires services together.

The codebase deliberately ships **one binary per Teleport process role** *via configuration* (rather than per binary). The other binaries in `tool/` are clients:

- `tctl` — admin CLI (uses local auth socket or remote gRPC).
- `tsh` — user CLI / agent (SSH client, kube proxy, app/db/desktop proxy).
- `tbot` — Machine ID agent — issues short-lived certs to workloads.
- `teleport-update` — auto-updater (uses `lib/autoupdate`).
- `fdpass-teleport` — file-descriptor-passing helper used by `tsh ssh` for FIPS/PIV scenarios.

**Directory style:** flat `lib/<feature>/` (101 packages), `srv/` inside `lib/` for protocol-specific *servers*, `client/` for the corresponding clients. Tests are in the same package as the code (`*_test.go`).

---

## Part 2 — Integrations

A loose collection of *separate programs* that authenticate to a Teleport cluster as service identities and perform an out-of-band job. Not part of the core daemon.

| Sub-project | Module | Tech / Framework | Purpose |
| --- | --- | --- | --- |
| `operator/` | (shares root `go.mod`) | kubebuilder-style controller using `controller-runtime` | Reconciles Teleport resources (`roles`, `users`, `oidc_connectors`, `apps`, etc.) from Kubernetes CRDs. Joins the cluster via in-process `tbot`. |
| `terraform/` | own `go.mod` | `hashicorp/terraform-plugin-framework v0.10.0` + `terraform-plugin-sdk/v2 v2.10.1` | Terraform provider for Teleport resources. Code-generated from protos via `protoc-gen-terraform-*.yaml` configs. |
| `terraform-mwi/` | own `go.mod` | Same Terraform plugin framework | Standalone provider for Machine & Workload Identity (joined out from main `terraform/` for release independence). |
| `terraform-modules/` | (just generators + templates, no Go module) | — | Helper for generating Terraform module docs. |
| `event-handler/` | own `go.mod` | `alecthomas/kong` CLI, `peterbourgon/diskv/v3` (queue), `sethvargo/go-limiter` | Streams Teleport audit events to external SIEMs (Splunk, Datadog, Elastic, etc.) over Fluentd or HTTP. |
| `kube-agent-updater/` | (shares root `go.mod`) | controller-runtime | Watches a deployed `teleport-kube-agent` Helm chart and rolls it forward when a new Teleport version is released. |
| `access/{slack,jira,pagerduty,opsgenie,servicenow,msteams,mattermost,datadog,discord,email}/` | (share root `go.mod`) | Per-vendor SDKs; common code in `integrations/access/common/` | "Access plugins" — listen for `access_request` events and post approval prompts to chat / ITSM tools. |
| `integrations/lib/` | shared library | `embeddedtbot`, `plugindata`, `watcherjob`, etc. | Reusable plumbing used by multiple integrations. |

**Architecture pattern (integrations):** **Sidecar / external clients** authenticating via short-lived certs from an embedded `tbot`. Each plugin is its own binary, has its own Helm chart in `examples/chart/access/<plugin>/`, and is released independently.

---

## Part 3 — Web UI

| Category | Choice | Version |
| --- | --- | --- |
| Language | TypeScript | `^6.0.3` (with `@typescript/native-preview 7.0.0-dev` — the Go-based `tsgo` is used for `type-check`) |
| Runtime | Node | `^24` |
| Package manager | `pnpm` workspace | `pnpm@10.32.1` (locked via Corepack) |
| Workspace globs | `web/packages/*`, `e/web/*`, `e2e`, `e/e2e` | (defined in `pnpm-workspace.yaml`) |
| Framework | React | 19 (`react`, `react-dom`, `react-is` 19.x) |
| Routing | `react-router` | 7 |
| Data fetching | `@tanstack/react-query` | 5 |
| Forms | `react-hook-form` + `@hookform/resolvers` + `zod` | — |
| Styling | `styled-components` 6 + `@emotion/is-prop-valid` + `styled-system` (`design/`) | — |
| Build | `vite` | 8 |
| Linting | `oxlint`, `oxfmt` (NOT eslint/prettier — note `eslint-plugin-*` deps exist only for Storybook integration) | `oxlint ^1.62`, `oxfmt ^0.47` |
| Type check | `tsgo --build` (native TypeScript compiler) with `tsc --build` fallback (`type-check-legacy`) | — |
| Testing | `jest` 30 + `@testing-library/react` 16 + `@testing-library/jest-dom` + `msw` 2 + `playwright` 1.59 + `jest-websocket-mock` | — |
| Storybook | `storybook` 10.3 + `@storybook/react-vite` + `@storybook/addon-vitest` | — |
| Terminals | `@xterm/xterm` 6 + `@xterm/addon-fit` + `addon-image` + `addon-web-links` + `addon-webgl` + `addon-search` | — |
| Charts | `@nivo/bar` + `d3-scale` + `d3-time-format` | — |
| Editors | `@codemirror/{view,autocomplete,lang-sql}` + `@uiw/react-codemirror` | — |
| gRPC (browser) | `@grpc/grpc-js 1.14.3` + `@protobuf-ts/runtime` + `@protobuf-ts/runtime-rpc` | — |
| OpenTelemetry | `@opentelemetry/*` (traces only) | 2.7 series |
| WASM | `vite-plugin-wasm` for `ironrdp` (in `web/packages/shared/libs/ironrdp/`) | — |
| License | Apache-2.0 (web UI packages) | — |

**Workspace packages:**

- `@gravitational/teleport` — the actual web app served at `https://<cluster>/web`. Depends on `design`, `shared`, OpenTelemetry, `xterm`.
- `@gravitational/design` — design system (components, theme, icons). Depends only on `styled-system` and `@emotion/is-prop-valid`.
- `@gravitational/shared` — cross-package utilities (ace editor, semver, xterm-search, ironrdp WASM wrapper). The `build-wasm` script shells out to `make -C ../../../ build-ironrdp-wasm`.
- `@gravitational/build` — shared build tooling: babel presets, swc, vite/jest config, plugins.
- `@gravitational/teleterm` — the Electron app (documented separately as Part 4).
- Enterprise overlay: `e/web/*` (not present in this workspace).

**Architecture pattern (web):** **Component-based React app** with the design system as a separate workspace package. Server-side rendering is *not* used — assets are built and embedded into the Go binary via `webassets_*.go` (`embed.FS`). Server-side configuration is delivered through the proxy and consumed via React Query hooks.

---

## Part 4 — Teleterm (Teleport Connect — Electron desktop app)

| Category | Choice |
| --- | --- |
| Language | TypeScript (renderer + main) + Go (daemon in `lib/teleterm/`) |
| Shell | Electron `41.1.1` |
| Build | `electron-vite ^5.0.0` (dev + build) |
| Packaging | `electron-builder ^26.8.2` (config in `electron-builder-config.js`) |
| Update channel | `electron-updater ^6.8.3` |
| IPC to daemon | gRPC (`@grpc/grpc-js 1.14.3` + `@protobuf-ts/grpc-transport`) over a Unix socket / named pipe |
| PTY | `node-pty 1.2.0-beta.12` |
| TLS / certs | `node-forge ^1.4.0` |
| Logging | `winston ^3.19.0` |
| Drag & drop | `react-dnd ^14.0.4` + `react-dnd-html5-backend` |
| Terminals | `@xterm/xterm` + `@xterm/addon-fit` |
| License | Apache-2.0 |
| Product name | `Teleport Connect` |
| Outputs | `build/app/main/index.js` (Electron main), renderer assets, plus a packaged installer per platform. |

**Architecture pattern (teleterm):** **Three-process architecture.**

1. **Electron renderer** (TypeScript / React) — the UI.
2. **Electron main process** (TypeScript / Node) — window management, OS integration, secure storage, talks to (3) over gRPC.
3. **`tshd` daemon** — a Go process implemented in `lib/teleterm/`. It owns cluster connections, certificate management, and proxying. It's spawned by the Electron main process and exposes a gRPC API to the renderer (through the main process).

The renderer never speaks directly to the Teleport cluster — every network call goes through `tshd`. The protos for the daemon API live under `proto/teleport/lib/teleterm/`.

---

## Part 5 — Rust RDP workspace

| Crate | Path | Crate type | Purpose |
| --- | --- | --- | --- |
| `rdp-client` | `lib/srv/desktop/rdp/rdpclient/` | `staticlib` (linked via CGo) | Native RDP client that runs inside the Go `desktop_service`. Negotiates RDP, handles smartcard redirection (`iso7816`), clipboard, audio (`ironrdp-rdpsnd`), drive redirection (`ironrdp-rdpdr`). Optional `fips` feature pulls BoringSSL FIPS. |
| `rdp-decoder` | `lib/srv/desktop/rdp/decoder/` | `staticlib` + `lib` | RDP graphics decoder shared between the native client and other tooling. |
| `ironrdp` | `web/packages/shared/libs/ironrdp/` | `cdylib` (WASM via `wasm-bindgen`) | In-browser RDP rendering library. Decodes RDP graphics in the browser for the web-based desktop session player. |

| Category | Choice |
| --- | --- |
| Edition | 2021 |
| Resolver | 2 (workspace) |
| LTO | `thin` (release), `off` (dev) |
| RDP library | `IronRDP` (Devolutions) pinned to git rev `a0a3e750c9e4ee9c73b957fbcb26dbc59e57d07d` |
| TLS | `rustls 0.23` with `aws-lc-rs` provider, OR BoringSSL (`boring`, gravitational fork rev `99897308a`) under the `fips` feature |
| Crypto | `picky`, `picky-asn1-*`, `rsa`, `iso7816` |
| Auth (SSPI) | `sspi 0.16.1` (Kerberos/NTLM) |
| Async runtime | `tokio 1.52` (full) |
| FFI | `cbindgen 0.29.2` (build script generates C headers consumed by `lib/srv/desktop/rdp/` Go code) |
| WASM build path | `make build-ironrdp-wasm` (invoked from `web/packages/shared` `build-wasm` script) |
| License | AGPL-3.0 |

**Architecture pattern (rust-rdp):** **Two FFI shapes for one library.** The same upstream `ironrdp-*` crates are used (a) by a `staticlib` linked into the Go desktop service and (b) by a `cdylib` compiled to WASM for the browser. The `decoder` crate is shared between the two.

---

## Cross-Cutting Notes

- **Single source of truth for protos**: every gRPC API is defined under `proto/` (top-level) or `api/proto/`. Generated Go, TS, and gogo-Go bindings live in `gen/` and `api/gen/`. `buf` is the linter and generator (`buf.yaml`, `buf-*.gen.yaml`).
- **`gravitational/trace` is not optional**: error wrapping with this package is enforced by codebase convention; it maps Go errors to HTTP/gRPC codes and carries stack traces.
- **Time is faked everywhere**: `jonboulle/clockwork.FakeClock` is used in tests instead of `time.Now()`.
- **Generated code is checked in.** `gen/` and `api/gen/` are part of the repo. Run `make grpc` to regenerate.
- **The `e/` enterprise overlay is a git submodule** (`teleport.e`). Some files in the root (e.g., `e_imports.go`, `webassets_embed_ent.go`) reference symbols from it. Without the submodule, the enterprise build will not compile, but the OSS build is self-contained.
