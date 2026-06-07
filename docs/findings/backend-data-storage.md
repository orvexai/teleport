# Teleport — Backend, Storage, Cache, Audit (findings)

This document covers Teleport's storage stack: the pluggable KV `lib/backend`,
the resource CRUD layer in `lib/services/local`, the in-memory mirror in
`lib/cache`, the audit-event spine in `lib/events`, the inventory protocol in
`lib/inventory`, and the supporting resource model in `api/types`.

Everything below was derived from sampling the actual source on `master`
(commit `4d580653b4`). Paths are absolute. Where a file is "load-bearing" the
exact line is given.

---

## 1. Storage layering

```
+---------------------------------------------------------------+
|  gRPC clients (tsh, tctl, agents, proxies, web UI)            |
+---------------------------------------------------------------+
                              |                                 ^
                              v                                 |
+---------------------------------------------------------------+
|  lib/auth (auth server)  — gRPC API, RBAC, sessions,          |
|  cert signing, etc. Talks to the Cache (read) and             |
|  services.* (write).                                          |
+---------------------------------------------------------------+
                              |                                 ^
                              v                                 |
+---------------------------------------------------------------+
|  lib/cache (Cache)        — event-driven in-memory mirror of  |
|  most KV resources. Subscribes to Events on top of            |
|  services/local, fans events back out to clients.             |
|     ForAuth / ForProxy / ForNode / ForKubernetes /            |
|     ForApps / ForDatabases / ForDiscovery / ForOkta /         |
|     ForWindowsDesktop / ForRemoteProxy presets.               |
+---------------------------------------------------------------+
                              |  read-through on miss/not-ok    |
                              v                                 |
+---------------------------------------------------------------+
|  lib/services/local       — typed CRUD on top of backend.     |
|  Each file owns one resource family and a key prefix:         |
|    access.go        roles/, locks/                            |
|    presence.go      nodes/, appServers/, kubeServers/, …      |
|    users.go         web/users/, web/sessions/, …              |
|    trust.go         authorities/, …                           |
|    dynamic_access.go access_requests/                         |
|    …                                                          |
|  Many resources use the generic Service[T] wrapper            |
|  (lib/services/local/generic/generic.go).                     |
|  EventsService (events.go) translates KV events to typed      |
|  resource events for the Cache.                               |
+---------------------------------------------------------------+
                              |                                 ^
                              v                                 |
+---------------------------------------------------------------+
|  lib/backend (Backend)    — pluggable KV store.               |
|  Operations: Create / Put / Update / Get / Items / GetRange / |
|  Delete / DeleteRange / KeepAlive / ConditionalUpdate /       |
|  ConditionalDelete / AtomicWrite / NewWatcher.                |
|  Wrappers: report.go (Reporter, metrics), wrap.go (Wrapper),  |
|  sanitize.go (key sanitization), atomicwrite.go.              |
+---------------------------------------------------------------+
                              |
                              v
+---------------------------------------------------------------+
|  Concrete drivers (each `MustRegister`s itself in init()):    |
|    etcdbk, dynamo, firestore, pgbk (postgres/postgresql),     |
|    lite (sqlite/dir), kubernetes (k8s secret), memory.        |
+---------------------------------------------------------------+
```

Canonical files for this layering:

- KV interface: `/home/daniel/repos/teleport/lib/backend/backend.go:48`
  (`type Backend interface`).
- Driver registry: `/home/daniel/repos/teleport/lib/backend/registry.go:34`
  (`MustRegister`).
- Service-on-top-of-backend example: `/home/daniel/repos/teleport/lib/services/local/access.go`
  (`AccessService` lines 40–86 use `backend.NewKey`, `Items`, `DeleteRange`).
- Generic CRUD wrapper: `/home/daniel/repos/teleport/lib/services/local/generic/generic.go:119`
  (`type Service[T Resource]`).
- Backend → typed events: `/home/daniel/repos/teleport/lib/services/local/events.go:67`
  (`(*EventsService).NewWatcher`).
- Cache: `/home/daniel/repos/teleport/lib/cache/cache.go:529`
  (`type Cache`); per-resource collections in
  `/home/daniel/repos/teleport/lib/cache/collections.go` and the per-kind
  files (`auth_server.go`, `node.go`, `role.go`, etc.).

---

## 2. Backend implementations

Driver name = `storage.type` in `teleport.yaml`.
All registered via `backend.MustRegister(name, factory)` in `init()`.

| Driver | Name (yaml) | Key file | Good for | Notable gotchas |
|---|---|---|---|---|
| In-memory B-tree | `in-memory` | `/home/daniel/repos/teleport/lib/backend/memory/memory.go` | Tests, single-process embedded use, cache mirror (`Mirror: true`). Uses google `btree` plus a min-heap for TTL expiry. | Not persisted — process exit loses state. In mirror mode revisions are preserved instead of regenerated (line 67). |
| etcd | `etcd` | `/home/daniel/repos/teleport/lib/backend/etcdbk/etcd.go` | HA self-hosted clusters. Most "classical" Teleport backend; rich watcher support, native CAS. | TTL handled via etcd leases. To avoid lease explosion the backend buckets TTLs to the nearest 10s and caches lease IDs (lines 1032-1090). Watcher reconnect/reset is non-trivial. |
| DynamoDB | `dynamodb` | `/home/daniel/repos/teleport/lib/backend/dynamo/dynamodbbk.go` | AWS deployments, especially Teleport Cloud. Uses DynamoDB Streams for the watcher. Auto-scaling and PITR are first-class config options. | Watchers are **stream-based**, not push: events arrive via `dynamodbstreams` polling (`PollStreamPeriod`). `GetRange` does `ConsistentRead: true` to mitigate eventual consistency (lines 619, 1178). The cache's `fetchAndWatch` comments call this out explicitly (`cache.go:1190-1204`): on Dynamo the cache may have to replay events from scratch because the stream lacks a strong revision number. Also handles `ConditionalCheckFailedException` (line 1208) for CAS. |
| Firestore | `firestore` | `/home/daniel/repos/teleport/lib/backend/firestore/firestorebk.go` | GCP-hosted clusters. | Uses doc field names `key`/`value`/`expires`/`revision`/`timestamp`. A separate `purgeInterval` ticker GC-s expired records (line 286). Index creation status is polled. There is a one-off migration in `firestore/migration.go`. |
| PostgreSQL (incl. CockroachDB / AlloyDB) | `postgresql` (alias `postgres`) | `/home/daniel/repos/teleport/lib/backend/pgbk/pgbk.go` | Modern self-hosted preference, especially with Cockroach for HA. | Two registrations (lines 42-48): both `postgresql` and `postgres` resolve to the same driver. Change feed uses `wal2json` logical decoding (`pgbk/wal2json.go`). Separate connection strings for the main client and the change feed (`ChangeFeedConnString`). Has its own expiry sweeper (`ExpiryInterval`, default 30s). |
| SQLite / filesystem | `sqlite` (alias `dir`) | `/home/daniel/repos/teleport/lib/backend/lite/lite.go` | Single-binary deployments, tests, agent local state. Uses pure-Go `modernc.org/sqlite`. | Synchronous mode defaults to `FULL`. Uses `busy_timeout` (10s default) rather than Go-side mutexes for write serialization. `dir` is just a friendlier alias for `sqlite`. |
| Kubernetes secret | `kubernetes` (only programmatically, not as `storage.type`) | `/home/daniel/repos/teleport/lib/backend/kubernetes/kubernetes.go` | Agent state inside a k8s Pod (Helm-managed Teleport). Stores items as entries in a single Kubernetes `Secret`'s `data` map. | Requires env vars `KUBE_NAMESPACE`, `TELEPORT_REPLICA_NAME` (per-replica) or `RELEASE_NAME` (shared). Single global mutex for concurrent ops (line 116). Two flavours: per-agent `state` secret and `shared-state` secret. |

