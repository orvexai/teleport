# Architecture — Backend (Go core daemon)

> **Scope:** `api/`, `lib/`, `tool/`, `proto/`, `gen/`, `integration/`, `bpf/` and root-level Go files (`constants.go`, `version.go`, `metrics.go`, `e_imports.go`, `webassets_embed*.go`).
>
> **For deep dives** — this doc is an *orientation*. Detailed file:line citations live in:
> - `findings/backend-auth-security.md` — auth / authz / crypto / device trust / SSO / MFA / join (1,455 lines, ~200 file:line citations)
> - `findings/backend-protocols.md` — SSH / DB / App / Desktop / Kube / Git / MCP / Proxy / reverse tunnel (1,004 lines)
> - `findings/backend-data-storage.md` — backend KV / services / cache / events / audit (854 lines)
> - `findings/backend-api-surface.md` — gRPC and REST APIs, public Go SDK (573 lines)

---

## 1. What This Part Is

A single Go binary (`teleport`) that runs *roles* — `auth_service`, `proxy_service`, `ssh_service`, `app_service`, `db_service`, `kubernetes_service`, `windows_desktop_service`, `discovery_service`, `git_service`, `mcp_service`, `relay_service` — selected by config. Plus a fleet of client / admin CLIs (`tsh`, `tctl`, `tbot`, `teleport-update`, `fdpass-teleport`) and a *separate public Go module* `api/` for external automation.

The defining mental model: **Teleport is a certificate-authority service**. The Auth Service is the only issuer of x509 + SSH certificates. Every other component (proxies, agents, users, bots) holds a short-lived cert signed by an Auth CA. All in-band traffic is mTLS between cert-holding peers. (See `findings/backend-auth-security.md` §1 for the request lifecycle.)

---

## 2. Process Topology

A typical production cluster runs three flavours of the same binary:

| Process | Services | Stateful? | Listens on |
| --- | --- | --- | --- |
| **Auth** | `auth_service` | Yes (writes to `lib/backend`) | mTLS gRPC `:3025` |
| **Proxy** | `proxy_service` | No | HTTPS `:3080` (web/API), SSH `:3023`, peer `:3021`, reverse tunnel `:3024`, DB ALPN, app ALPN, etc. |
| **Agent** (SSH/DB/App/Kube/Desktop/Discovery/Git/MCP) | one or more `*_service`s | No (state is in Auth) | Outbound reverse-tunnel only |

All are wired by **`lib/service/service.go::TeleportProcess`** (the supervisor). Each service registers itself, heartbeats inventory back to Auth via `lib/srv/heartbeatv2.go` + `lib/inventory/`, and shuts down via the shared lifecycle.

---

## 3. Major Subsystems

### 3a. Auth Service — `lib/auth/`

The gRPC server that mints all certificates and owns all cluster state. Subdirectories follow the **per-resource v1-service pattern**: a focused `Service` type implementing one proto-defined gRPC service for one resource:

```
lib/auth/
├── grpcserver.go                Registers ~55 modern services + the legacy AuthService
├── methods.go                   Login flow (authenticateUserLogin → GenerateUserCerts)
├── auth.go                      Server struct; generateCert is the universal cert-minting chokepoint
├── init.go                      Bootstrap (CA generation, presets, migrations)
├── register.go                  Agent registration (join)
├── role_service.go              Roles RPC
├── webauthn/  webauthncli/  webauthntypes/  webauthnwin/  touchid/   MFA
├── join/                        Join methods (token, ec2, iam, kubernetes, github, azure, gcp, oracle, …)
├── machineid/                   tbot
├── keystore/  keygen/           CA key material (HSM, software, AWS KMS, GCP KMS, Azure KV)
├── trust/                       Trusted-cluster RPC
├── recordingencryption/         Age-encrypted session recordings (RFD 0042)
├── moderation/  loginrule/  scopes/                                   Policy primitives
├── accessmonitoringrules/  notifications/  presence/  dbobject/  okta/
│                                v1 per-resource services (37 of them)
└── …                            ~30 more sub-packages
```

