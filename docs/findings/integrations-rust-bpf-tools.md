# Teleport: Integrations, Rust RDP, eBPF, and Tool Binaries

Reference for the four secondary-but-important subsystems: the `integrations/`
client family, the Rust RDP workspace, the eBPF enhanced-recording subsystem,
and the `tool/` binary entrypoints.

---

## 1. Integrations — overview

The `integrations/` tree is **not** part of a Teleport cluster. Every binary
under it is a *client* that talks to a Teleport cluster (either over the public
auth/proxy network endpoints or the in-cluster Auth gRPC) and reacts to or
manipulates cluster state. None of these binaries run as `auth`, `proxy`,
`node`, etc.; instead they hold a Teleport identity certificate and act as a
user/bot.

Authentication for the long-lived integrations (operator, Terraform MWI,
access plugins) is done via Machine ID (`tbot`):

* `integrations/lib/embeddedtbot/` runs an in-process `tbot` instance. See
  `bot.go::New` (`/home/daniel/repos/teleport/integrations/lib/embeddedtbot/bot.go:53`)
  which builds a `bot.Config` with `InternalStorage: destination.NewMemory()`
  and a `clientcredentials.ServiceBuilder` so the certificate is held
  in-process. Workflow: `Preflight(ctx)` does a `OneShot` join + `Ping` and
  returns server features; `start(ctx)` runs the bot loop renewing certs;
  `StartAndWaitForClient(ctx, timeout)` returns the `api/client.Client`.
* Short-lived plugins (Terraform "classic", event-handler) can alternatively
  use a static identity file produced by `tctl auth sign`.
* `integrations/lib/credentials/credentials.go::CheckIfExpired` is the shared
  helper that all plugins use to validate certs before connecting.

Shared library code lives in `integrations/lib/`:

| File / subdir | Purpose |
|---------------|---------|
| `lib/process.go`, `lib/runner.go`, `lib/signals.go` | Process/job scaffolding used by all plugins; `lib.Process` is the common supervisor that plugins embed. |
| `lib/config.go` | `TeleportConfig` (auth-addr, identity, cert/key/ca paths) and `NewClient(ctx)`. Used by every plugin's TOML config. |
| `lib/embeddedtbot/` | In-process tbot, as above. Used by operator and other long-lived integrations. |
| `lib/plugindata/` | Helpers for the `plugin_data` resource attached to access requests, access lists, and CAS (compare-and-swap) mutations: `access_request.go`, `access_list.go`, `cas.go`. |
| `lib/watcherjob/watcherjob.go` | Generic event-stream consumer (uses `types.Events.NewWatcher` under the hood) with backoff and a worker pool (`DefaultMaxConcurrency = 128`). Used by every access plugin to drive its event loop. |
| `lib/tar/extract.go` | Tarball extraction helper. |
| `lib/tctl/tctl.go` | Wrapper that shells out to a `tctl` binary (e.g. for `tctl auth sign`). |
| `lib/logger/logger.go` | slog initialization. |
| `lib/testing/integration/` | Integration test harness that brings up a real Teleport cluster. |
| `lib/email.go`, `lib/http.go`, `lib/addr.go`, `lib/bail.go`, `lib/errors.go`, `lib/escape.go` | Misc helpers. |

---

## 2. Operator (Kubernetes operator)

Source: `/home/daniel/repos/teleport/integrations/operator/`.

The operator is a `controller-runtime` Manager that watches Kubernetes Custom
Resources representing Teleport objects (`TeleportRole`, `TeleportUser`,
`TeleportBot`, ...) and reconciles them against a remote Teleport cluster. As
of v15 it can run separately from the cluster (see `README.md`).

### Architecture

The README's Mermaid diagram describes the per-event reconciliation:
event arrives → if delete: delete in Teleport, remove finalizer; if
create/update: add finalizer, check existence, check origin label, upsert,
report status on CR. Failures retry with backoff.

### Startup sequence (`main.go`)

`/home/daniel/repos/teleport/integrations/operator/main.go`:

1. Initialize slog (JSON output, `logutils.Initialize`).
2. Bind flags from both `operatorConfig` (`config.go`) and an
   `embeddedtbot.BotConfig{Kind: bot.KindKubernetesOperator}`.
3. `bot.Preflight(ctx)` — `tbot.OneShot` join + `Ping` to confirm
   credentials and obtain `ServerFeatures` (used to gate enterprise-only
   reconcilers).
4. `bot.StartAndWaitForClient(ctx, 15*time.Second)` — start the bot loop and
   block until we have an authenticated `api/client.Client`.
5. Build a `ctrl.NewManager(ctx, ctrl.Options{ LeaderElection: true,
   LeaderElectionID: config.leaderElectionID,
   LeaderElectionNamespace: config.namespace, … })`. Default lock ID is
   `431e83f4.teleport.dev` (`config.go:52`). Cache is scoped to a single
   namespace, `unstructured: true` because all controllers use unstructured
   objects.
6. `mgr.Add(bot)` — bot runs as a manager-managed runnable so leader-loss
   stops it.
7. `resources.SetupAllControllers(setupLog, mgr, client, pong.ServerFeatures,
   config.scoped)` registers reconcilers.
8. `mgr.Start(ctx)` blocks.

### Reconcilers

`controllers/resources/setup.go::enabledReconcilers` returns a list keyed off
`ServerFeatures` and the `--scoped` flag:

* **Scoped (always)**: `ScopedTokenV1`, `ScopedRoleV1`, `ScopedRoleAssignmentV1`.
* **Unscoped (default)**: `Role`, `RoleV6`, `RoleV7`, `RoleV8`, `User`,
  `GithubConnector`, `LockV2`, `ProvisionToken`, `OpenSSHServerV2`,
  `OpenSSHEICEServerV2`, `TrustedClusterV2`, `BotV1`,
  `WorkloadIdentityV1`, `AutoupdateConfigV1`, `AutoupdateVersionV1`,
  `AppV3`, `DatabaseV3`, `AccessMonitoringRuleV1`,
  `SAMLIdPServiceProviderV1`.
* **Entitlement-gated**: `OIDCConnector` (OIDC), `SAMLConnector` (SAML),
  `InferenceModel`/`InferencePolicy`/`InferenceSecret`/`RetrievalModelV1`
  (Policy), `LoginRule` (OIDC or SAML), `AccessList`/`OktaImportRule`
  (Advanced Access Workflows).

Each `*_controller.go` file calls one of two generic reconciler factories:

* `controllers/reconcilers/generic.go::resourceReconciler[T,K]` — the core
  generic reconciler. Adds the `resources.teleport.dev/deletion` finalizer,
  honours `teleport.dev/ignore` and `teleport.dev/keep` annotations, checks
  the `teleport.dev/origin` label, and dispatches CRUD via a per-resource
  `resourceClient[T]` interface.
* `controllers/reconcilers/resource153.go::NewTeleportResource153Reconciler` —
  wrapper for resources implementing the newer `types.Resource153` interface
  (proto-defined). All v1 resources use this.

API types live in `apis/resources/{v1,v2,v3,v5}/` (e.g.
`apis/resources/v1/accesslist_types.go`). Versions correspond to Teleport
resource versions; v3 is the original (githubconnector_types.go,
oidcconnector_types.go); v1 holds all post-2024 protobuf-defined resources.
The shared scheme is built in `controllers/scheme.go::Scheme` (initialised
from `apis/resources::AddToScheme` plus `clientgoscheme` and `apiextv1` for
CRD types).

### CRD generator (`crdgen/`)