There is **no Spanner backend** — only a Spanner _database protocol_
implementation in `lib/srv/db/spanner` (database access feature).

Cross-cutting wrappers in `lib/backend`:

- `backend.Reporter` (`report.go:89`) wraps any backend to add Prometheus
  request/latency metrics.
- `backend.Wrapper` (`wrap.go:37`) lets tests inject read errors.
- `backend.Sanitizer` (`sanitize.go`) enforces UTF-8 / control-char rules.
- `backend.AtomicWrite` (`atomicwrite.go`) — multi-key CAS up to
  `MaxAtomicWriteSize = 64` actions per batch.
- Conformance suite for new drivers: `/home/daniel/repos/teleport/lib/backend/test/suite.go`.

---

## 3. Key model

The KV store keys are slash-separated UTF-8 byte strings, modelled by
`backend.Key` (`/home/daniel/repos/teleport/lib/backend/key.go:27`):

```
const Separator = '/'

NewKey("roles", "admin", "params")        -> "/roles/admin/params"
ExactKey("roles")                         -> "/roles/"
RangeEnd(NewKey("/roles/admin"))          -> /roles/admiñ  (last byte +1)
```

A `Key` carries both the textual form and the parsed components, so prefix
math, range scans (`GetRange`, `Items` with `StartKey`/`EndKey`/`Descending`),
and prefix watches are cheap. `ExactKey` appends a trailing slash so a prefix
match doesn't accidentally also match siblings (e.g. `/roles/` won't match
`/roles_archive/`).

The convention is **every resource family has its own top-level prefix**
constant defined in `lib/services/local/<file>.go`. A short tour (all from
`grep`-ing for `Prefix\s*=`):

```
roles                     -> "roles"            access.go
locks                     -> "locks"            access.go
params                    -> "params"           access.go (suffix marker for the JSON blob)

users (local)             -> "web/users"        users.go  (webPrefix+usersPrefix)
sessions (web)            -> "web/sessions"     users.go
attempts/pwd/oidc/saml/   -> "web/users/<u>/…"  users.go
github (per user)         -> "web/users/<u>/connectors/github/…"

nodes                     -> "nodes/<ns>/<id>"  presence.go
namespaces                -> "namespaces"       presence.go (rare; ns is a synthetic concept now)
authservers/proxies       -> "authservers" / "proxies"  presence.go
servers                   -> "servers"          presence.go
appServers                -> "appServers/<ns>/<host>/<name>"   presence.go
kubeServers               -> "kubeServers/<host>/<name>"       presence.go
databaseServers           -> "databaseServers"  presence.go
dbServices                -> "databaseService"  databaseservice.go
db                        -> "db"               databases.go
apps                      -> "applications"     apps.go
kubernetes                -> "kubernetes"       kube.go

windowsDesktops           -> "windowsDesktop"   desktops.go
windowsDesktopServices    -> "windowsDesktopServices"  presence.go
dynamicWindowsDesktops    -> "dynamicWindowsDesktop"   dynamic_desktops.go
linuxDesktops             -> "linux_desktop"          linux_desktop.go

trustedclusters / authorities  -> "trustedclusters" / "authorities"  trust.go
remoteClusters            -> "remoteClusters"  presence.go
tunnelConnections         -> "tunnelConnections"  presence.go
reverseTunnels            -> "reverseTunnels"  presence.go

access_requests           -> "access_requests"  dynamic_access.go
access_request_promotions -> "access_request_promotions"  dynamic_access.go
access_list / member / review -> "access_list" etc.  access_list.go
access_monitoring_rule    -> "access_monitoring_rule"  access_monitoring_rules.go
plugin_data               -> "plugin_data"     plugin_data.go
plugins                   -> "plugins"         plugins.go

tokens (provisioning)     -> "tokens"          provisioning.go
scoped_token              -> "scoped_token"    scoped_tokens.go
cluster_configuration     -> "cluster_configuration/{name,static_tokens,authentication,…}"  configuration.go

session_tracker           -> "session_tracker"  sessiontracker.go
delegation_session        -> "delegation_session"  delegation_session.go
headless_authentication   -> "headless_authentication/users/<u>/<id>"  headlessauthn.go

integrations              -> "integrations"    integrations.go
okta_import_rule / okta_assignment   -> "okta_import_rule" / "okta_assignment"  okta.go
discovery_config          -> "discovery_config"  discoveryconfig.go
crown_jewel / saml_idp_service_provider / …                       (one file per kind)

instances                 -> "instances"          inventory.go
bot_instance              -> "bot_instance"       bot_instance.go
spiffe_federation         -> "spiffe_federation"  spiffe_federations.go
workload_identity[_x509…] -> "workload_identity…" workload_identity*.go
sigstore_policy           -> "sigstore_policy"    sigstore_policy.go

git_server                -> "git_server"     git_server.go
relay_servers             -> "relay_servers"  presence.go
beams / beams_alias       -> "beams" / "beams_alias"  beam.go
appAuthConfig             -> "app_auth_config"  appauthconfig.go
recording_encryption      -> "recording_encryption"  recording_encryption.go
rotated_key               -> "recording_encryption_rotated"  recording_encryption.go
external_audit_storage    -> "external_audit_storage"  externalauditstorage.go
health_check_config       -> "health_check_config"  health_check_config.go
auto_update_*             -> "auto_update_config", "auto_update_version",
                             "auto_update_agent_rollout", "auto_update_agent_report",
                             "auto_update_bot_instance_report"   autoupdate.go

identity_center/...       -> "identity_center/{accounts,permission_sets,
                              principal_assignments,account_assignments}"  identitycenter.go
inference_models, inference_secrets,
inference_policies, retrieval_model  -> summarizer.go

cluster-alerts            -> "cluster-alerts"  status.go
alert-ack                 -> "alert-ack"       status.go
notification              -> notifications.go
.locks/<scope>            -> backend-internal mutex (see lock.go)
```

Conventions of note:

- Many object families store the canonical JSON blob under a `params`
  *suffix* (e.g. `/roles/admin/params`). Other components under the same
  resource key (such as `/roles/admin/access_request`) can then live
  alongside without clashing with the parent. The watcher parsers use
  `key.HasSuffix(backend.NewKey("params"))` to distinguish the canonical
  record from sibling sub-keys (see `events.go:1009`).
- "Namespaced" historically meant `<resource>/<namespace>/<name>`, but
  namespaces are effectively deprecated — only `apidefaults.Namespace`
  ("default") is used. The prefix is still in keys for backward
  compatibility (e.g. `appServers/<ns>/...`).
- Backend-internal lock keys live under `.locks/` (`lib/backend/lock.go:35`)
  — the leading dot keeps them out of normal resource ranges.
- Pagination keys for items implementing `HostID` are
  `<hostID>/<name>` (`backend.go:297`).
- Revisions: `backend.CreateRevision()` mints a UUID per write; backends
  that have native versioning (etcd `ModRevision`, Postgres txn id, etc.)
  use that instead. `BlankRevision = uuid.Nil.String()` is used for legacy
  records pre-revisioning.

