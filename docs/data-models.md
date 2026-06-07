# Data Models

> **Quick orientation.** Teleport's cluster state is a collection of *resources* (Role, User, Node, Database, App, …) stored in a pluggable KV backend and served through a layered abstraction. This document maps the layers and lists the resources. Deep details (one-by-one driver behaviour, every cache collection, every audit event) are in [`findings/backend-data-storage.md`](./findings/backend-data-storage.md).

---

## 1. The Storage Stack

```
            ┌─────────────────────────────────────────┐
gRPC client │           lib/auth/<resource>v1          │   per-resource gRPC service
            ├─────────────────────────────────────────┤
            │           lib/cache/                     │   in-memory mirror + watcher fan-out
            ├─────────────────────────────────────────┤
            │           lib/services/local/            │   resource CRUD on top of KV
            ├─────────────────────────────────────────┤
            │           lib/backend/ (interface)       │   KV abstraction
            ├─────────────────────────────────────────┤
            │   etcd / dynamo / firestore / pgbk /     │
            │   lite (SQLite) / kubernetes / memory    │
            └─────────────────────────────────────────┘
```

Each layer adds a concern:

- **`lib/backend/`** — typed `Backend` interface (`backend.go`) exposing `Create / Put / Get / Update / Delete / DeleteRange / GetRange / Items / NewWatcher / KeepAlive / AcquireLock` operations on `Item{Key, Value, Expires, Revision}`. Drivers register via `init()` + `MustRegister`.
- **`lib/services/`** — provides typed CRUD per resource (e.g. `GetRole`, `UpsertRole`, `DeleteUser`). `local/` is the production implementation that uses `lib/backend` for storage.
- **`lib/cache/`** — Auth-side in-memory mirror. Subscribes to all backend events, fans out to client watchers, serves queries without round-tripping the backend.
- **`lib/auth/<resource>v1/`** — the gRPC service that *clients* talk to. Reads through the cache; writes through `lib/services/local` and emits an audit event.

---

## 2. Concrete Backend Drivers

| Driver | Package | Best for | Gotchas |
| --- | --- | --- | --- |
| **etcd** | `lib/backend/etcdbk/` | Multi-node HA without cloud dependencies | Lease management is delicate; cluster watch can fall behind on huge resource volumes |
| **DynamoDB** | `lib/backend/dynamo/` | AWS-native HA (most common production deployment) | Eventual consistency; the dynamo-backed `lib/events/dynamoevents` audit store is separate |
| **Firestore** | `lib/backend/firestore/` | GCP-native | Quota limits on writes; uses `lib/events/firestoreevents` for audit |
| **Postgres** | `lib/backend/pgbk/` | Generic SQL deployments. Registers under **both** `postgresql` and `postgres` names (`pgbk/pgbk.go:42-48`). | Schema migrations are run at startup |
| **SQLite** | `lib/backend/lite/` | Dev / single-node demo (default in `teleport configure`) | Not for prod with > 1 Auth instance |
| **Kubernetes** | `lib/backend/kubernetes/` | Used by `tbot` running inside a pod to persist state to a Secret | Single-node only |
| **Memory** | `lib/backend/memory/` | Tests + ephemeral demos | No durability |

> **Important quirk**: there is **no Spanner storage backend**. `lib/srv/db/spanner` is a *protocol engine* for Database Access (i.e., Teleport proxying a Cloud Spanner database), not a storage driver for Teleport state itself. If you see "Spanner" in the docs, double-check whether it means "use Spanner *for* Teleport state" (does not exist) or "let users access *their* Spanner databases" (yes, via DB Access).

Auxiliary packages:

- `lib/backend/test/` — driver conformance harness (every driver must pass).
- `lib/backend/backendtest/` — test fixtures.
- `lib/backend/backendmetrics/` — Prometheus instrumentation for backend operations.
- `lib/backend/migration/` — schema migrations (especially used by SQL drivers).
- `lib/backend/wrapper/` — middleware (retries, tracing) wrapping the underlying driver.

---

## 3. Key Model

Keys are slash-prefixed strings. Examples (sampled — full enumeration in `lib/services/local/*.go`):