`integrations/operator/crdgen/` is a `protoc` plugin that generates the
Kubernetes CRD YAML manifests from the `api/types` protobuf descriptors.

* Two `cmd/` binaries: `cmd/protoc-gen-crd` (emits CRD YAMLs),
  `cmd/protoc-gen-crd-docs` (emits docs).
* `handlerequest.go::HandleCRDRequest` is the protoc entrypoint. It walks the
  proto descriptor, finds known message types listed in the hard-coded
  `resources` slice in `generateSchema`, and emits a CRD per resource. Each
  entry can carry `resourceSchemaOption`s: `withVersionOverride`,
  `withNameOverride`, `withAdditionalColumns`, `withCustomSpecFields`,
  `withSingletonName`, `withScope`, `legacyWithoutVersionInKindOverride`.
* `schemagen.go` is the JSON-schema synthesizer; `format.go` produces the
  final `CustomResourceDefinition` YAML; `tree.go` walks the proto tree;
  `additional_doc.go` injects extra description text; `ignored.go` lists
  proto fields to skip.

`PROJECT` is the kubebuilder PROJECT file. It only lists `TeleportRole` and
`TeleportUser` (the originals) because everything newer is added via the CRD
generator and `apis/resources/` packages, not via kubebuilder scaffolding.

### Other notes

* `namespace.go` reads the operator's own namespace from `POD_NAMESPACE` or
  the K8s service-account namespace file, mandatory.
* The cache is `DefaultNamespaces: {config.namespace: {}}` — operator only
  watches its own namespace.

---

## 3. Terraform providers

Two providers live side-by-side, **for different reasons**.

### `integrations/terraform/` — main "classic" provider

* `main.go` → `providerserver.Serve(... Address: "terraform.releases.teleport.dev/gravitational/teleport")`.
* `provider/provider.go` implements `provider.Provider` and registers ~46
  resources and corresponding data sources (one of each per Teleport object).
  See the file listing — every `resource_teleport_*.go` and matching
  `data_source_teleport_*.go` is **code-generated** from the proto schemas
  using:
* `gen/main.go` — the codegen driver, which invokes a custom `protoc` plugin
  using the per-resource YAML configs in this directory:
  `protoc-gen-terraform-{accesslist,accessmonitoringrules,appauthconfig,autoupdate,dbobjectimportrule,devicetrust,discoveryconfig,example,healthcheckconfig,loginrule,scopedroleassignment,scopedrole,scopedtoken,statichostuser,summarizer,teleport,vnetconfig,workloadcluster,workloadidentity}.yaml`.
  Templates: `gen/{plural,singular}_{resource,data_source}.go.tpl`.
* `protoc-gen-terraform-teleport.yaml` is the big legacy bag of resources
  (`AppV3`, `AuthPreferenceV2`, `ClusterMaintenanceConfigV1`, `Database`,
  `Github/OIDC/SAMLConnector`, `KubernetesClusterV3`, `LockV2`, `OktaImportRuleV1`,
  `Role`, `Server`, `User`, `ProvisionTokenV2`, `IntegrationV1`, etc.).
  Each per-resource YAML adds `id` injected fields,
  excluded/computed/required/sensitive field lists, `plan_modifiers` (e.g.
  `Metadata.Name → RequiresReplace()`), validators (`UseVersionBetween`,
  `UseMapKeysPresentValidator("teleport.dev/origin")`, etc.).
* Generated Terraform schemas land in `tfschema/`.
* `provider/credentials.go` accepts identity-file or cert/key/CA combos and
  builds an `api/client.Client`.
* Docs reference: `DOCS.md`, `templates/`, `reference.mdx`. `make docs`
  re-renders the markdown reference (custom fork of `tfplugindocs`).

### `integrations/terraform-mwi/` — Machine & Workload Identity provider

Separate because:

1. It joins via `tbot`-style ephemeral join methods (proxy address + join
   method + join token) — *no static identity file* — so the configuration
   surface is fundamentally different.
2. It only exposes ephemeral resources / data sources, not persistent
   Teleport CRUD. Its purpose is to feed credentials to *other* Terraform
   providers (e.g. provision a kubeconfig that talks through Teleport).
3. As of writing, the README explicitly says "work-in-progress and is not yet
   ready for production use."

* `main.go` registers under `terraform.releases.teleport.dev/gravitational/teleportmwi`.
* `provider/provider.go::Provider` schema accepts `proxy_server`,
  `join_method` (validated against `onboarding.SupportedJoinMethods` and
  *not* allowed to be `JoinMethodToken`), `join_token`, `insecure`,
  `skip_initial_connection`.
* Two main capabilities so far:
  * `provider/kubernetes_data_source.go` — data source that emits Kube
    cluster info.
  * `provider/kubernetes_ephemeral_resource.go` — ephemeral resource that
    issues a short-lived kubeconfig per Terraform apply (Terraform 1.10+
    ephemeral-resource support).
* `gen/docs.sh` and the `templates/` directory drive docs generation.
* Examples live in `examples/data-sources/` and `examples/ephemeral-resources/`.

### `integrations/terraform-modules/`

A small helper that renders Terraform modules from templates. The
`teleport/` subdirectory contains the per-resource Terraform module fragments
referenced by `tctl terraform`.

---

## 4. Event-handler

Source: `/home/daniel/repos/teleport/integrations/event-handler/`.

Forwards Teleport audit events to **Fluentd via mTLS**. There is *no other
supported sink* in the OSS pipeline — the entire I/O layer is the
`FluentdClient` in `fluentd_client.go`. To get events into Splunk/etc. users
typically configure a Fluentd output plugin downstream.

### Pipeline

`main.go::start` → `app.go::App.Run(ctx)`:

* `app.init(ctx)` builds the Teleport API client, creates the on-disk
  `State` (see below), and instantiates `FluentdClient`.
* Two long-running jobs spawned via `lib.Process.SpawnCriticalJob`:
  * `events_job.go::EventsJob` — main audit log consumer. Uses
    `auditlogpb` bulk export when available, falls back to the legacy
    watcher (`legacy_events_watcher.go`).
  * `session_events_job.go::SessionEventsJob` — separately drains session
    recording events. Posts each session's events to
    `FluentdSessionURL.<session-id>.log`.
* `App.SendEvent` retries via `retryutils.NewRetryV2` with an exponential
  backoff (base 1s, max 10s, 5 attempts). The retry/backoff layer is the
  only thing keeping a flaky Fluentd from losing events.

### Persistent state — `state.go`

* On-disk KV store using `github.com/peterbourgon/diskv/v3` rooted at the
  configured `storage` directory. Stores `start_time`, `window_time`,
  cursor, last event id, session prefix entries, and missing-recording
  markers.
* Also wraps `lib/events/export.Cursor` (v2 export cursor) when the
  Auth Service supports the newer bulk-export API.
* If the `--storage` directory is lost, restart with an explicit
  `--start-time` to resume.

### Auto-locking ("response" feature)

`events_job.go` uses `github.com/sethvargo/go-limiter` (`memorystore.New`)
with `Tokens: LockFailedAttemptsCount`, `Interval: LockPeriod` to detect a
configurable burst of failed-login events per user and call
`tctl lock`-equivalent gRPC to lock the offender for `LockFor`. This is the
"if we see N failed logins in T window, lock the user automatically"
mechanism.

### CLI / config — `cli.go`

`kong` parser, three subcommands:
* `version` — print version.
* `configure <out> <addr> <ca> <server> <client>` — generate mTLS cert/key
  bundle for Fluentd (`configure_cmd.go`, `mtls_certs.go`).