---

## 4. Watchers (Subscribe / NewWatcher)

There are three layers of "watcher":

1. **Backend watcher** (`backend.NewWatcher` in
   `/home/daniel/repos/teleport/lib/backend/backend.go:106`). Per-prefix
   subscription to raw KV events:

   ```go
   type Event struct {
       Type types.OpType   // OpInit / OpPut / OpDelete
       Item Item
   }
   ```

   Each driver implements this differently:
   - **etcd**: native `Watch` API plus a worker pool, results funneled
     through a concurrent queue (`etcdbk/etcd.go`).
   - **dynamo**: poll-based shard reader on DynamoDB Streams every
     `PollStreamPeriod` (default 1s).
   - **firestore**: query change listener.
   - **pgbk**: dedicated logical-replication connection using `wal2json`
     (`pgbk/wal2json.go`).
   - **lite, memory, kubernetes**: in-process fan-out via
     `backend.CircularBuffer` (`/home/daniel/repos/teleport/lib/backend/buffer.go`).
     The buffer enforces a per-watcher backlog grace period
     (`DefaultBacklogGracePeriod = 59s`,
     `DefaultCreationGracePeriod = 3*DefaultBacklogGracePeriod`,
     `defaults.go:34`).

   All watchers emit an `OpInit` first to signal that the subscription is
   live; consumers must wait for this before they assume they're caught
   up. Watcher `QueueSize` defaults to `DefaultBufferCapacity = 1024`.

2. **Typed events** (`types.Events`, implemented by
   `services/local.EventsService` —
   `/home/daniel/repos/teleport/lib/services/local/events.go:53`).
   Translates raw KV events to `types.Event{Op, Resource types.Resource}`
   via per-kind parsers. A single typed `Watch` can request multiple kinds
   (`types.WatchKind`), each with optional filters
   (e.g. `LoadSecrets`, `CertAuthorityFilter`, `SubKind`).

   The parser layer enforces "no overlapping prefixes" — if two parsers'
   prefixes are subsets of each other the watcher refuses to start
   (`events.go:315-322`). Use `backend.ExactKey` to avoid that pitfall.

3. **Service-level watchers** (`lib/services/watcher.go`).
   `resourceWatcher` keeps an up-to-date local snapshot of a resource set:
   on every watcher reset it does `getResourcesAndUpdateCurrent` (a full
   fetch) and then `processEventsAndUpdateCurrent` on each event. The
   generic implementation is `GenericWatcher[T,R]`
   (`watcher.go:817`); specific watchers (`LockWatcher`,
   `CertAuthorityWatcher`, `AccessRequestWatcher`, `OktaAssignmentWatcher`,
   `HealthCheckConfigWatcher`, etc.) layer Subscribe/filter semantics on
   top.

4. **Cache fan-out** (`lib/services.FanoutV2` —
   `/home/daniel/repos/teleport/lib/services/fanoutv2.go:68`). Used by the
   Cache to deliver events to many subscribers without copying per
   subscriber. Built around a shared
   `/home/daniel/repos/teleport/lib/utils/fanoutbuffer` ring; each watcher
   holds a "cursor" that catches up at its own pace, subject to the
   configured `GracePeriod` (default 59s).

   The Cache splits fan-out across two pools:
   - one "main" fanout for normal resources, and
   - a sharded `RoundRobin[*FanoutV2]` for **high-volume** kinds
     (`KindNode`, `KindAppServer`, `KindDatabaseServer`,
     `KindDatabaseService`, `KindWindowsDesktopService`, `KindKubeServer`,
     `KindDatabaseObject`, `KindGitServer` — see
     `cache.go:105`). This isolates the heartbeat firehose from
     watchers that only care about config-style resources.

---

## 5. Cache (`lib/cache`)

The Cache is the in-memory mirror that every Teleport process (auth,
proxy, node, kube, app, database, discovery, okta, windows desktop,
remote proxy) runs to serve reads without hitting the backend.

### Layout

- Entry point: `/home/daniel/repos/teleport/lib/cache/cache.go:529`
  (`type Cache struct`).