The **single chokepoint** patterns to know:
- All user certs are minted by `lib/auth/auth.go::generateCert` (`auth.go:3610`). Inside: lock check → hardware-key attestation → suite resolution → SSH+TLS dual signing.
- All authorization decisions flow through `lib/authz/permissions.go::authorizer.Authorize` (`permissions.go:427`). It converts a TLS peer cert into a populated `authz.Context` with an `AccessChecker` (`lib/services/access_checker.go:52`).

**Decomposition story:** the monolithic legacy `proto.AuthService` (274 RPCs) is gradually being broken into per-resource modern `teleport.<resource>.v1.<Resource>Service` services. The `lib/auth/<resource>/` packages implement these and the matching `api/client/<resource>/` packages wrap them on the client side. Both old and new co-exist.

### 3b. Authorization — `lib/authz/`

Where the policy enforcement lives. `AccessChecker.CheckAccess`, `CheckAccessToRule`, `CheckAccessToResource`. Role traits are interpolated by `lib/services/role.go::ApplyTraits`. Locks are checked via `verifyLocksForUserCerts`. **Don't reimplement role evaluation** elsewhere — go through `AccessChecker`.

> **Critical security note** (from `findings/backend-auth-security.md`): the env var `TELEPORT_UNSTABLE_DISABLE_MFA_ADMIN_ACTIONS=yes` at `lib/authz/permissions.go:569` is a documented kill-switch that disables admin-action MFA cluster-wide. Marked as a temporary escape hatch with a "should be removed" TODO. Treat any code path interacting with it with extreme care.

### 3c. Cryptosuites — `lib/cryptosuites/`

The pluggable algorithm registry. Drives whether a CA key is RSA / ECDSA / Ed25519, whether it's HSM-backed, whether FIPS is on. `cryptosuites.GenerateUserSSHAndTLSKey()` (`suites.go:444`) is the canonical user-keygen entry point.

### 3d. Storage — `lib/backend/` + `lib/services/` + `lib/cache/`

A three-layer stack:

```
                ┌─────────────────────────────────────┐
gRPC client →   │           lib/auth/<resource>v1     │   per-resource gRPC service
                ├─────────────────────────────────────┤
                │           lib/cache/                 │   in-memory mirror (watcher fan-out)
                ├─────────────────────────────────────┤
                │           lib/services/local/        │   resource CRUD (UpsertRole, GetUser, etc.)
                ├─────────────────────────────────────┤
                │           lib/backend/               │   KV abstraction
                ├─────────────────────────────────────┤
                │   etcd / dynamo / firestore / pgbk / lite / memory / kubernetes
                └─────────────────────────────────────┘
```

Concrete backends (`lib/backend/<driver>/`) register via `init()` + `MustRegister`. Keys follow a slash-prefixed convention (`/users/foo`, `/web/sessions/<id>`, `/cert_authorities/…`). The cache (`lib/cache/cache.go`) initializes via `Init`, has explicit *high-volume* kinds (`cache.go:105`) that get sharded fanout so heartbeat noise doesn't drown config watchers, and exposes a `WatchKind` preset per consumer role (`ForAuth`, `ForProxy`, etc.).

> **Surprise**: there is **no Spanner backend** in `lib/backend/`. `lib/srv/db/spanner` is the *Database Access engine* (i.e. Teleport proxying a Spanner database), not a storage driver. "Postgres" registers twice (`postgresql` + `postgres` names map to the same driver in `pgbk/pgbk.go:42-48`).

### 3e. Audit & Recording — `lib/events/`

Two flows on the same package:
- **Audit events** — discrete structured events (`Session.Start`, `UserLogin`, etc., defined in `api/proto/teleport/legacy/types/events/events.proto`) emitted to `athena`, `dynamoevents`, `firestoreevents`, `pgevents`, or filesystem.
- **Session recordings** — full session I/O (SSH, DB queries, RDP frames) emitted to `s3sessions`, `gcssessions`, `azsessions`, or `filesessions`. Recordings can be age-encrypted (`lib/auth/recordingencryption/`, RFD 0042).

### 3f. Protocol services — `lib/srv/`

The "things Teleport proxies." Each subdirectory is an *agent role*:

| Sub-package | What it proxies | Entry point |
| --- | --- | --- |
| `lib/srv/regular/` | SSH (native Teleport node) | `sshserver.go::Server` |
| `lib/srv/forward/` | SSH (forwarding to upstream OpenSSH) | `sshserver.go::Server` |
| `lib/srv/db/<engine>/` | Postgres, MySQL, Mongo, Redis, Snowflake, Clickhouse, DynamoDB, OpenSearch, ElasticSearch, SQL Server, Oracle, Cassandra, Spanner | `server.go::Server` + `db/common.Engine` per engine |
| `lib/srv/app/` | HTTP apps (with AWS console / Azure CLI / GCP flows) | `server.go::Server` |
| `lib/srv/desktop/` | RDP (Windows desktops) | `windows_server.go::WindowsService` |
| `lib/srv/discovery/` | Cloud resource discovery → registers nodes/apps/dbs | `discovery.go::Server` |
| `lib/srv/git/` | Git protocol | |
| `lib/srv/mcp/` | Model Context Protocol | |
| `lib/srv/ingress/` | PROXY protocol / IP propagation at the proxy | |
| `lib/srv/alpnproxy/` | **ALPN/SNI multiplexer** at the proxy: one TCP port hosts SSH+HTTPS+DB+RDP | `proxy.go:384::dispatch` + `common/protocols.go:32` for the protocol enum |
| `lib/srv/transport/transportv1/` | gRPC transport layer | |
| `lib/srv/server/` | Generic heartbeat infra + installer endpoints | |

Common cross-cutting machinery sits at the root of `lib/srv/`: `ctx.go::Server` interface (`ctx.go:128`), `exec.go` (process exec for SSH sessions), `authhandlers.go`, `monitor.go`, `sess.go`, `session_control.go`, `sessiontracker.go`, `termhandlers.go`, `term.go`, `usermgmt.go`.

### 3g. Proxy & Tunnel — `lib/proxy/`, `lib/reversetunnel/`, `lib/relay*`, `lib/multiplexer/`

Four overlapping but distinct tunnel packages:

| Package | Purpose | Protocol |
| --- | --- | --- |
| `lib/reversetunnel/` | Classic agent ↔ proxy tunnel | SSH-on-TCP carrying gRPC |
| `lib/relaytunnel/` | New transport layer | yamux-over-TLS |
| `lib/relaytransport/` | SNI-dispatch so one port hosts both gRPC and yamux | |
| `lib/relaypeer/` | Relay ↔ relay forwarding | |
| `lib/multiplexer/` | PROXY-protocol detection + protocol sniffing | (multiplexer.go:556) |
| `lib/proxy/peer/` | Proxy ↔ Proxy peering for connection routing | gRPC; QUIC variant in `proto/teleport/quicpeering/` |

Reverse-tunnel role dispatch happens at `lib/reversetunnel/srv.go:817`. Trusted-cluster traffic re-uses this mesh — root ↔ leaf is "just" a long-lived reverse tunnel.

### 3h. Web HTTP API — `lib/web/`

The ~220-route HTTP+WebSocket API consumed by the React UI and `tsh`-via-proxy. Master router in `lib/web/apiserver.go` (registered on an embedded `httprouter.Router`). Per-route auth via middleware wrappers: `WithAuth`, `WithClusterAuth`, `WithClusterAuthWebSocket`, `WithLimiter`. A `/v1` prefix shim transparently strips the version. WebSockets use **gorilla/websocket** (`lib/web/terminal/terminal.go`); **gobwas/ws** is only used in `lib/web/conn_upgrade.go` for the ALPN connection-upgrade endpoint.

The site map at a glance (full table in `findings/backend-api-surface.md` §5):

- `/v1/webapi/*` — browser UI endpoints (login, sessions, resource lookup, terminal, recording playback, MFA challenges, …)
- `/v1/sessions`, `/v1/apps`, `/v1/databases`, `/v1/desktops`, `/v1/kube_clusters` — resource lookup
- `/v1/scripts`, `/v1/scripts/node-join/install.sh` — node-join installer
- `/webapi/sites/{site}/...` — trusted-cluster-aware sub-routes
- `/.well-known/jwks.json` — JWKS for App Access JWTs

### 3i. tbot / MachineID — `lib/tbot/` (library) + `tool/tbot/` (CLI)