```
/users/<name>                                   – User accounts
/roles/<name>                                   – Role definitions
/web/sessions/<id>                              – Browser session
/web/tokens/<id>                                – Web sign-up / invite tokens
/cluster_config/auth_preference                 – auth_preference singleton
/cluster_config/networking_config               – cluster_networking_config singleton
/cluster_config/session_recording_config        – session_recording_config singleton
/cert_authorities/<type>/<cluster>              – x509 + SSH CAs (user/host/jwt/openssh/…)
/trusted_clusters/<cluster>                     – Inbound trust
/nodes/<namespace>/<name>                       – Registered SSH nodes
/app_servers/<namespace>/<name>                 – Application servers
/db_servers/<namespace>/<name>                  – Database servers
/kube_servers/<namespace>/<name>                – Kubernetes servers
/windows_desktops/<host>/<name>                 – Windows desktops
/windows_desktop_services/<name>                – Windows desktop services
/git_servers/<name>                             – Git servers
/sessions/<id>                                  – SessionTracker
/locks/<name>                                   – Locks (KindLock)
/oidc/<name>, /saml/<name>, /github/<name>      – SSO connectors
/access_requests/<id>                           – Access requests
/access_lists/<name>                            – Access lists
/notifications/<id>                             – Notifications
/installers/<name>                              – Installer scripts
/discovery_config/<name>                        – Discovery configurations
/bot/<name>                                     – MachineID bots
/spiffe/federated_trust_domains/<name>          – SPIFFE federation
/integrations/<name>                            – AWS OIDC / Azure / GitHub integrations
/mcp_servers/<name>                             – MCP servers
/login_rules/<name>                             – Login rules
/scopes/<name>                                  – Scoped RBAC
```

A `KeepAlive` is a separate key associated with a TTL — used for heartbeats so a missing keep-alive auto-expires the parent resource.

---

## 4. Resource Catalog

There are roughly **60 resource kinds**. The full table is in [`findings/backend-data-storage.md` §6](./findings/backend-data-storage.md). Categorised:

### 4a. Identity & Access

| Kind | Type | Notes |
| --- | --- | --- |
| `user` | `UserV2` | `api/types/user.go` |
| `role` | `RoleV6` | `api/types/role.go` (most actively versioned resource — current `V6`) |
| `cert_authority` | `CertAuthorityV2` | Sub-types: user, host, jwt, openssh, db, kube, saml_idp, oidc_idp, spiffe |
| `oidc`, `saml`, `github` | `OIDCConnectorV3`, `SAMLConnectorV2`, `GithubConnectorV3` | SSO connectors |
| `lock` | `LockV2` | Active locks (immediate session termination) |
| `login_rule` | `LoginRuleV1` | Trait-rewriting at login |
| `access_list`, `access_request`, `access_monitoring_rule` | (various) | Identity Governance |
| `user_login_state`, `user_preferences`, `user_tasks` | (various) | Per-user state |
| `bot` | (proto-only) | MachineID bots |
| `trusted_cluster` | `TrustedClusterV2` | Inbound trust |

### 4b. Workload Resources (things Teleport proxies)

| Kind | Type | Notes |
| --- | --- | --- |
| `node` | `ServerV2` | SSH nodes |
| `app_server` + `app` | `AppServerV3`, `AppV3` | HTTP apps. `KindMCP` is **not** stored — MCP servers are `KindApp` with `SubKindMCP` |
| `db_server` + `db` | `DatabaseServerV3`, `DatabaseV3` | Databases |
| `kube_server` + `kube_cluster` | `KubernetesServerV3`, `KubernetesClusterV3` | K8s clusters |
| `windows_desktop` + `windows_desktop_service` | `WindowsDesktopV3`, `WindowsDesktopServiceV3` | RDP-accessible hosts |
| `dynamic_windows_desktop` | (proto v1) | Dynamic desktop resources |
| `git_server` | (proto v1) | Git repos |
| `mcp_server` | (stored as `KindApp` + `SubKindMCP`) | MCP servers |

### 4c. Cluster Configuration

| Kind | Type | Notes |
| --- | --- | --- |
| `auth_preference` | `AuthPreferenceV2` (singleton) | MFA modes, connector defaults |
| `cluster_networking_config` | (singleton) | Proxy listener mode, tunnel intervals |
| `session_recording_config` | (singleton) | Recording mode (`node`, `proxy`, `off`) |
| `cluster_audit_config` | (singleton) | Audit storage selection |
| `cluster_maintenance_config` | (singleton) | Update windows |
| `installer` | `InstallerV1` | Custom installer scripts |
| `health_check_config` | (proto v1) | Health-check periodicity |
| `discovery_config` | (proto v1) | Discovery service configs |
| `autoupdate_config`, `autoupdate_version` | (proto v1) | Managed updates |
| `db_object_import_rule`, `db_object` | (proto v1) | DB Object Discovery |
| `app_auth_config` | (proto v1) | App access auth config |
| `vnet_config` | (proto v1) | VNet config |