- Configuration presets: `ForAuth`, `ForProxy`, `ForRemoteProxy`,
  `ForNode`, `ForKubernetes`, `ForApps`, `ForDatabases`,
  `ForWindowsDesktop`, `ForDiscovery`, `ForOkta`. Each preset registers a
  different set of `WatchKind`s (e.g. nodes don't watch `AccessList`).
- Per-resource cache collections live in
  `/home/daniel/repos/teleport/lib/cache/collections.go` and the
  matching per-kind file (`auth_server.go`, `role.go`, `node.go`,
  `access_list.go`, `kube.go`, etc.). Each is a
  `collection[T, IndexType]` over a `store[T, I]`
  (`lib/cache/collection.go:29`, `lib/cache/store.go`).
- A short canonical example: `/home/daniel/repos/teleport/lib/cache/auth_server.go:32`
  builds a `collection[types.Server, authServerIndex]` with a
  `fetcher` (calls `services.Presence.ListAuthServers`), a `store` and
  a `headerTransform` for delete events.

### State machine

Three flags collectively make up the "OK / NotReady / Closed" state:

| Cache state | `closed` | `ok` | `initC` closed? | `firstTimeInitC` closed? |
|---|---|---|---|---|
| Initialising (first time) | false | false | no | no |
| NotReady (post-reset, before next sync) | false | false | yes (err is set if first attempt failed) | maybe (only if a prior generation succeeded) |
| OK | false | true | yes | yes |
| Closed | true | (last value) | yes | yes |

Key methods:

- `setInitError(err)` (`cache.go:593`) sets `initErr` and closes `initC`
  once; on `err == nil` also closes `firstTimeInitC` and bumps the
  `cache_health{component=...}` gauge to 1.
- `setReadStatus(ok, confirmedKinds)` (`cache.go:617`) flips `ok` while
  holding `rw.Lock()`; readers acquire `rw.RLock()` via
  `acquireReadGuard` (`cache.go:631`). The lock is held for the whole
  read, ensuring that a reset can't happen mid-read.
- `generation` is an `atomic.Uint64` bumped each time we go OK; used by
  cache miss/fallback paths.
- `closed` is an `atomic.Bool`.

### Fetch / watch loop

`fetchAndWatch` (`cache.go:1205`) is the heart of the cache. Each
iteration:

1. Opens a new typed watcher on `c.Events` for the configured
   `Watches` (`WatcherInitTimeout` enforced via `timer`).
2. Waits for `OpInit` and reads the per-kind ack
   (`types.WatchStatus.GetKinds()`) — this is "partial success" mode:
   server-side may reject some kinds (e.g. unknown in the cache's
   version), and the cache logs which kinds were rejected.
3. Calls `c.fetch(ctx, confirmedKindsMap)` to do a full fetch per
   collection.
4. Sets read status to false, applies the fetched snapshot, sets read
   status to true, bumps `generation`, calls `setInitError(nil)`.
5. Broadcasts `OpInit` to every fan-out so derivative caches downstream
   know to release their queued events.
6. Enters the event loop:
   - `relativeExpiryInterval` (only on auth) periodically purges expired
     nodes (`performRelativeNodeExpiry`) — covers the case where the
     backend's expiry mechanism is slow.
   - Each backend event is routed to the right `collection` and applied
     in-place, then emitted on the appropriate fan-out (high-volume or
     low-volume).

The Dynamo eventual-consistency comment in `cache.go:1190-1204` is
worth reading: because Dynamo's stream lacks a strong revision number,
the cache may replay events 1, 2, 3 in order to converge — there is no
"start from revision X" optimization.

### Cache miss / fallback semantics

`acquireReadGuard` returns `{cacheRead: true, store}` only when both
`cache.ok` is true **and** the requested kind is in `confirmedKinds`.
Otherwise it returns `{cacheRead: false}` and the caller falls back to
the live `services.*` interface (the underlying backend client). This
makes the cache fail-safe: when it's degraded, callers still get
correct (if slower) reads.

### Events emitted by the cache

For ops/test code, the cache emits its own lifecycle `Event`s on
`c.notify` channels: `WatcherStarted`, `WatcherFailed`, `Reloading`,
`RelativeExpiry` (all in `cache.go` near line 911).

---

## 6. Resources catalog

The kind constants are defined in
`/home/daniel/repos/teleport/api/types/constants.go`. The "Where" column
points to the canonical Go type. Most resources are namespaced only in
name — the API uses `apidefaults.Namespace = "default"` for everything
except a small number of legacy ssh-related types.

| Kind constant | String | Go type / file | NS? | Notes |
|---|---|---|---|---|
| `KindUser` | `user` | `api/types/user.go:62` (`User` interface, `UserV2` struct) | n | Local DB user; `web/users/<name>/params`. |
| `KindRole` | `role` | `api/types/role.go:67` (`Role`, `RoleV6` etc., currently default `V8`) | n | Stored at `roles/<name>/params`. Has presets (`PresetEditorRole`, …) in `lib/services/presets.go`. |
| `KindNode` | `node` | `api/types/server.go:36` (`Server`, `ServerV2`) | y (`nodes/<ns>/<id>`) | High-volume heartbeat. |
| `KindAuthServer` | `auth_server` | same `Server` interface | n | `authservers/`. |
| `KindProxy` | `proxy` | same `Server` interface | n | `proxies/`. |
| `KindGitServer` | `git_server` | same `Server` interface (`server.go:191`) | n | `git_server/`; sub-kinds: `github`. |
| `KindRelayServer` | `relay_server` | server | n | `relay_servers/`. |
| `KindKubeServer` | `kube_server` | `api/types/kubernetes_server.go` (`KubernetesServer`) | n (`kubeServers/<host>/<name>`) | Heartbeat for kube proxy. |
| `KindKubernetesCluster` | `kube_cluster` | `api/types/kubernetes.go:36` (`KubeCluster`) | n | The bookkeeping resource itself. |
| `KindAppServer` | `app_server` | `api/types/appserver.go:36` (`AppServer`) | y (`appServers/<ns>/<host>/<name>`) | Heartbeat. |
| `KindApp` | `app` | `api/types/app.go:39` (`Application`, `AppV3`) | n | `applications/`. Includes MCP apps — `KindMCP = "mcp"` and `IsAppMCP`/`SubKindMCP` (`app.go:314-505`). |
| `KindDatabaseServer` | `db_server` | `api/types/databaseserver.go` | n | `databaseServers/`. |
| `KindDatabase` | `db` | `api/types/database.go:41` (`Database`, `DatabaseV3`) | n | `db/`. |
| `KindDatabaseService` | `db_service` | `api/types/databaseservice.go` | n | `databaseService/`. Heartbeat by `db_service` agents. |
| `KindDatabaseObject` | `db_object` | `lib/srv/db/...` plus `api/types/database_permissions.go` | n | High-volume; cached in low-volume fanout. |
| `KindDatabaseObjectImportRule` | `db_object_import_rule` | `databaseobjectimportrule.go` | n | |
| `KindWindowsDesktop` | `windows_desktop` | `api/types/desktop.go:330` | n | `windowsDesktop/`. |
| `KindWindowsDesktopService` | `windows_desktop_service` | `api/types/desktop.go:37` | n | Heartbeat. |
| `KindDynamicWindowsDesktop` | `dynamic_windows_desktop` | `api/types/desktop.go:160` | n | Per-user enroll. |
| `KindLinuxDesktop` | `linux_desktop` | `api/gen/proto/.../linuxdesktop/v1` | n | Newer proto-defined resource. |
| `KindCertAuthority` | `cert_authority` | `api/types/authority.go` (`CertAuthority`, `CertAuthorityV2`) | n | `authorities/<type>/<cluster>`. `LoadSecrets` controls whether private keys are included. Types: host, user, db, jwt, openssh, saml_idp, oidc_idp, spiffe. |
| `KindCertAuthorityOverride` | `cert_authority_override` | `api/types/cert_authority_override.go` | n | RFD for CA overrides. |
| `KindTrustedCluster` | `trusted_cluster` | `api/types/trustedcluster.go:31` | n | `trustedclusters/<name>`. |
| `KindRemoteCluster` | `remote_cluster` | `api/types/remotecluster.go` | n | `remoteClusters/`. |
| `KindReverseTunnel` | `tunnel` | `api/types/tunnel.go` | n | `reverseTunnels/`. |
| `KindTunnelConnection` | `tunnel_connection` | `api/types/tunnelconn.go` | n | `tunnelConnections/`. |
| `KindOIDCConnector` | `oidc` | `api/types/oidc.go:38` | n | `web/users/.../connectors/...` (sso). |
| `KindSAMLConnector` | `saml` | `api/types/saml.go:34` | n | |
| `KindGithubConnector` | `github` | `api/types/github.go:37` | n | |
| `KindOIDCRequest` / `SAMLRequest` / `GithubRequest` | `oidc_request` etc. | `api/types/oidc.go` etc. | n | Short-TTL flow state. |
| `KindLock` | `lock` | `api/types/lock.go:30` | n | `locks/`. Special "remote" variant under `locks/<clusterName>/<lockName>` (see `access.go:413`). |
| `KindAccessRequest` | `access_request` | `api/types/access_request.go` | n | `access_requests/<id>/params`. |
| `KindAccessMonitoringRule` | `access_monitoring_rule` | `api/types/accessmonitoringrule/access_monitoring_rule.go` | n | |
| `KindAccessList` | `access_list` | `api/types/accesslist/accesslist.go` | n | Composite resource: `KindAccessListMember`, `KindAccessListReview`. Uses convert/legacy for marshaling. |
| `KindSessionTracker` | `session_tracker` | `api/types/session_tracker.go:56` | n | `session_tracker/<id>`. Short TTL. |
| `KindWebSession` | `web_session` | `api/types/session.go:55` | n | Multi-subkind: `web_session`, `app_session`, `snowflake_session` (kept distinct in the cache via `WatchKind.SubKind`). |
| `KindWebToken` | `web_token` | `api/types/session.go:535` (`WebToken`) | n | OIDC-style bearer tokens for the web UI. |
| `KindSnowflakeSession` | `snowflake_session` | subkind of web session | n | |
| `KindAppSession` | `app_session` | subkind of web session | n | |
| `KindToken` | `token` | `api/types/provisioning.go` (`ProvisionToken`) | n | `tokens/<name>`. Join tokens. |
| `KindScopedToken` / `KindStaticScopedTokens` | `scoped_token` / `static_scoped_tokens` | `api/gen/proto/.../scopes/joining/v1` | n | Newer scoped join-token model. |
| `KindStaticTokens` | `static_tokens` | `api/types/statictokens.go` | n | Singleton. |
| `KindClusterAuthPreference` | `cluster_auth_preference` | `api/types/authentication.go` | n | Singleton; name `cluster-auth-preference`. |
| `KindClusterNetworkingConfig` | `cluster_networking_config` | `api/types/networking.go` | n | Singleton. |
| `KindClusterAuditConfig` | `cluster_audit_config` | `api/types/audit.go` | n | Singleton. |
| `KindSessionRecordingConfig` | `session_recording_config` | `api/types/sessionrecording.go` | n | Singleton (`off`/`node`/`proxy`/`*-sync`). |
| `KindRecordingEncryption` | `recording_encryption` | `api/gen/proto/.../recordingencryption/v1` | n | Singleton; RFD 127 (age-encrypted recordings). |
| `KindRotatedKey` | `rotated_key` | same package | n | |
| `KindExternalAuditStorage` | `external_audit_storage` | `api/types/externalauditstorage` | n | Cloud-only: customer-owned S3+Athena. |
| `KindClusterName` | `cluster_name` | `api/types/clustername.go` | n | Singleton. |
| `KindClusterMaintenanceConfig` | `cluster_maintenance_config` | `api/types/maintenance.go` | n | Singleton (`cluster-maintenance-config`). |
| `KindUIConfig` | `ui_config` | `api/types/ui_config.go` | n | Singleton. |
| `KindNamespace` | `namespace` | `api/types/namespace.go` | special | Vestigial — only `default` is supported. |
| `KindInstaller` | `installer` | `api/types/installer.go` | n | Bash scripts served by /scripts/. Embed templates in `api/types/installers/`. |
| `KindNetworkRestrictions` | `network_restrictions` | `api/types/restrictions.go` | n | Singleton. |
| `KindServerInfo` | `server_info` | `api/types/server_info.go` | n | Per-node info applied at join. Subkind `cloud_info`. |
| `KindClusterAlert` | `cluster_alert` | `api/types/cluster_alert.go` | n | Ephemeral notifications. |
| `KindHeadlessAuthentication` | `headless_authentication` | `api/types/headlessauthn.go` | per-user | `headless_authentication/users/<user>/<id>`. |
| `KindIntegration` | `integration` | `api/types/integration.go` | n | AWS OIDC, GitHub, etc. |
| `KindPlugin` | `plugin` | `api/types/plugin.go` | n | |
| `KindPluginStaticCredentials` | `plugin_static_credentials` | `api/types/plugin_static_credentials.go` | n | |
| `KindUserGroup` | `user_group` | `api/types/usergroup.go` | n | Imported from Okta etc. |
| `KindOktaImportRule` / `KindOktaAssignment` | `okta_import_rule` / `okta_assignment` | `api/types/okta.go` | n | |
| `KindDiscoveryConfig` | `discovery_config` | `api/types/discoveryconfig/discoveryconfig.go` | n | Discovery service. |
| `KindAuditQuery` / `KindSecurityReport[State]` | `audit_query` / … | `api/types/secreports/secreports.go` | n | Access Monitoring queries. |
| `KindNotification` / `KindGlobalNotification` / `KindUserLastSeenNotification` / `KindUserNotificationState` / `KindUniqueNotificationIdentifier` | various | `api/gen/proto/.../notifications/v1` | n | |
| `KindAccessGraph[*]` | `access_graph*` | `api/types/accessgraph` | n | |
| `KindAccessGraphSettings` | `access_graph_settings` | `api/types/clusterconfig/access_graph_settings.go` | n | Singleton (`access-graph-settings`). |
| `KindKubeWaitingContainer` | `kube_ephemeral_container` | `api/types/kubewaitingcontainer` | n | |
| `KindBot` / `KindBotInstance` | `bot` / `bot_instance` | `api/gen/proto/.../machineid/v1` | n | tbot identity & per-instance state. |
| `KindSPIFFEFederation` | `spiffe_federation` | `api/types/spiffe_federations.go` (and `lib/services/local/spiffe_federations.go`) | n | |
| `KindWorkloadIdentity[X509Revocation/X509IssuerOverride[CSR]]` | `workload_identity*` | `api/gen/proto/.../workloadidentity/v1` | n | |
| `KindSigstorePolicy` | `sigstore_policy` | same package | n | |
| `KindStaticHostUser` | `static_host_user` | `api/gen/proto/.../userprovisioning/v2` | n | Host user reconciliation. |
| `KindUserLoginState` | `user_login_state` | `api/types/userloginstate/user_login_state.go` | n | Effective user after AccessList expansion. |
| `KindUserTask` | `user_task` | `api/types/usertasks/object.go` | n | |
| `KindCrownJewel` | `crown_jewel` | `api/gen/proto/.../crownjewel/v1` | n | Access Graph "important resources" tagging. |
| `KindSAMLIdPServiceProvider` | `saml_idp_service_provider` | `api/types/saml_idp_service_provider.go` | n | Teleport's built-in IdP. |
| `KindDevice` | `device` | `api/types/device.go` and `device.pb.go` | n | Device Trust. |
| `KindIdentityCenter*` | `aws_identity_center` / `aws_ic_account` / `aws_ic_permission_set` / `aws_ic_principal_assignment` / `aws_ic_account_assignment` | `api/gen/proto/.../identitycenter/v1` | n | |
| `KindAutoUpdate*` | `autoupdate_config` / `autoupdate_version` / `autoupdate_agent_rollout` / `autoupdate_agent_report` / `autoupdate_bot_instance_report` | `api/gen/proto/.../autoupdate/v1` | n | Singletons + per-agent state. |
| `KindHealthCheckConfig` | `health_check_config` | `api/gen/proto/.../healthcheckconfig/v1` | n | |
| `KindStableUNIXUser` | `stable_unix_user` | `api/types/userloginstate` etc. | n | RBAC-only. |
| `KindSemaphore` | `semaphore` | `api/types/semaphore.go` | n | Concurrency limits (max sessions etc.). |
| `KindMFADevice` | `mfa_device` | `api/types/mfa.go` and `mfa_device.pb.go` | per-user | Stored under `web/users/<u>/mfa/<id>`. |
| `KindRecoveryCodes` | `recovery_codes` | `api/types/recovery_codes.go` | per-user | |
| `KindLoginRule` | `login_rule` | `api/gen/proto/.../loginrule/v1` | n | |
| `KindPluginData` / `KindAccessPluginData` | `plugin_data` / `access_plugin_data` | `api/types/plugin_data.go` | n | Side-car data attached to other resources (e.g. access requests). |
| `KindVnetConfig` | `vnet_config` | `api/types/vnet` | n | Singleton (`vnet-config`). |
| `KindDelegationSession` | `delegation_session` | `api/types/delegation_session.go` | n | Lent identity to bots / AI agents. |
| `KindBeam` | `beam` | `api/gen/proto/.../beams/v1` | n | Ephemeral compute env. |
| `KindMCP` | `mcp` | RBAC alias for MCP-flavoured apps (see `app.go`). | n | Not a separate stored kind; the resource is still `KindApp` with `SubKindMCP`. |
| `KindBackendInfo` | `backend_info` | `api/types/backendinfo` | n | Singleton: `backend-info`. Records the active backend type. |
| `KindAppAuthConfig` | `app_auth_config` | `appauthconfig.go` | n | |
| `KindRetrievalModel` / `KindInference*` | `retrieval_model` / `inference_*` | `api/gen/proto/.../summarizer/v1` | n | Session-summarisation feature. |
| `KindWorkloadCluster` | `workload_cluster` | `lib/services/local/workloadcluster.go` | n | |
| `KindValidatedMFAChallenge` | `validated_mfa_challenge` | `api/types/validated_mfa_challenge_filter.go` | n | Short-lived. |

Notes that recur:
- Most "singleton" resources have a fixed `Name`; sometimes a constant
  like `MetaNameAccessGraphSettings = "access-graph-settings"`.
- "Heartbeat" resources (`node`, `app_server`, `db_server`, `kube_server`,
  `db_service`, `windows_desktop_service`, `git_server`,
  `database_object`) are the high-volume kinds that get a dedicated cache
  fanout.
- Resources defined in `api/gen/proto/...` (proto-first, RFD-153 style)
  are wrapped by `types.Resource153` shims and exposed through the legacy
  `types.Resource` interface via `types.Resource153ToLegacy`.

---

## 7. Audit events (`lib/events`)

The package distinguishes **two storage planes**:

a) **Discrete audit events** — small protobufs (`apievents.AuditEvent`)
   emitted via `Emitter.EmitAuditEvent(ctx, evt)`. These go into an
   *event store* and are searchable via `SearchEvents` /
   `SearchSessionEvents` (`api.go:1322` `type AuditLogger`).