The bot identity system: gets a short-lived cert via a join method, then continuously renews and writes destinations (kubeconfig, identity file, SSH config, AWS profile, SPIFFE SVID, etc.). Used by **every integration** for authentication. Embedded inside the operator via `integrations/lib/embeddedtbot/`.

### 3j. Teleterm daemon — `lib/teleterm/`

Even though Teleterm is "Part 4" (Electron), the **Go daemon** that backs it lives in `lib/teleterm/`. It's a separate gRPC server (`apiserver/`) that owns cluster connections, certs, and proxying for the desktop app. Bootstraps via `teleterm.go` → `daemon/` → `apiserver/` registering `TerminalService`, `VnetService`, `AutoUpdateService`. Wraps `lib/client` via `clusters/` facades. Cross-references `architecture-teleterm.md`.

### 3k. Auto-update — `lib/autoupdate/`, `tool/teleport-update/`

The agent-side updater (`lib/autoupdate/agent/`) checks a manifest URL, downloads, verifies, swaps the binary. `tool/teleport-update` is the standalone binary form. Multiple update sources: RFD-184 proxy-driven + RFD-109 HTTP-channel.

### 3l. eBPF — `bpf/` + `lib/bpf/` + `lib/cgroup/`

Three CO-RE eBPF programs (`bpf/enhancedrecording/{command,disk,network}.bpf.c`) attached via tracepoints / fentry/fexit / kprobes. Output goes through ring buffers + task-local storage. `lib/bpf/` (Go) loads them via `cilium/ebpf` + bpf2go-generated bindings. `lib/cgroup/` scopes BPF programs to cgroup-v2 sessions; **slated for deprecation / removal in v20** (replaced by per-task storage).

### 3m. VNet — `lib/vnet/`

A TUN-device transparent proxy: registers a virtual network and routes traffic to Teleport-proxied apps/dbs without per-app port forwards. Subsystems: `daemon/`, `db/`, `dns/`, `diag/`, `polkit/` (Linux privilege escalation prompt), `systemdresolved/`.

### 3n. Public Go SDK — `api/`

A *separate Go module* (`api/go.mod`). External consumers (`go get github.com/gravitational/teleport/api`) get only this module — not the entire backend's 277-dep transitive closure. Client construction: `client.New(ctx, Config)` (`api/client/client.go:191`). The client tries up to 5 connection methods in parallel (`authConnect`, `tunnelConnect`, `proxyConnect`, `tlsRoutingConnect`, `tlsRoutingWithConnUpgradeConnect`) — first to succeed wins. Credentials live in `api/client/credentials.go`: `LoadTLS`, `LoadKeyPair`, `LoadIdentityFile`, `LoadProfile`, `NewDynamicIdentityFileCreds`.

### 3o. Protobuf IDL — `proto/` + `api/proto/`

Two trees, generated by `buf`:

- `api/proto/teleport/` → `api/gen/proto/go/` (public, in the api/ module). Modern `teleport.<service>.v1` packages plus a `legacy/` subtree for the original monolithic protos (gogoproto-extended).
- `proto/teleport/` → `gen/proto/go/` (internal). Sub-trees for teleterm daemon, web app, VNet, multiplexer, storage, QUIC peering, relay tunneling.
- `proto/accessgraph/` + `proto/prehog/` → external-facing streams (access graph + usage telemetry).

Code-gen pipeline: 4 buf templates — `buf-go.gen.yaml` (modern), `buf-gogo.gen.yaml` (legacy gogo), `buf-connect-go.gen.yaml` (prehog only), `buf-ts.gen.yaml` (TS). Driven by `build.assets/genproto.sh` via `make grpc/host`. A custom lint plugin (`build.assets/tooling/cmd/buf-plugin-linters/`) enforces RFD-0153's `PAGINATION_REQUIRED` rule on list methods.

### 3p. CLIs — `tool/`

Six binaries, all Go except one:

| Binary | What it is |
| --- | --- |
| `tool/teleport/` | The daemon entry. Sub-commands: `start`, `configure`, `status`, `install`, `debug`, `scp`, `sftp`, `join openssh`, plus hidden re-exec subcommands. |
| `tool/tctl/` | Admin CLI. Plugin pattern via `CLICommand` interface; 40+ registered commands (`get/create/edit/rm/auth/tokens/users/roles/recordings/sso/…`). |
| `tool/tsh/` | User CLI. One ~4500-line kingpin parser with 74+ subcommands (`login`, `ssh`, `app`, `db`, `kube`, `proxy`, `request`, `mfa`, `daemon`, `ports`, `scan`, `status`, `headless`, `mcp`, `gh`, `recordings`, …). |
| `tool/tbot/` | MachineID CLI. Sub-commands: `start`, `configure`, `init`, `keypair`, `proxy`, `migrate`, `spiffe-inspect`, `tpm`. |
| `tool/teleport-update/` | Auto-updater. 10-min lock to prevent racing. |
| `tool/fdpass-teleport/` | **Rust** binary. Uses `nix::sendmsg` + `SCM_RIGHTS` to pass file descriptors to `tbot ssh-multiplexer`. Not in the Cargo workspace. |

---

## 4. Where State Lives (cluster resources)

Every cluster-state resource follows this lifecycle:

1. **Proto type** at `api/proto/teleport/<resource>/v1/<resource>.proto` (modern) or `api/proto/teleport/legacy/types/types.proto` (legacy).
2. **Go type** at `api/types/<resource>.go`.
3. **Storage adapter** at `lib/services/local/<resource>.go` (calls `lib/backend`).
4. **Cache collection** at `lib/cache/<resource>.go` (replicates from Auth via watcher).
5. **gRPC service** at `lib/auth/<resource>v1/<resource>v1/service.go` (modern) or as a method on the legacy `AuthService` (older).
6. **Client wrapper** at `api/client/<resource>/<resource>.go`.

See `findings/backend-data-storage.md` §6 for the full resource catalog (roles, users, nodes, kube_servers, db_servers, app_servers, cert_authorities, trusted_clusters, OIDC/SAML/GitHub connectors, locks, session_trackers, web_sessions, web_tokens, windows_desktops, access_lists, access_requests, installers, MCP servers, git_servers, discovery_configs, bots, cluster_config_*, ~60 in total).

> **Naming quirk**: `KindMCP` is **not** a stored kind. MCP servers are stored as `KindApp` with `SubKindMCP` (see `api/types/app.go:314-505`). 

---

## 5. Build Tag Matrix

The backend binary is built with a dense set of conditional features (see `findings/build-deploy-ops.md` §2 and root `Makefile`):

| Tag | Effect |
| --- | --- |
| `fips` | Switches Rust `rdpclient` to BoringSSL + GOFIPS140; disables some non-compliant code paths. |
| `bpf` | Compiles in eBPF session-recording support (Linux amd64/arm64 only). Auto-disabled otherwise. |
| `desktop_access_rdp` | Compiles in the CGo bridge to the Rust RDP staticlib. Auto-disabled on architectures where Rust isn't built. |
| `pam` | Compiles in PAM integration (libpam). |
| `libfido2` | Touch-ID / FIDO2 support (`tsh`/`tbot`). |
| `touchid`, `piv`, `vnetdaemon` | OS-specific feature compilation. |
| `sessionhelper_embed`, `webassets_embed` | Whether the auxiliary binary / web assets are embedded. |
| `grpcnotrace` | Disable gRPC tracing instrumentation. |
| `kustomize_disable_go_plugin_support` | Reduce binary size by disabling kustomize Go plugins. |

The Makefile auto-disables BPF and RDPCLIENT on `ARM`/`386` and on `FIPS+arm64`.

---

## 6. Observability

- Single `--diag-addr` flag (`tool/teleport/common/teleport.go:173`) serves `/metrics`, `/healthz`, `/readyz`, `/debug/pprof/*`, plus log-level controls.
- ~150 Prometheus metric names enumerated in root `metrics.go` under the `teleport` namespace.
- Tracing is **configured via `tracing_service` block in `teleport.yaml`** (consumed by `lib/observability/tracing/`). There is *no* `--observability` CLI flag.
- All gRPC traffic is instrumented via OpenTelemetry interceptors.

---

## 7. Integration Seams

(See `integration-architecture.md` for the full cross-part seam catalogue. Backend-relevant entries:)