### 4d. Identity Security / Reporting

| Kind | Notes |
| --- | --- |
| `secreport`, `secreport_state` | Security reports |
| `crownjewel` | "Crown jewel" tagging for high-value resources |
| `accessgraph` (stream, not stored) | Access Graph events |
| `provision_token` | Join tokens |

### 4e. Session State

| Kind | Notes |
| --- | --- |
| `session_tracker` | Ephemeral, in-memory, controls active sessions |
| `web_session`, `web_token`, `app_session`, `snowflake_session`, `saml_idp_session` | Various session types |

---

## 5. Cache

`lib/cache/cache.go` is the in-memory mirror that sits in front of every read path inside Auth. It subscribes to a `WatchKind` set from the backend, builds resource-typed collections (`lib/cache/<resource>.go`, ~40+ files), and fans out events to client watchers.

Key facts:

- **Cache state machine.** `Init` → `OK` → (`NotReady` ↔ `Closed`). Clients can subscribe at any state but only get fully synced data once `OK`.
- **High-volume kinds** (`cache.go:105`) get their own sharded fanout so heartbeat noise (nodes, db_servers, app_servers, kube_servers) doesn't drown out config watchers.
- **Watcher presets per role.** `ForAuth`, `ForProxy`, `ForApp`, `ForDatabase`, … each define which kinds that role subscribes to.

The auth gRPC server is *never* called for reads that the cache can answer — the cache is what handles those.

---

## 6. Audit & Session Recording

> **Distinction is critical**: *audit events* are discrete structured events with a fixed schema (e.g. `Session.Start`, `UserLogin.Login`, `Role.Create`). *Session recordings* are full streams of session I/O (SSH terminal bytes, DB queries, RDP frames). They use *different storage* and have different retention semantics.

### 6a. Audit event stores (`lib/events/<store>/`)

| Store | Best for |
| --- | --- |
| **`athena`** | AWS — events go to S3 + Glue + Athena for query. Most common cloud setup. |
| **`dynamoevents`** | AWS — events in DynamoDB. Simpler but limited query. |
| **`firestoreevents`** | GCP. |
| **`pgevents`** | Postgres. |
| **`mockevents`** | Testing. |
| Filesystem (built into `lib/events`) | Local dev. |

Events are defined in `api/proto/teleport/legacy/types/events/events.proto`. Codes enumerated in `lib/events/codes.go`.

### 6b. Session recording stores (`lib/events/<store>/` and `lib/events/filesessions/`)

| Store | Best for |
| --- | --- |
| **`s3sessions`** | AWS S3 (with KMS encryption — see RFD 0042). |
| **`gcssessions`** | GCS. |
| **`azsessions`** | Azure Blob Storage. |
| **`filesessions`** | Local disk. |

Recording can be **age-encrypted** end-to-end (`lib/auth/recordingencryption/`) so even cluster operators can't read recordings without the recipient key.

### 6c. Recording machinery

| Package | Role |
| --- | --- |
| `lib/events/recorder/` | Session-recorder orchestration |
| `lib/events/eventstest/` | Test helpers |
| `lib/player/` | Session-recording playback (timeline, scrubbing) |

---

## 7. Inventory & Heartbeats

`lib/srv/heartbeatv2.go` (note: `lib/srv`, **not** `lib/inventory` as one might guess) implements the v2 heartbeat protocol on top of `lib/inventory/`'s Inventory Control Stream. Each service registers itself with Auth, then heartbeats periodically — agent state (load, version, OS, hostname, labels) flows up to Auth which makes it queryable via the unified-resources endpoint.

---

## 8. Cross-References

- Deep file:line citations: [`findings/backend-data-storage.md`](./findings/backend-data-storage.md).
- gRPC API on top: [`api-contracts.md`](./api-contracts.md) + [`findings/backend-api-surface.md`](./findings/backend-api-surface.md).
- How types flow into the Operator + Terraform provider: [`architecture-integrations.md` §3](./architecture-integrations.md).
- Audit event encryption (RFD): `rfd/0042-s3-kms-encryption.md`.
- Streaming events (RFD): `rfd/0002-streaming.md`, `rfd/0019-event-iteration-api.md`.