b) **Session recordings** — multi-part binary streams (PTY bytes plus
   structured events) uploaded asynchronously to a *session store*
   implementing `MultipartUploader` (`api.go:1192`). The auth server
   reads back recordings via `StreamSessionEvents`.

The two planes are configured independently in
`cluster_audit_config`. They can use entirely different storage; e.g.
DynamoDB for events plus S3 for recordings is common on AWS.

### Event stores (discrete `EmitAuditEvent`)

| Store | Driver pkg | Behaviour |
|---|---|---|
| Filesystem | `lib/events/filelog.go` (`FileLog`) | One JSON-per-line audit log file per shard; the simplest store. `EmitAuditEvent` at line 132. |
| DynamoDB | `lib/events/dynamoevents/dynamoevents.go` | Native upsert plus a `event_index` for ordering. `EmitAuditEvent` at line 568. `SearchEvents` paginates via `LastEvaluatedKey`. |
| Athena (S3 + SNS + SQS + Glue) | `lib/events/athena/` | Producer side: SNS→SQS→S3 (Parquet) batcher (`publisher.go`, `consumer.go`). Query side: Athena SQL (`querier.go`). Cloud / large-scale customers. `EmitAuditEvent` at `athena.go:521`. Large events (>256KB) get stashed in `LargeEventsS3` first. Rate-limited reads. |
| Firestore | `lib/events/firestoreevents/firestoreevents.go` | One doc per event in a collection. Background purge task. `EmitAuditEvent` at line 320. |
| PostgreSQL | `lib/events/pgevents/pgevents.go` | Single `events` table with JSONB payload, indexed by time and event type. `EmitAuditEvent` at line 387. |
| In-memory (tests) | `lib/events/membuffer.go`, `lib/events/eventstest/` | |
| Multi-emitter / discard | `lib/events/multilog.go`, `lib/events/discard.go` | Compose or drop. |