| Seam | This part ↔ | Channel |
| --- | --- | --- |
| S1 | `lib/web/*` ↔ `web/packages/teleport/src/services/*.ts` (browser) | HTTPS REST + WebSockets |
| S5 | `lib/srv/desktop/rdp/rdpclient/*.go` ↔ Rust `rdp-client` crate | CGo + cbindgen header |
| S7 | `lib/bpf/*.go` ↔ `bpf/*.bpf.c` | `cilium/ebpf` + bpf2go bindings |
| S9-S13 | `lib/auth/*` (gRPC) ↔ `integrations/*` (operator, terraform, access plugins, event-handler, kube-agent-updater) | gRPC, MachineID auth |
| S14 | `lib/proxy/peer/` ↔ other Teleport proxies | gRPC (or QUIC via `proto/teleport/quicpeering/`) |
| S15 | `lib/reversetunnel/` (agent) ↔ `lib/proxy/` | SSH transport carrying gRPC |
| S16 | `lib/auth/trust/` (root) ↔ leaf cluster `lib/auth/trust/` | gRPC over reverse tunnel |

---

## 8. Testing Conventions

- Tests live in the same package as the code (`*_test.go`).
- `make test-go` runs the suite via `gotestsum` (from `build.assets/tools/gotestsum/`).
- `make test-api` runs the separate API module's tests.
- `make test-bpf` requires Linux + root (loads real BPF programs).
- Use `clockwork.NewFakeClock()` to control time. Don't read `time.Now()` directly.
- `tool/teleport/testenv.MakeTestServer(…)` spins an in-process auth+proxy+node cluster for integration tests.
- `integration/helpers/` has the higher-level harness used by `integration/*_test.go`.

---

## 9. Pointer Index (Most-Used Concepts → File:Line)

| Concept | Location |
| --- | --- |
| Service supervisor | `lib/service/service.go` |
| User cert issuance | `lib/auth/auth.go:3610::generateCert` |
| User login | `lib/auth/methods.go:136::authenticateUserLogin` |
| Authorization | `lib/authz/permissions.go:427::authorizer.Authorize` |
| Role evaluation | `lib/services/role.go:1061::RoleSet`, `role.go:494::ApplyTraits` |
| TLS middleware | `lib/authz/middleware.go` |
| gRPC server registration | `lib/auth/grpcserver.go:NewGRPCServer` |
| Web HTTP routes | `lib/web/apiserver.go::NewAPIServer` |
| Web WebSockets | `lib/web/terminal/terminal.go` |
| Public Go SDK client | `api/client/client.go:191::New`, `connect()` at L274 |
| Backend KV interface | `lib/backend/backend.go::Backend` |
| Service-CRUD layer | `lib/services/local/*.go` |
| Cache | `lib/cache/cache.go::Init` |
| Audit events spine | `lib/events/*.go` |
| SSH node server | `lib/srv/regular/sshserver.go` |
| Database engine registry | `lib/srv/db/common/engines.go`, registrations in `lib/srv/db/server.go:84-96` |
| Database connection dispatch | `lib/srv/db/server.go:1161::HandleConnection`, `:1299::dispatch` |
| Desktop service | `lib/srv/desktop/windows_server.go:608`, `:754`, `:1083` |
| ALPN dispatch | `lib/srv/alpnproxy/proxy.go:384::dispatch` + `common/protocols.go:32` |
| Protocol multiplexer | `lib/multiplexer/multiplexer.go:556` |
| Reverse tunnel dispatch | `lib/reversetunnel/srv.go:817` |
| Inventory heartbeat | `lib/srv/heartbeatv2.go` |
| Diagnostics endpoint | `lib/service/diagnostic.go`, flag in `tool/teleport/common/teleport.go:173` |
| Prometheus metrics | root `metrics.go` |
| MFA admin-action kill switch | `lib/authz/permissions.go:569` (env: `TELEPORT_UNSTABLE_DISABLE_MFA_ADMIN_ACTIONS`) |
| Decision-service (WIP) | `lib/decision/service.go:105` |

For everything else, the AI-pointer indexes at the end of each `findings/backend-*.md` file are the canonical lookup.