* `start` — start ingestion.

All config keys also available via `FDFWD_*` env vars and TOML file
(`kong_toml_resolver.go`).

---

## 5. Kube-agent-updater

Source: `/home/daniel/repos/teleport/integrations/kube-agent-updater/`.

Updates the Teleport image tag of a Kubernetes `Deployment` or `StatefulSet`
holding Teleport agents. Per the README, designed first for cloud customers
but adaptable on-prem.

### Discovery flow (`cmd/teleport-kube-agent-updater/main.go`)

* Mandatory flags `--agent-name` and `--agent-namespace`; the manager cache
  is scoped to that single namespace + name.
* `getUpdateID(ctx, mgr, …)` reads or creates a ConfigMap named
  `<agent-name>-updater` that holds a stable per-installation UUID used by
  the canary mechanism.
* Configures two update channels (any combination):
  1. **RFD-184 "proxy-driven" updates** when `--proxy-address` is set: uses
     `webclient.NewReusableClient` → `version.NewProxyVersionGetter` and
     `maintenance.NewProxyMaintenanceTrigger` (both reads piggyback on a
     shared client to hit the proxy's `/find` endpoint).
  2. **RFD-109 HTTP version-channel updates** when `--version-server` is set:
     defaults to `https://updates.releases.teleport.dev/v1/` + channel
     `stable/cloud`. Uses `version.NewBasicHTTPVersionGetter` and
     `maintenance.NewBasicHTTPMaintenanceTrigger`; planned maintenance
     windows come from the agent pods themselves via
     `podmaintenance.NewWindowTrigger`.
* `version.FailoverGetter` tries each version source in order.
* Image validation: `img.NewCosignSingleKeyValidator` against the embedded
  prod and (for pre-release builds) staging cosign public keys.
  Override flags: `--insecure-no-verify-image`,
  `--insecure-no-resolve-image`.

### Reconciler (`pkg/controller/`)

* `updater.go::VersionUpdater` is the engine: ask each
  `version.Getter` for the desired version, run each `maintenance.Trigger`
  to decide if it's time to roll, validate the resulting image reference
  with the configured `img.Validators`, then mutate the workload.
* `deployment.go::DeploymentVersionUpdater` and
  `statefulset.go::StatefulSetVersionUpdater` are the two `Reconciler`s
  registered with the manager — they only watch the single
  Deployment/StatefulSet whose name matches `--agent-name`.
* `status_writer.go` writes update status (current version, last update
  attempt, update group) to the per-installation ConfigMap so the proxy can
  read it.
* Leader election uses the agent name as the lock ID
  (`LeaderElectionID: agentName`).

### Maintenance triggers (`pkg/maintenance/`)

* `unhealthy.go` — triggers an immediate update if pods are CrashLoopBackOff.
* `window.go` — honours per-agent maintenance windows.

---

## 6. Access plugins

Source: `/home/daniel/repos/teleport/integrations/access/`.

All access plugins follow the same pattern. The pattern lives in
`integrations/access/common/`.

### Shared scaffold — `access/common/`

* `app.go::BaseApp` — every plugin embeds (or returns) a `*BaseApp`.
  Responsibilities:
  * Open the Teleport API client via `PluginConfiguration.GetTeleportClient(ctx)`.
  * `checkTeleportVersion(ctx)` (minimum `6.1.0-beta.1`).
  * `Conf.NewBot(clusterName, webProxyAddr) → MessagingBot`.
  * Call `Bot.SupportedApps()` to get the list of `App` workers and run them
    on the embedded `lib.Process`.
* `bot.go::MessagingBot` — interface every plugin's bot satisfies:
  `CheckHealth`, `FetchRecipient`, `SupportedApps`.
* `config.go::PluginConfiguration` + `BaseConfig` — TOML config shared by all
  plugins (Teleport client config, `RawRecipientsMap`, log config, plugin
  type, TeleportUser identity).
* `recipient.go` — the `Recipient` type and recipient-resolution helpers
  (channel ID vs. email vs. group).
* `annotations.go` — read `teleport.dev/notify-services` etc. annotations
  from access requests.
* `status.go` — set the messaging-side status indicator (e.g. updating a
  Slack message colour when an access request is approved).
* `response.go` — common response types.
* `auth/token_provider.go` + `auth/storage/` — shared token-store
  abstraction for plugins that use OAuth (Slack, Discord, MS Teams).
* `teleport/client.go` — wrapper interface that unions
  `*api/client.Client` with the access-list, access-monitoring-rules, and
  user-login-state subclients; constructed via `config.go::wrapAPIClient`.

### Cross-cutting "App" implementations under `access/`

Three implementations of the `common.App` interface that get composed into
each plugin's `Bot.SupportedApps()`:

* `access/accessrequest/app.go::App` — listens for access-request events,
  posts/edits notifications via the bot, runs reviews. Uses
  `plugindata.AccessRequestData` for CAS-style state.
* `access/accesslist/app.go::App` — periodic reminders for access list
  reviews. `bot.go` defines the notification interface specific to access
  lists.
* `access/accessmonitoring/access_monitoring_rules.go` — listens for
  Access Monitoring Rules events, evaluates them against access requests,
  and notifies recipients matching the rule.

### Per-plugin entry-point pattern

Each `access/<plugin>/` directory typically contains:

| File | Role |
|------|------|
| `app.go` | One-liner `NewXxxApp(conf) → *common.BaseApp` that wraps `common.NewApp(conf, pluginName)`. See `slack/app.go` and `discord/app.go`. |
| `config.go` | TOML config struct embedding `common.BaseConfig`, plus `NewBot()` constructor. |
| `bot.go` | Implements `common.MessagingBot`. |
| `client.go` (some) | HTTP client for the third-party API. |
| `types.go` | Local DTOs. |
| `cmd/teleport-<plugin>/main.go` | `kingpin` parser with `start`/`configure`/`version` subcommands; calls `common.NewApp(conf, pluginName)`. |
| `Makefile` | Per-plugin build target (each is built into its own binary). |
| `testlib/` | Integration test suite. |

Per-plugin notes:

| Plugin | Channel / posts to | Notes |
|--------|--------------------|-------|
| `slack/` | Slack workspace channels and users. Has OAuth flow (`oauth.go`, `oauth_test.go`) and HTTP API client (`http.go`). | Standalone binary `teleport-slack`. |
| `jira/` | Jira issues. Has a webhook server (`webhook_server.go`) for Jira → Teleport callbacks. `INSTALL-JIRA-CLOUD.md` + `INSTALL-JIRA-SERVER.md`. | Standalone binary `teleport-jira`. |
| `pagerduty/` | PagerDuty incidents. | Standalone binary `teleport-pagerduty`. |
| `mattermost/` | Mattermost channels. | Standalone binary `teleport-mattermost`. |
| `discord/` | Discord channels via webhooks. | Standalone binary `teleport-discord`. |
| `email/` | SMTP email. `mailers.go` builds the SMTP client; `client.go` sends. | Standalone binary `teleport-email`. |
| `datadog/` | Datadog Incident Management. | Standalone binary `teleport-datadog`. |
| `msteams/` | MS Teams via the Microsoft Graph / bot framework (`msapi/`). `card.go` builds adaptive cards. Has its own configure/uninstall lifecycle (`configure.go`, `uninstall.go`). | Standalone binary `teleport-msteams`. |
| `opsgenie/` | Opsgenie alerts. | **Library only** — no `cmd/`. Used by the Cloud-hosted plugin runtime. |
| `servicenow/` | ServiceNow incidents. | **Library only** — no `cmd/`. Same as Opsgenie. |
| `accesslist/`, `accessrequest/`, `accessmonitoring/` | Not standalone plugins — cross-cutting `App` implementations that the standalone plugins compose. | See above. |

---

## 7. Rust RDP — workspace layout

The root `Cargo.toml` defines a 3-member workspace:

```
[workspace]
members = [
    "lib/srv/desktop/rdp/rdpclient",   # CGO staticlib used by `teleport`
    "lib/srv/desktop/rdp/decoder",     # CGO staticlib used to render session recordings
    "web/packages/shared/libs/ironrdp", # WASM cdylib used by the browser
]
```

All three pin the same IronRDP git revision in `[workspace.dependencies]`
(currently `a0a3e750c9e4ee9c73b957fbcb26dbc59e57d07d`).

### `lib/srv/desktop/rdp/rdpclient/` — Windows-side RDP client

`Cargo.toml` declares `crate-type = ["staticlib"]` and exposes a `fips`
feature that pulls in `tokio-boring/fips` and `boring/fips`. Wires in:

* `ironrdp-{cliprdr, connector, core, pdu, rdpdr, rdpsnd, session, svc, dvc,
  displaycontrol, tls, tokio}` — full IronRDP protocol stack from
  Devolutions.
* `boring` / `tokio-boring` (optional, FIPS only) — BoringSSL for TLS.
* `rustls` (default features off, `aws-lc-rs` only) — TLS for non-FIPS.
* `sspi` 0.16 with `network_client` — for NLA/CredSSP.
* `picky`, `picky-asn1-{der,x509}` — X.509 / smartcard cert handling.
* `iso7816`, `iso7816-tlv` — smartcard primitives used by the PIV emulation.
* `rdp-decoder` (path dep) — re-exported so the staticlib carries both APIs.
* `cbindgen` (build-dep) drives `build.rs` to emit `librdpclient.h`.

`src/lib.rs` is the FFI surface. It declares the modules:

```
pub mod client;          // src/client.rs + src/client/global.rs
mod cliprdr;             // Clipboard channel
mod license;             // RDP licensing
mod network_client;      // KDC / Kerberos client
mod piv;                 // Fake PIV smartcard
mod rdpdr;               // Device redirection (rdpdr.rs + rdpdr/{filesystem,flags,path,scard,tdp}.rs)
mod ssl;
mod util;
```

And exports the following `#[no_mangle] pub unsafe extern "C"` functions
(callable from Go):

* `rdpclient_init_log`, `free_string`.
* `client_run(cgo_handle, CGOConnectParams) -> CGOResult` — opens an RDP
  session; blocks until the session ends.
* `client_stop(cgo_handle)` — terminates a running session.
* `client_update_clipboard(cgo_handle, data, len)`.
* `client_handle_tdp_sd_announce / _remove / _info_response /
  _create_response / _delete_response / _list_response / _read_response /
  _write_response / _move_response / _truncate_response` — TDP shared
  directory FUSE-like operations.
* `client_handle_tdp_rdp_response_pdu` — generic PDU passthrough.
* `client_write_rdp_{pointer,keyboard,sync_keys}` — keyboard/mouse input
  from the browser.

Functions called *from* Rust *into* Go are declared as `extern "C"` in
`src/client.rs` and matched by `//export` comments in the Go file:
`cgo_handle_fastpath_pdu`, `cgo_handle_rdp_connection_activated`,
`cgo_handle_remote_copy`.

The CGO bridge stores per-session state in `src/client/global.rs` (a global
map of `CgoHandle → ClientHandle`). The Go side never holds a Rust pointer
— only opaque `cgo.Handle` integers go across the boundary in either
direction. See `README.md` for the rationale.

### `lib/srv/desktop/rdp/decoder/` — session recording renderer

`Cargo.toml` declares `crate-type = ["staticlib", "lib"]` so it is usable
both via CGO and from other Rust crates (the rdpclient crate `pub extern
crate rdp_decoder as _;` to re-export its unmangled C symbols).

Dependencies: `ironrdp-{core,graphics,pdu,session}` — only the parts needed
to render a `DecodedImage` from fast-path PDUs.

`src/lib.rs::RdpDecoder` holds a `DecodedImage`, a `fast_path::Processor`, a
`CursorState`, and an `UpdatedRegions` tracker. Its `process(&mut self,
tdp_fast_path_frame: &[u8])` is called by Go to consume a recorded fast-path
frame; `image_data()` returns the current RGBA32 frame. Used during
`tsh recordings export` and `tsh play` to render desktop recordings to PNGs
/ MP4s.

### `web/packages/shared/libs/ironrdp/` — WASM crate

`Cargo.toml` declares `crate-type = ["cdylib"]` so it can be turned into a
`.wasm` for the browser via `wasm-bindgen`. Uses `getrandom` versions 1 + 2
both pinned to the `js`/`wasm_js` backends; `tracing-web` for console
logging; `wasm-bindgen` for the JS bindings.

`src/lib.rs` exports to JavaScript:

* `init_wasm_log(log_level)` — install panic hook and tracing subscriber.
* `BitmapFrame` struct (top, left, ImageData).
* `FastPathProcessor` struct: `new(width, height, io_channel_id,
  user_channel_id)`, `resize`, `process(tdp_fast_path_frame, cb_context,
  draw_cb, respond_cb, update_pointer_cb)` — same fast-path IronRDP
  processor as the Go side, but talks back to the browser via JS callbacks
  instead of into a Go channel. Also includes a one-shot `check_remote_fx`
  that emits a friendly error if the Windows host hasn't enabled RemoteFX.

Mock for unit tests: `mock_ironrdp.js`.

### Go side of the FFI

* `lib/srv/desktop/rdp/rdpclient/client_common.go` — public Go API,
  `Config`, `LicenseStore`, `Client` shell. **Always compiled.** Heavily
  documents the protocol flow.
* `lib/srv/desktop/rdp/rdpclient/client.go` — `//go:build
  desktop_access_rdp`. Contains the `import "C"` block with the linker flags
  for each `GOOS/GOARCH`:
  * `linux/{386,amd64,arm,arm64}`: link
    `-l:librdp_client.a -lpthread -ldl -lm` from
    `target/<rust-triple>/release/`.
  * `darwin/{amd64,arm64}`: `-framework CoreFoundation -framework Security
    -lrdp_client -lpthread -ldl -lm`.
  Imports `#include <librdpclient.h>` (the cbindgen-emitted header).
* `client_fips.go` — `//go:build desktop_access_rdp && fips`. Calls
  `C.rdpclient_assert_fips_enabled()` at init so a FIPS Teleport binary
  refuses to run against a non-FIPS rdp staticlib.
* `client_nop.go` — `//go:build !desktop_access_rdp`. Stub implementation
  so `lib/srv/desktop` still builds without the Rust dep.
* The decoder mirror: `decoder.go`, `decoder_rdpclient.go` (build-tagged
  `desktop_access_rdp`, blank-imports rdpclient), `decoder_standalone.go`
  (standalone build-tag), `decoder_nop.go`, `decoder_common.go` (shared
  types). This conditional graph lets the decoder be linked either alongside
  the full RDP client (so the symbols come from `librdp_client.a`) or by
  itself.

---

## 8. Rust RDP — build flow

### Static library (Linux / macOS native binaries)

`Makefile` (root):

```
.PHONY: rdpclient
rdpclient: rustup-toolchain-warning
ifeq ("$(with_rdpclient)", "yes")
ifneq ($(RDPCLIENT_SKIP_CARGO),1)
    $(RDPCLIENT_ENV) \
        cargo build -p rdp-client $(if $(FIPS),--features=fips) --release --locked $(CARGO_TARGET)
endif
endif

.PHONY: rdpdecoder
rdpdecoder: rustup-toolchain-warning
ifeq ("$(with_rdpclient)", "yes")
    $(RDPCLIENT_ENV) \
        cargo build -p rdp-decoder --release --locked $(CARGO_TARGET)
endif
```

`RDPCLIENT_ENV` includes `CGO_ENABLED=0` and `BORING_BSSL_FIPS_SYSROOT` for
FIPS builds. The resulting `librdp_client.a` is left at
`target/<rust-triple>/release/librdp_client.a` and picked up by the CGO
`LDFLAGS` in `client.go`.

`teleport` itself is built with build tag `desktop_access_rdp` (see
`Makefile:408`) which switches `client_nop.go` off and pulls in the real
implementation.

### WASM (browser)

`Makefile`:

```
.PHONY: build-ironrdp-wasm
build-ironrdp-wasm: ironrdp = web/packages/shared/libs/ironrdp
build-ironrdp-wasm: ensure-wasm-deps
    RUSTFLAGS='--cfg getrandom_backend="wasm_js"' \
        cargo build --package ironrdp --lib --target $(CARGO_WASM_TARGET) --release
    wasm-opt target/$(CARGO_WASM_TARGET)/release/ironrdp.wasm \
        -o target/$(CARGO_WASM_TARGET)/release/ironrdp.wasm -O
    wasm-bindgen target/$(CARGO_WASM_TARGET)/release/ironrdp.wasm \
        --out-dir $(ironrdp)/pkg --typescript --target web
    printenv ironrdp_package_json > $(ironrdp)/pkg/package.json
```

`CARGO_WASM_TARGET = wasm32-unknown-unknown`. The pipeline is
`cargo build` → `wasm-opt -O` → `wasm-bindgen --target web` →
synthesize `package.json`. Final artifacts (`ironrdp_bg.wasm`,
`ironrdp.js`, `ironrdp.d.ts`) end up in `web/packages/shared/libs/ironrdp/pkg/`
and are imported by the React web UI as the npm package `ironrdp`. Tooling
versions enforced by `ensure-wasm-bindgen` and `ensure-wasm-opt` targets.

`IRONRDP_SKIP_BUILD=1` short-circuits the WASM build (useful in CI when
the artifacts are already cached).

### Docker build environments

* `build.assets/Dockerfile` — main glibc-based buildbox.
* `build.assets/Dockerfile-centos7` — old-glibc buildbox built on
  `centos:7 + devtoolset-12` used to produce binaries compatible with very
  old Linux distros. Pulled by `make -C build.assets buildbox-centos7`
  (Makefile target around line 241).
* `build.assets/Dockerfile-arm` — cross-compile buildbox for arm/arm64
  Linux targets.
* `build.assets/Dockerfile-bpf` — Debian-12-based buildbox dedicated to
  compiling the BPF C files (Go + clang + llvm + libbpf-dev). Built with
  `make -C build.assets bpf-bytecode`.

The native Rust toolchain is invoked from inside these images so the
resulting `librdp_client.a` has the same glibc ABI as the rest of the
binary.

---

## 9. eBPF subsystem

Source: `bpf/` (kernel C side), `lib/bpf/` (userspace Go side), `lib/cgroup/`
(cgroup scoping). Used only for **enhanced session recording** on Linux
servers (`kernel >= 5.8`) and only when `BPF` tag is set in the build.

### Programs (`bpf/enhancedrecording/`)

Three CO-RE BPF programs, each compiled per-arch via `bpf2go`:

| Source | Hooks (high-level) | Event type emitted | Ring buffer |
|--------|--------------------|--------------------|-------------|
| `command.bpf.c` | `tracepoint/syscalls/sys_enter_execve`, `sys_exit_execve`, `sys_enter_execveat`, `sys_exit_execveat`, plus `fexit/bprm_execve` for reliable argv. Uses `BPF_MAP_TYPE_TASK_STORAGE` (`inflight_exec`) to stash filename/argv on enter and finalise on exit. | `data_t {pid, ppid, command[16], filename[512], args[20*1024], args_len, args_truncated, cgroup, audit_session_id, return_code}` | `execve_events` 4096*2048 bytes |
| `network.bpf.c` | kprobe on `tcp_v4_connect` / `tcp_v6_connect`. Uses an `INFLIGHT_MAX=8192` hashmap (`currsock`) to correlate enter/exit. | Separate `ipv4_data_t` / `ipv6_data_t` (cgroup, audit_session_id, ip, pid, saddr, daddr, dport, command) | `ipv4_events` / `ipv6_events` 4096*8 bytes each |
| `disk.bpf.c` | `fentry/security_file_open` (for resolved path via `bpf_d_path`) + `fexit/do_filp_open` (for return code). Uses `BPF_MAP_TYPE_TASK_STORAGE` (`inflight_open`) to carry the path across. | `data_t {cgroup, audit_session_id, pid, return_code, command[16], file_path[PATH_MAX], flags}` | `open_events` 4096*2048 bytes |

Common scaffolding:

* `bpf/helpers.h` — `BPF_HASH`, `BPF_RING_BUF`, `BPF_COUNTER` macros.
* `bpf/enhancedrecording/common.h` — debug-only `bpf_printk` wrapper,
  `MAX_MONITORED_SESSIONS=1024`, `FILENAMESIZE=512`, `MAXARGLEN/MAXARGS`
  constants.
* All three programs share a `monitored_sessionids` hashmap keyed by
  `audit_session_id` (the kernel's `loginuid`) that the userspace populates
  per session.
* All three programs publish a `BPF_COUNTER(lost)` for ring-buffer
  overflow accounting (with a `LostDoorbell` map to wake the userspace
  collector).
* Per-arch `vmlinux.h` lives in `bpf/x86/vmlinux.h` and `bpf/arm64/vmlinux.h`
  (regenerated via `make update-vmlinux-h`).
* License is `Dual BSD/GPL` (note: `GPL` enables more BPF features but is
  reverted before merge — see `bpf/README.md`).

### Userspace loader (`lib/bpf/`)

* `generate.go::go:generate` — calls `bpf2go` (multi-arch, both `amd64` and
  `arm64`) to compile each `.bpf.c` into a `bpfel.o` blob and matching Go
  bindings. The committed artifacts are
  `{command,disk,network}_{amd64,arm64}_bpfel.{go,o}`.
* `bpf.go::Service` (`//go:build bpf && !386`) — the live service. On
  start it constructs a `controlgroup.Service` for cgroup scoping then
  calls `startExec`, `startOpen`, `startConn` to load the three programs
  via `cilium/ebpf`'s ringbuf reader + link attachers. Spawns one goroutine
  per ring buffer that drains events and dispatches to
  `emitCommandEvent`, `emitDiskEvent`, `emit4NetworkEvent`,
  `emit6NetworkEvent`. Lost-event counters are logged every 5s.
* `command.go::startExec`, `disk.go::startOpen`, `network.go::startConn` —
  per-program loaders. Each:
  1. Calls `rlimit.RemoveMemlock()` (for kernels <5.11).
  2. `loadXxxObjects(&objs, nil)` from the bpf2go-generated bindings.
  3. Attaches each `Program` in the object to its tracepoint / fentry /
     kprobe via `link.{Tracepoint,Tracing,Kprobe}`.
  4. Creates a `ringbuf.Reader` and spawns a goroutine running
     `sendEvents(eventType, bpfEvents, eventBuf)` which reads frames and
     pushes raw bytes into a `chan []byte` (with 10s send timeout to avoid
     hanging on a stalled emitter).
  5. Wraps the lost-counter map in `bpf.Counter` so it can be polled for
     Prometheus.
* `common.go` — `BPF` interface (`OpenSession`, `CloseSession`, `Close`,
  `Enabled`, `LostEvents`), `SessionContext` struct (carries
  AuditSessionID, Login, User, Roles, Traits, Emitter).
* `helper.go` — `Counter` wiring with the `LostDoorbell` map.
* `bpf_nop.go` (`//go:build !(bpf && !386)`) — stub for non-Linux/arch
  builds.

### Session lifecycle

`Service.OpenSession(ctx *SessionContext)`:
1. Sanity-check `AuditSessionID != MaxUint32` (Linux `-1` underflow means
   unset).
2. Register the audit session id with each module's `monitored_sessionids`
   map (only the modules whose event type is in
   `ctx.Events` — `command`, `disk`, `network`).
3. Store the `SessionContext` in `s.sessions` map keyed by audit session id.

`Service.CloseSession(ctx)` reverses it.

Events arriving on each ring buffer carry `audit_session_id`; the emit
function looks up the matching `SessionContext` and calls
`ctx.Emitter.EmitAuditEvent(...)` with a properly typed
`apievents.SessionCommand` / `SessionDisk` / `SessionNetwork` event.

### Cgroup scoping (`lib/cgroup/cgroup.go`)

`Service` (`Config{MountPath, RootPath}`) places each Teleport session into a
sub-cgroup under `<MountPath>/<RootPath>/<random-uuid>` and exposes
`cgroupv2` IDs to the BPF programs (BPF programs read the current task's
cgroup ID via `BPF_CORE_READ`). The package has a top-of-file `TODO:
REMOVE IN v20` note — by v19 the cgroup filesystem is no longer mounted by
Teleport, and `Close(skipUnmount)` is only retained to clean up after
upgrades. Each `BPFConfig` carries `*controlgroup.Config`; the BPF
`Service` owns one `controlgroup.Service` and unmounts (unless restarting)
on `Close`.

### Build environment

`Dockerfile-bpf` (Debian-12 + clang + llvm + libbpf-dev + Go); invoked via
`make -C build.assets bpf-bytecode` to produce reproducible BPF object
files. For local development, `make bpf-bytecode` runs `go generate` against
the local clang.

---

## 10. `tool/` binaries

### `tool/teleport/` — the main daemon

* `main.go` — calls `reexec.MaybeReexec()` first (re-exec idiom used for
  privilege-drop and SSH child commands), then
  `common.Run(common.Options{Args: os.Args[1:]})`.
* `tool/teleport/common/teleport.go::Run` builds a single `kingpin`
  application with these top-level commands (extracted from the file):
  * `start` — main daemon (huge flag set: roles, token, advertise-ip,
    listen-ip, auth-server, config, config-string, labels, fips, etc.).
  * `status` — print status of the current SSH session.
  * `configure` — write a sample config file.
  * `version` — print version.
  * `join openssh` — register an OpenSSH server with the cluster (no daemon).
  * `app start`, `db start`, `db configure {create,bootstrap,aws
    {print-iam,create-iam}}`, `discovery bootstrap`, `node configure`,
    `install {systemd,autodiscover-node}`, `integration configure
    {deployservice-iam,ec2-ssm-iam,aws-app-access-iam, …}`,
    `kube-state delete` — secondary management surfaces.
  * Hidden re-exec subcommands (used internally when the daemon re-execs
    itself): `scp`, `sftp`, `exec`, `networking`, `checkHomeDir`, `park`,
    `true`.
  * `wait {no-resolve,duration}` — startup-time helpers used by Helm
    charts to gate readiness.
* Loads its config from a YAML file (`--config`, default
  `/etc/teleport.yaml`), or `--config-string` (base64-encoded), or the
  flag-driven `CommandLineFlags` (`config.CommandLineFlags`). The whole
  thing ends up in a `servicecfg.Config` that gets passed to
  `lib/service.NewTeleport(config)`.
* Imports `_ "github.com/gravitational/teleport/lib/fipscheck"` (same as
  every other CLI) — the blank import installs an init-time check that
  fatally exits if `FIPS=1` is set in the env and BoringSSL is not.
* `common/integration_configure.go` is where the `tctl/teleport` IAM
  bootstrappers live, shared with `tctl`.

### `tool/tctl/` — admin CLI

* `main.go` — sets up a signal-handled context and calls
  `common.Run(ctx, common.Commands())`.
* `tool/tctl/common/cmds.go::Commands()` returns the full list of
  `CLICommand` implementations (each implements
  `Initialize(*kingpin.Application, *GlobalCLIFlags, *servicecfg.Config)`
  and `TryRun(ctx, cmd, clientFunc) (match, err)`):
  ```
  VersionCommand, UserCommand, NodeCommand, TokensCommand, AuthCommand,
  StatusCommand, top.Command, AccessRequestCommand, AppsCommand, DBCommand,
  KubeCommand, DesktopCommand, LockCommand, BotsCommand,
  WorkloadIdentityCommand, InventoryCommand, discovery.Command,
  RecordingsCommand, AlertCommand, ProxyCommand, ResourceCommand,
  EditCommand, ExternalAuditStorageCommand, LoadtestCommand,
  DevicesCommand, SAMLCommand, ACLCommand, loginrule.Command, IdPCommand,
  accessmonitoring.Command, accessgraph.AccessGraphCommand,
  plugin.PluginsCommand, NotificationCommand, sso/configure.SSOConfigureCommand,
  sso/tester.SSOTestCommand, fido2Command, webauthnwinCommand,
  touchIDCommand, TerraformCommand, AutoUpdateCommand,
  stableunixusers.Command, decision.Command, BoundKeypairCommand,
  ScopedCommand
  ```
* `tool/tctl/common/tctl.go::TryRun` is the dispatcher. Global flags:
  `--auth-server`, `--identity`, `--config`, `--mfa-mode`, `--insecure`.
  Auth target priority: identity file, then `--auth-server`, then local
  Auth socket (`--config`-derived).
* Distinctive subcommands:
  * `tctl get` / `tctl create` / `tctl rm` / `tctl edit` — generic
    resource CRUD (`resource_command.go`, `edit_command.go`). `tctl edit`
    fetches the resource, opens `$EDITOR`, then applies the diff.
  * `tctl auth sign` / `tctl auth rotate` (`auth_command.go`,
    `auth_rotate_command.go`) — CA management and identity-file issuance.
  * `tctl tokens add/ls/rm` (`token_command.go`).
  * `tctl bots {add,ls,update,rm}` (`bots_command.go`,
    `bound_keypair_command.go`).
  * `tctl plugins ...` (`plugin/`) — manage Cloud-hosted integration plugins.
  * `tctl scoped {role,role-assignment,token} ...` (`scoped_*.go`).
  * `tctl top` — TUI dashboard (`top/`).
  * `tctl terraform env` (`terraform_command.go`) — bootstrap a Terraform
    environment with the right credentials.
  * `tctl sso configure ...` / `tctl sso test ...` from `tool/tctl/sso/`.
* `tool/tctl/common/client/` — wraps `auth.AuthClient` creation; in-cluster
  auth (when run on the Auth host) vs. proxy gRPC are picked here.
* `tool/tctl/common/config/` — `GlobalCLIFlags` definition.
* `tool/tctl/common/mfa/` — MFA prompt for admin actions.

### `tool/tsh/` — user CLI

* `main.go` — one-liner: `tshcommon.Main()`.
* `tool/tsh/common/tsh.go` is ~4500 lines containing one giant
  `Main()` → `Run(args)` function. The kingpin `app` has 74+ top-level
  subcommands. Grouped:
  * **SSH-style**: `ssh`, `scp`, `ls`, `resolve`, `join`, `play`,
    `clusters`.
  * **Session management**: `sessions ls`, `recordings ls`, `recordings
    export`, `latency ssh`.
  * **Auth**: `login`, `logout`, `status`, `env`, `headless approve`,
    `mfa`, `delegation create-session`, `device`.
  * **Cloud / proxy**: `aws`, `aws-profile`, `az`, `gcloud`, `gsutil`,
    `proxy {ssh,db,app,mcp,aws,azure,gcloud}`.
  * **Apps**: `apps {ls,login,logout,config}`.
  * **Databases**: `db {ls,login,logout,env,config,connect,exec}`.
  * **Kubernetes**: `kube {credentials,ls,login,sessions,exec,join}`
    (declared in `kube.go::newKubeCommand`); also `kubectl` (which
    re-execs `kubectl` with Teleport-aware kubeconfig).
  * **Access requests**: `request {ls,show,create,review,search,drop}`.
  * **Git proxying**: `git {clone,config,list,login,ssh}`
    (`git_*.go`) — proxies Git through Teleport.
  * **Beams (file transfer)**: `beams {add,ls,publish,scp,ssh,rm,
    unpublish,exec}` (`beams_*.go`).
  * **MCP (Model Context Protocol)**: `mcp {app,db}` (`mcp*.go`).
  * **Workload Identity**: `workload_identity.go`.
  * **VNet** (virtual network): `vnet`, `vnet-ssh-auto-config`,
    `vnet-admin-setup`, `vnet-daemon`, `vnet-service`,
    `vnet-install-service`, `vnet-uninstall-service` (per-platform files
    `vnet_{darwin,linux,windows,other,daemon_*}.go`).
  * **Bench**: `bench {ssh, web {ssh,sessions}, kube {ls,exec},
    postgres, mysql}` (hidden, internal benchmarking).
  * **Daemon mode** (Teleport Connect backend): `daemon {start,stop}` —
    hidden, used by Teleport Connect.
  * **Config emit**: `config` (OpenSSH ProxyCommand format), `puttyconfig`
    (Windows-registry config).
  * **Scopes**: `scopes` / `scope` (`scopes.go`).
  * **Show**: `show` (read an identity file).
* Config is loaded from `~/.tsh/` profiles via `api/profile`, augmented by
  `TELEPORT_*` env vars. Cluster, proxy and current identity come from
  there.
* Hardware-key support (`piv.go`, `tool/common/fido2`, `touchid`,
  `webauthnwin`) is conditionally compiled per-platform.
* `vnet_client_application.go`, `vnet_daemon_*.go` implement the macOS/Linux
  daemon split for the per-app TUN VNet.

### `tool/tbot/` — Machine ID CLI

* `main.go` — kingpin app `tbot`. Top-level commands:
  * `version`.
  * `start` — primary mode: long-running renewal loop. Subcommands attach
    to it via `cli.New{SSHMultiplexer,Application,Database,Kubernetes,
    KubernetesV2,ApplicationTunnel,DatabaseTunnel,WorkloadIdentityX509,
    WorkloadIdentityAPI,WorkloadIdentityJWT,WorkloadIdentityAWSRA,
    ApplicationProxy,SSHMultiplexer}Command`. Configures an output
    "service" producing those credentials.
  * `configure` — mirror of `start` but emits YAML config instead of
    running.
  * `kube credentials` — `exec`-style kubectl credential plugin (writes
    short-lived creds to stdout).
  * `keypair create` — manage bound-keypair joining material
    (`keypair.go`).
  * `init` — initialize a new tbot config interactively (`init.go`).
  * `migrate` — migrate old configs.
  * `proxy`, `ssh-proxy`, `ssh-multiplexer-proxy` — Git/SSH proxy modes
    (`proxy.go`, `proxy_ssh.go`).
  * `db` — DB-helper commands (`db.go`).
  * `copy-binaries` — `copy_binaries.go`, used by the kube-agent-updater.
  * `wait` — `wait.go`, startup gating.
  * `spiffe-inspect` — debug SPIFFE workload API.
  * `tpm identify` — emit TPM diagnostics for TPM joining.
  * `install-systemd` — write a systemd unit file (`systemd.go`).
* Real implementation lives in `lib/tbot/`; the `tool/tbot/` files are
  CLI glue + per-platform pieces (`signals_{unix,windows}.go`,
  `init.go`, etc.).
* `anonymous_telemetry.go` — opt-out anonymous telemetry on first run.

### `tool/teleport-update/` — managed-updates client

* Single-file binary at `tool/teleport-update/main.go`. Subcommands
  (kingpin):
  * `enable` / `disable` — toggle managed updates.
  * `status [--is-up-to-date]` — print update status; returns exit code 3
    when not up to date.
  * `update` — run an immediate update.
  * `uninstall` — completely remove the install (requires
    `--force` if `--no-confirm`).
* Loads its config from `/etc/teleport/update.yaml` (see
  `lib/autoupdate/agent`), or env vars `TELEPORT_PROXY`,
  `TELEPORT_INSTALL_DIR`, `TELEPORT_PATH`, `TELEPORT_UPDATE_GROUP`,
  `TELEPORT_UPDATE_VERSION`, `TELEPORT_UPDATE_FLAGS`.
* Uses a 10-minute lock (`updateLockTimeout`) to serialise concurrent
  invocations.
* Restart logic is in `lib/autoupdate/agent` (separate from this tool).

### `tool/fdpass-teleport/` — FD-passing helper

* **Rust binary**, not Go. Independent Cargo project (own `Cargo.toml`,
  `Cargo.lock`), built via:
  ```
  cd tool/fdpass-teleport && cargo build --release --locked $(CARGO_TARGET)
  ```
* Single source file `src/main.rs`. Usage: `fdpass-teleport <mux-socket-path>
  <target>`. Opens the multiplexer socket (a Unix socket), opens stdin/stdout
  file descriptors, and uses `nix::sys::socket::sendmsg` with
  `ControlMessage::ScmRights` to pass those FDs across to the
  `tbot ssh-multiplexer` process, so the multiplexer can take over the I/O
  for that SSH connection without going through Go.
* Distributed alongside `teleport`, `tctl`, `tsh`, `tbot`, `teleport-update`
  in the default Linux build (`BINS_default = teleport tctl tsh tbot
  fdpass-teleport teleport-update`, Makefile:260) but **not on macOS**
  (`BINS_darwin` excludes it, Makefile:261).
* The macOS universal binary is built by lipoing the arm64 and amd64 builds
  (Makefile:780).

### `tool/common/` — shared CLI helpers

* `common.go` — `ExitCodeError` for typed exit codes, `SessionsCollection`
  writers (text/JSON/YAML), `WriteSessionsTable` used by `tsh sessions ls`
  and `tctl recordings ls`.
* `fido2/`, `touchid/`, `webauthnwin/` — per-platform CLI commands for
  managing webauthn devices, used by both `tsh` and `tctl`.

---

## 11. AI-pointer index

| Concept | File / directory |
|---------|------------------|
| Operator main | `/home/daniel/repos/teleport/integrations/operator/main.go` |
| Operator reconciler setup | `/home/daniel/repos/teleport/integrations/operator/controllers/resources/setup.go` |
| Generic CR reconciler | `/home/daniel/repos/teleport/integrations/operator/controllers/reconcilers/generic.go` |
| CRD generator protoc plugin | `/home/daniel/repos/teleport/integrations/operator/crdgen/handlerequest.go` |
| Embedded tbot for plugins | `/home/daniel/repos/teleport/integrations/lib/embeddedtbot/bot.go` |
| Terraform provider (classic) main | `/home/daniel/repos/teleport/integrations/terraform/main.go` |
| Terraform classic codegen config | `/home/daniel/repos/teleport/integrations/terraform/protoc-gen-terraform-teleport.yaml` |
| Terraform MWI provider | `/home/daniel/repos/teleport/integrations/terraform-mwi/provider/provider.go` |
| Event-handler main | `/home/daniel/repos/teleport/integrations/event-handler/main.go` |
| Event-handler app/runtime | `/home/daniel/repos/teleport/integrations/event-handler/app.go` |
| Event-handler persistent state (diskv) | `/home/daniel/repos/teleport/integrations/event-handler/state.go` |
| Event-handler rate limiter (go-limiter) | `/home/daniel/repos/teleport/integrations/event-handler/events_job.go` |
| Fluentd egress client | `/home/daniel/repos/teleport/integrations/event-handler/fluentd_client.go` |
| Kube-agent-updater main | `/home/daniel/repos/teleport/integrations/kube-agent-updater/cmd/teleport-kube-agent-updater/main.go` |
| Kube-agent-updater reconciler | `/home/daniel/repos/teleport/integrations/kube-agent-updater/pkg/controller/updater.go` |
| Cosign image validator | `/home/daniel/repos/teleport/integrations/kube-agent-updater/pkg/img/cosign.go` |
| Access plugin BaseApp | `/home/daniel/repos/teleport/integrations/access/common/app.go` |
| Access plugin MessagingBot interface | `/home/daniel/repos/teleport/integrations/access/common/bot.go` |
| Access plugin TOML config base | `/home/daniel/repos/teleport/integrations/access/common/config.go` |
| Slack plugin entry | `/home/daniel/repos/teleport/integrations/access/slack/cmd/teleport-slack/main.go` |
| Slack plugin app glue | `/home/daniel/repos/teleport/integrations/access/slack/app.go` |
| Access request cross-cutting App | `/home/daniel/repos/teleport/integrations/access/accessrequest/app.go` |
| Access list cross-cutting App | `/home/daniel/repos/teleport/integrations/access/accesslist/app.go` |
| Access monitoring rule worker | `/home/daniel/repos/teleport/integrations/access/accessmonitoring/access_monitoring_rules.go` |
| Generic event-watcher helper | `/home/daniel/repos/teleport/integrations/lib/watcherjob/watcherjob.go` |
| Plugin data helpers | `/home/daniel/repos/teleport/integrations/lib/plugindata/access_request.go` |
| Rust workspace root | `/home/daniel/repos/teleport/Cargo.toml` |
| RDP client FFI surface | `/home/daniel/repos/teleport/lib/srv/desktop/rdp/rdpclient/src/lib.rs` |
| RDP client IronRDP wiring | `/home/daniel/repos/teleport/lib/srv/desktop/rdp/rdpclient/src/client.rs` |
| RDP client cbindgen build | `/home/daniel/repos/teleport/lib/srv/desktop/rdp/rdpclient/build.rs` |
| RDP client smartcard PIV emulation | `/home/daniel/repos/teleport/lib/srv/desktop/rdp/rdpclient/src/piv.rs` |
| RDP client smartcard reader emulation | `/home/daniel/repos/teleport/lib/srv/desktop/rdp/rdpclient/src/rdpdr/scard.rs` |
| RDP decoder (Rust) | `/home/daniel/repos/teleport/lib/srv/desktop/rdp/decoder/src/lib.rs` |
| Go side of RDP FFI (real impl) | `/home/daniel/repos/teleport/lib/srv/desktop/rdp/rdpclient/client.go` |
| Go side of RDP FFI (FIPS check) | `/home/daniel/repos/teleport/lib/srv/desktop/rdp/rdpclient/client_fips.go` |
| Go side of RDP FFI (no-op stub) | `/home/daniel/repos/teleport/lib/srv/desktop/rdp/rdpclient/client_nop.go` |
| Browser WASM IronRDP crate | `/home/daniel/repos/teleport/web/packages/shared/libs/ironrdp/src/lib.rs` |
| Rust build targets | `/home/daniel/repos/teleport/Makefile` (lines 525-570) |
| BPF README | `/home/daniel/repos/teleport/bpf/README.md` |
| BPF programs (exec/disk/network) | `/home/daniel/repos/teleport/bpf/enhancedrecording/{command,disk,network}.bpf.c` |
| BPF common helpers | `/home/daniel/repos/teleport/bpf/enhancedrecording/common.h`, `/home/daniel/repos/teleport/bpf/helpers.h` |
| BPF Go service | `/home/daniel/repos/teleport/lib/bpf/bpf.go` |
| BPF Go session lifecycle | `/home/daniel/repos/teleport/lib/bpf/bpf.go::OpenSession`/`CloseSession` |
| BPF Go per-program loaders | `/home/daniel/repos/teleport/lib/bpf/{command,disk,network}.go` |
| BPF Go codegen invocation | `/home/daniel/repos/teleport/lib/bpf/generate.go` |
| Cgroup scoping | `/home/daniel/repos/teleport/lib/cgroup/cgroup.go` |
| BPF Dockerfile | `/home/daniel/repos/teleport/build.assets/Dockerfile-bpf` |
| Teleport daemon entry | `/home/daniel/repos/teleport/tool/teleport/main.go` |
| Teleport daemon CLI parser | `/home/daniel/repos/teleport/tool/teleport/common/teleport.go` |
| tctl entry | `/home/daniel/repos/teleport/tool/tctl/main.go` |
| tctl command list | `/home/daniel/repos/teleport/tool/tctl/common/cmds.go` |
| tctl dispatcher | `/home/daniel/repos/teleport/tool/tctl/common/tctl.go` |
| tctl edit | `/home/daniel/repos/teleport/tool/tctl/common/edit_command.go` |
| tctl get / create / rm | `/home/daniel/repos/teleport/tool/tctl/common/resource_command.go` |
| tsh entry | `/home/daniel/repos/teleport/tool/tsh/main.go` |
| tsh main parser/dispatcher | `/home/daniel/repos/teleport/tool/tsh/common/tsh.go` |
| tsh kube subcommands | `/home/daniel/repos/teleport/tool/tsh/common/kube.go` |
| tsh proxy subcommands | `/home/daniel/repos/teleport/tool/tsh/common/proxy.go` |
| tsh VNet | `/home/daniel/repos/teleport/tool/tsh/common/vnet.go` |
| tbot entry | `/home/daniel/repos/teleport/tool/tbot/main.go` |
| teleport-update entry | `/home/daniel/repos/teleport/tool/teleport-update/main.go` |
| fdpass-teleport (Rust) | `/home/daniel/repos/teleport/tool/fdpass-teleport/src/main.rs` |
| Common CLI session writer | `/home/daniel/repos/teleport/tool/common/common.go` |