`AuditLogSessionStreamer` (`api.go:1259`) is the umbrella interface
combining `AuditLogger`, `SessionStreamer`, and
`EncryptedRecordingUploader` — auth always speaks through this.

### Session recording stores (`MultipartUploader`)

| Store | Driver pkg | Notes |
|---|---|---|
| Local filesystem | `lib/events/filesessions/fileuploader.go` plus `filestream.go`, `fileasync.go` | Default for self-hosted; uploads are written to `<datadir>/sessions/<sid>/`. `fileasync.go` is the async uploader that ships completed sessions to a remote store. `filesessionrecorder.go` is the writer-side. |
| Amazon S3 | `lib/events/s3sessions/s3handler.go` | Native S3 multipart upload. Supports `sse_kms_key` URL parameter for SSE-KMS encryption (see RFD 42). |
| Google Cloud Storage | `lib/events/gcssessions/gcshandler.go` plus `gcsstream.go` | GCS multipart equivalent (compose objects). |
| Azure Blob | `lib/events/azsessions/azsessions.go` | Uses block blobs and Put-Block-List as the multipart primitive. |
| In-memory | `lib/events/memsessions/` | Tests. |

### Session-event spine

- `lib/events/api.go` — the canonical interfaces (`Emitter`, `Streamer`,
  `MultipartUploader`, `AuditLogger`, `SessionRecorder`).
- `lib/events/emitter.go` — async batching emitter with retry.
- `lib/events/stream.go` — the actual session-event stream protocol;
  builds gzip-compressed parts with optional encryption wrapping
  (`stream.go:109` `EncryptionWrapper`, `stream.go:819` handling of
  `recordingencryption.ErrEncryptionDisabled`).
- `lib/events/recorder/recorder.go` — high-level recorder used by SSH,
  kube, db. Picks file-based or sync streamer based on
  `RecordingCfg`. The Recorder ships bytes to `events.Streamer` which
  calls `EmitAuditEvent` and (for recordings) flushes through a
  `MultipartUploader`.
- `lib/events/auditlog.go` — composite implementation that fronts the
  per-driver event store; handles fan-out (cluster events + per-session
  index).
- `lib/events/playback.go` — replay path for session recordings (used
  by `tsh play` and the web UI playback).
- `lib/events/uploader.go` — interface that ties session-id to a backing
  `UploadHandler` (the per-cloud blob store).
- `lib/events/sessionend.go`, `lib/events/complete.go` — handle the
  "session ended without an end event" case (e.g. a crashed node):
  reads the uploaded recording back and synthesises a `session.end`
  event.

### Encryption

Two separate encryption stories:

1. **At-rest object encryption (S3 server-side)** — RFD 42, *S3 KMS
   Encryption*
   (`/home/daniel/repos/teleport/rfd/0042-s3-kms-encryption.md`,
   marked `implemented`). Adds an `sse_kms_key=<id>` query parameter to
   the S3 storage URL; the S3 handler forwards it as
   `ServerSideEncryption=aws:kms` on every PUT.

2. **Client-side recording encryption (age + KMS)** — RFD 127,
   *Encrypted Session Recordings*
   (`/home/daniel/repos/teleport/rfd/0127-encrypted-session-recordings.md`,
   `in development`). Encrypts the recording bytes *before* they leave
   the node, so neither the auth server nor the storage operator can
   decrypt without the keystore. Plumbed via
   `EncryptionWrapper`/`Encrypter` in `lib/events/stream.go` and the
   `recording_encryption` / `rotated_key` cluster resources (see
   `KindRecordingEncryption`, `KindRotatedKey`).
   `lib/auth/recordingencryption` is the keystore-aware service.

### Event codes / schema

- `lib/events/api.go` defines the *field name* constants (`EventType`,
  `EventCode`, `LoginMethod*`, etc.).
- `lib/events/codes.go` enumerates the per-event-type Teleport codes
  (e.g. `T1000I` for a user login).
- `lib/events/fields.go` is the legacy JSON-fields helper.

---

## 8. Inventory & heartbeats (`lib/inventory`)

The "Inventory Control Stream" (ICS) is a long-lived bidirectional gRPC
stream between every Teleport service and the auth server. It replaces
the legacy "open a connection for each heartbeat" model.

Layout:
- `lib/inventory/inventory.go` — *downstream* (agent) side.
  - `DownstreamHandle` (`inventory.go:60`) is the persistent handle. It
    auto-reconnects on failure; consumers acquire a fresh
    `DownstreamSender` from a channel after each disruption.
  - `instanceStateTracker` (`inventory.go:520`) tracks `LastHeartbeat`,
    the local hello message, the assigned auth-server ID, and computes
    `nextHeartbeat` (`inventory.go:638`).
- `lib/inventory/controller.go` — *upstream* (auth-server) side.
  - Controller multiplexes streams from many agents and feeds them into
    the `Store` (`store.go`).
  - Periodic instance heartbeats with `instanceHBInterval` (default
    `apidefaults.MinInstanceHeartbeatInterval`) and TTL
    `apidefaults.InstanceHeartbeatTTL`.
- `lib/inventory/store.go` — auth-side map of agent IDs → handles.
- `lib/inventory/servicecounter.go` — per-role counters (how many
  proxies/nodes/etc. are currently connected).
- `lib/inventory/metadata/` — gathers OS / cloud / version metadata for
  the `UpstreamInventoryHello`.
- `lib/inventory/internal/delay/` — jittered backoff helper.

Heartbeat v2 (`lib/srv/heartbeatv2.go`, despite the path it logically
belongs to inventory) is the *resource* heartbeat layer that runs on top
of ICS:

- `HeartbeatV2Config[T any]` is generic — there is one concrete
  constructor per resource: `NewSSHServerHeartbeat`,
  `NewAppServerHeartbeat`, `NewDatabaseServerHeartbeat`,
  `NewKubernetesServerHeartbeat`, `NewRelayServerHeartbeat`, etc.
- The heartbeat goroutine calls `GetResource` (the agent's own latest
  `types.ServerV2`), compares to the previously announced version, and
  on change/expiry sends `UpstreamInventoryHeartbeat` over ICS.
- The legacy fallback (still present for back-compat with old auth
  servers) is `Announcer.UpsertNode`/etc., kept for "DELETE IN 11.0".
- `AnnounceInterval`, `DisruptionAnnounceInterval`, `PollInterval` and
  `OnHeartbeat` are the knobs.

Watcher counterparts:
- `lib/services/local/inventory.go` (`instancePrefix = "instances"`) is
  the auth-side persistent record (`types.Instance`) of every agent —
  enables `tctl inventory ls`.
- `lib/cache/inventory/` (a sub-package) houses the cache-side
  collections.

---

## 9. Operational support packages

Short pointers — these are mostly orthogonal to the storage stack but
called out for completeness.

- `lib/usagereporter/usagereporter.go` — generic batching reporter (`UsageReporter[T]`,
  line 96). `Run(ctx)` is a goroutine; events submitted via
  `Submit()`/`SubmitAnonymized()` get batched and shipped at intervals,
  with Prometheus counters `teleport_usage_events_submitted`,
  `teleport_usage_batches`, `teleport_usage_events_requeued`. Concrete
  implementations live in `lib/usagereporter/teleport/` and
  `lib/usagereporter/web/`; `lib/usagereporter/teleport/aggregating/`
  pre-aggregates before shipping.
- `lib/release/release.go` — talks to the Teleport release HTTP API
  (versions, channels).
- `lib/versioncontrol/` — version comparison/normalisation helpers
  (`Normalize`, `Visitor`). `endpoint/` resolves the upgrade endpoint
  URL; `upgradewindow/` exports maintenance window helpers;
  `github/` reads releases from GitHub.
- `lib/autoupdate/agent/` — agent-side updater (downloads and switches
  binaries).
- `lib/autoupdate/rollout/controller.go` — auth-side rollout reconciler
  that watches `autoupdate_agent_rollout`/`autoupdate_agent_report`
  resources and decides which version each agent should run.
- `lib/autoupdate/lookup/` — auth answers
  `/v1/webapi/find` style version queries.
- `lib/autoupdate/tools/` — `tsh`/`tctl` self-update support
  (`@anthropic-managed-tools`-style replace-on-restart).
- `lib/autoupdate/report/` — agent report submission.
- `lib/automaticupgrades/` (older mechanism for k8s installs that
  predates `autoupdate`):
  - `channel.go` — channel resolution (`stable/cloud`, `stable/v15`, …).
  - `version/` — version fetcher (HTTP).
  - `maintenance/` — basic-HTTP based maintenance schedule.
  - `cache/` — in-memory caching layer over the HTTP version source.
- `lib/healthcheck/manager.go` — runs configured `health_check_config`
  policies against registered targets (apps, databases, kube clusters).
  `worker.go` runs a single target; `target.go` describes the contract.
- `lib/limiter/limiter.go` — connection + rate limiter. `Limiter` wraps
  `ConnectionsLimiter` and `RateLimiter`; integrates with gRPC and
  net/http via `listener.go` and `ratelimiter.go`.

---

## 10. AI-pointer index (concept → file)

Storage primitives
- KV backend interface — `/home/daniel/repos/teleport/lib/backend/backend.go:48`
- Driver registry — `/home/daniel/repos/teleport/lib/backend/registry.go:34`
- Key type, slash math — `/home/daniel/repos/teleport/lib/backend/key.go:27`
- TTL/expiry helpers — `/home/daniel/repos/teleport/lib/backend/backend.go:335`
- Revisions — `/home/daniel/repos/teleport/lib/backend/backend.go:356`
  (`CreateRevision`, `BlankRevision`)
- Atomic batch writes — `/home/daniel/repos/teleport/lib/backend/atomicwrite.go:174`
  (`ConditionalAction`, `MaxAtomicWriteSize = 64`)
- Backend metrics wrapper — `/home/daniel/repos/teleport/lib/backend/report.go:89`
- Backend in-memory fan-out buffer — `/home/daniel/repos/teleport/lib/backend/buffer.go:88`
- Backend lock primitive — `/home/daniel/repos/teleport/lib/backend/lock.go:35`
  (uses `.locks/...` keys)
- Backend conformance test suite — `/home/daniel/repos/teleport/lib/backend/test/suite.go`
- Auth-side data migrations — `/home/daniel/repos/teleport/lib/auth/migration/migration.go:60`

Backend drivers
- etcd — `/home/daniel/repos/teleport/lib/backend/etcdbk/etcd.go:58`
- dynamo — `/home/daniel/repos/teleport/lib/backend/dynamo/dynamodbbk.go:58`
- firestore — `/home/daniel/repos/teleport/lib/backend/firestore/firestorebk.go:57`
- postgres — `/home/daniel/repos/teleport/lib/backend/pgbk/pgbk.go:42`
- sqlite/lite — `/home/daniel/repos/teleport/lib/backend/lite/lite.go:49`
- kubernetes secret — `/home/daniel/repos/teleport/lib/backend/kubernetes/kubernetes.go:215`
- memory — `/home/daniel/repos/teleport/lib/backend/memory/memory.go:40`

Services / resource CRUD
- AccessService (roles, locks) — `/home/daniel/repos/teleport/lib/services/local/access.go`
- PresenceService (servers, app/db/kube heartbeats) — `/home/daniel/repos/teleport/lib/services/local/presence.go`
- IdentityService (users, web sessions, mfa) — `/home/daniel/repos/teleport/lib/services/local/users.go`
- TrustService (CAs, trusted clusters) — `/home/daniel/repos/teleport/lib/services/local/trust.go`
- DynamicAccess (access requests) — `/home/daniel/repos/teleport/lib/services/local/dynamic_access.go`
- EventsService (KV → typed event translation) — `/home/daniel/repos/teleport/lib/services/local/events.go:53`
- Generic CRUD wrapper — `/home/daniel/repos/teleport/lib/services/local/generic/generic.go:119`
- Higher-level Subscribe helpers (LockWatcher, CAWatcher, etc.) — `/home/daniel/repos/teleport/lib/services/watcher.go`
- Fanout (V2) — `/home/daniel/repos/teleport/lib/services/fanoutv2.go:68`
- Preset roles — `/home/daniel/repos/teleport/lib/services/presets.go:109`

Cache
- Cache struct — `/home/daniel/repos/teleport/lib/cache/cache.go:529`
- ForAuth (canonical watch set) — `/home/daniel/repos/teleport/lib/cache/cache.go:137`
- Read guard / "ok" state machine — `/home/daniel/repos/teleport/lib/cache/cache.go:617`
- Fetch+watch loop — `/home/daniel/repos/teleport/lib/cache/cache.go:1205`
- High-volume kinds — `/home/daniel/repos/teleport/lib/cache/cache.go:105`
- Generic cache collection — `/home/daniel/repos/teleport/lib/cache/collection.go:29`
- Cache collections registry — `/home/daniel/repos/teleport/lib/cache/collections.go:74`
- Example per-kind collection (`auth_server`) — `/home/daniel/repos/teleport/lib/cache/auth_server.go:32`
- Cache lifecycle events — `/home/daniel/repos/teleport/lib/cache/cache.go:911`

Resources / api/types
- All kind constants — `/home/daniel/repos/teleport/api/types/constants.go`
- Resource header — `/home/daniel/repos/teleport/api/types/header/header.go`
- Role — `/home/daniel/repos/teleport/api/types/role.go:67`
- User — `/home/daniel/repos/teleport/api/types/user.go:62`
- Server (node, auth, proxy, git_server, relay) — `/home/daniel/repos/teleport/api/types/server.go:36`
- App / MCP detection — `/home/daniel/repos/teleport/api/types/app.go:314`
- AppServer — `/home/daniel/repos/teleport/api/types/appserver.go:36`
- Database — `/home/daniel/repos/teleport/api/types/database.go:41`
- KubeCluster — `/home/daniel/repos/teleport/api/types/kubernetes.go:36`
- Desktops — `/home/daniel/repos/teleport/api/types/desktop.go`
- Lock — `/home/daniel/repos/teleport/api/types/lock.go:30`
- SessionTracker — `/home/daniel/repos/teleport/api/types/session_tracker.go:56`
- WebSession / WebToken — `/home/daniel/repos/teleport/api/types/session.go:55, :523`
- TrustedCluster — `/home/daniel/repos/teleport/api/types/trustedcluster.go:31`
- OIDC / SAML / Github — `/home/daniel/repos/teleport/api/types/oidc.go:38`, `saml.go:34`, `github.go:37`
- AccessList — `/home/daniel/repos/teleport/api/types/accesslist/accesslist.go`
- DiscoveryConfig — `/home/daniel/repos/teleport/api/types/discoveryconfig/discoveryconfig.go`
- UserLoginState — `/home/daniel/repos/teleport/api/types/userloginstate/user_login_state.go`
- UserTask — `/home/daniel/repos/teleport/api/types/usertasks/object.go`
- SecurityReports — `/home/daniel/repos/teleport/api/types/secreports/secreports.go`
- AccessGraphSettings — `/home/daniel/repos/teleport/api/types/clusterconfig/access_graph_settings.go`
- Wrappers (Labels/etc.) — `/home/daniel/repos/teleport/api/types/wrappers/wrappers.go`
- Installer templates — `/home/daniel/repos/teleport/api/types/installers/installers.go`

Audit & sessions
- Top-level AuditLogger interface — `/home/daniel/repos/teleport/lib/events/api.go:1322`
- AuditLogSessionStreamer — `/home/daniel/repos/teleport/lib/events/api.go:1259`
- MultipartUploader interface — `/home/daniel/repos/teleport/lib/events/api.go:1192`
- Streamer interface (session event streams) — `/home/daniel/repos/teleport/lib/events/api.go:1133`
- SSH session start emit — `/home/daniel/repos/teleport/lib/srv/sess.go:1054`
  (`emitSessionStartEvent`)
- SSH session reject — `/home/daniel/repos/teleport/lib/srv/regular/sshserver.go:1536`
- Recorder constructor — `/home/daniel/repos/teleport/lib/events/recorder/recorder.go`
- File-based recording uploader (default) — `/home/daniel/repos/teleport/lib/events/filesessions/fileuploader.go`
- Async upload daemon — `/home/daniel/repos/teleport/lib/events/filesessions/fileasync.go`
- Stream encryption — `/home/daniel/repos/teleport/lib/events/stream.go:109`
- Athena emit / search — `/home/daniel/repos/teleport/lib/events/athena/athena.go:521, :526`
- Dynamo emit / search — `/home/daniel/repos/teleport/lib/events/dynamoevents/dynamoevents.go:568, :778`
- Firestore emit / search — `/home/daniel/repos/teleport/lib/events/firestoreevents/firestoreevents.go:320, :362`
- Postgres emit / search — `/home/daniel/repos/teleport/lib/events/pgevents/pgevents.go:387, :595`
- S3 session handler — `/home/daniel/repos/teleport/lib/events/s3sessions/s3handler.go`
- GCS session handler — `/home/daniel/repos/teleport/lib/events/gcssessions/gcshandler.go`
- Azure session handler — `/home/daniel/repos/teleport/lib/events/azsessions/azsessions.go`
- RFD 42 S3+KMS — `/home/daniel/repos/teleport/rfd/0042-s3-kms-encryption.md`
- RFD 127 encrypted recordings — `/home/daniel/repos/teleport/rfd/0127-encrypted-session-recordings.md`

Inventory / heartbeat
- Downstream handle (agent) — `/home/daniel/repos/teleport/lib/inventory/inventory.go:60`
- Upstream controller (auth) — `/home/daniel/repos/teleport/lib/inventory/controller.go`
- Persistent instance store — `/home/daniel/repos/teleport/lib/services/local/inventory.go`
  (`instancePrefix = "instances"`)
- Heartbeat v2 (resource heartbeats over ICS) — `/home/daniel/repos/teleport/lib/srv/heartbeatv2.go:47`
- Inventory metadata gathering — `/home/daniel/repos/teleport/lib/inventory/metadata/`

Operational / lifecycle
- Generic usage reporter — `/home/daniel/repos/teleport/lib/usagereporter/usagereporter.go:96`
- Teleport usage events — `/home/daniel/repos/teleport/lib/usagereporter/teleport/`
- Release info client — `/home/daniel/repos/teleport/lib/release/release.go`
- Version compare/normalize — `/home/daniel/repos/teleport/lib/versioncontrol/versioncontrol.go`
- Maintenance window — `/home/daniel/repos/teleport/lib/versioncontrol/upgradewindow/`
- Auto-update rollout controller — `/home/daniel/repos/teleport/lib/autoupdate/rollout/controller.go`
- Auto-update agent — `/home/daniel/repos/teleport/lib/autoupdate/agent/`
- tools (tsh/tctl) self-update — `/home/daniel/repos/teleport/lib/autoupdate/tools/`
- Legacy auto-upgrades (k8s/helm) — `/home/daniel/repos/teleport/lib/automaticupgrades/channel.go`
- Healthcheck manager — `/home/daniel/repos/teleport/lib/healthcheck/manager.go`
- Limiter — `/home/daniel/repos/teleport/lib/limiter/limiter.go`
