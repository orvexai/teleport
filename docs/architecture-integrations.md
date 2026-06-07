# Architecture — Integrations

> **Scope:** `integrations/operator/`, `integrations/terraform/`, `integrations/terraform-mwi/`, `integrations/terraform-modules/`, `integrations/event-handler/`, `integrations/kube-agent-updater/`, `integrations/access/*`, `integrations/lib/`.
>
> **Deep dive:** `findings/integrations-rust-bpf-tools.md` §1-6 (~500 lines on integrations alone, with ~30 file:line citations).

---

## 1. What This Part Is

Auxiliary Go programs that **authenticate to a Teleport cluster as bots** (via `tbot` / MachineID) and perform an out-of-band job — reconciling Kubernetes CRDs, projecting Terraform state, posting access-request approvals to Slack, forwarding audit events to a SIEM, rolling agent versions.

These are *clients* of a Teleport cluster. They are **not** part of the core daemon and do **not** ship with the `teleport` binary. Each has its own release lifecycle and (in some cases) its own Go module.

---

## 2. Common Patterns

1. **Authentication = MachineID.** Every integration runs an embedded `tbot` (`integrations/lib/embeddedtbot/`) or uses an identity file from a host-side `tbot`. Short-lived certs only — no static credentials.
2. **Communication = gRPC via `api/client`.** The public Go SDK (`github.com/gravitational/teleport/api`) is the *only* sanctioned channel.
3. **Watchers.** Integrations that react to cluster state use `api/client`'s watcher API to stream resource changes (especially `access_request` and audit event streams).
4. **Per-binary deployment.** Each integration is its own binary. Most have a corresponding Helm chart in `examples/chart/`.
5. **Code generation.** Operator CRDs and Terraform schemas are *generated from Teleport protos*. Edit the generator, not the rendered output.

---

## 3. Sub-Project Inventory

### 3a. Operator — `integrations/operator/`

Kubernetes operator that lets users manage Teleport resources via Kubernetes CRDs (`kind: TeleportRole`, `kind: TeleportUser`, etc.). Architecture: kubebuilder layout + `controller-runtime`.

```
integrations/operator/
├── main.go                       Bootstraps manager, leader election, embedded tbot
├── config.go                     Loads flags + reads identity file
├── namespace.go
├── apis/resources/
│   ├── teleportcr/               Shared CR plumbing
│   ├── v1/                       Modern proto-driven CR APIs
│   ├── v2/  v3/  v5/             Legacy hand-rolled CR API families
├── crdgen/                       Custom protoc plugin
│   └── cmd/
│       ├── protoc-gen-crd/       Emits CRDs from Teleport protos
│       └── protoc-gen-crd-docs/  Emits matching docs
├── controllers/
│   ├── reconcilers/              Generic reconciler core
│   └── resources/                Per-resource reconcilers
│       └── secretlookup/         Resolves secrets referenced in CRs
├── config/crd/bases/             Generated CRD YAML
└── Makefile  PROJECT  README.md
```

**Architecture pattern** (matches the Mermaid diagram in `integrations/operator/README.md`):

- Operator boots and starts an in-process `tbot`. Waits for certificate availability.
- Acquires leader lock (only the leader reconciles; followers are warm standbys).
- Each CR reconciler watches one Teleport resource kind. On Kubernetes event:
  - If `delete`: call Teleport `Delete<Resource>` (treats 404 as success), remove finalizer.
  - If `create`/`update`: idempotently `Upsert<Resource>` via the gRPC client.
- Reconcilers are feature-gated — a missing CR family (e.g. `v5/`) doesn't break startup.

**CR version families**: the modern `v1/` family is *proto-driven* (CRD types auto-generated from Teleport's protos). Older families `v2/v3/v5/` are hand-rolled and predate the generator.

**Module**: shares the root `go.mod`. **Helm chart**: `examples/chart/teleport-cluster/templates/operator/` (subchart) — operator is typically deployed alongside an Auth cluster.

### 3b. Terraform Provider — `integrations/terraform/`

Hashicorp `terraform-plugin-framework` provider for Teleport resources. **Separate Go module** (`integrations/terraform/go.mod`).

```
integrations/terraform/
├── main.go                       go-plugin entry
├── provider/                     Provider, resources, data sources
├── tfschema/<resource>/v1/       Generated Terraform schema per resource
│                                  ~40 resources (access_list, role, user, oidc_connector,
│                                  saml_connector, github_connector, app, database,
│                                  kube_cluster, lock, login_rule, provision_token,
│                                  trusted_cluster, scoped_role, workload_identity, …)
├── gen/strcase/                  Codegen helpers
├── examples/resources/<resource>/HCL examples per resource
├── templates/                    Doc templates
├── testlib/                      Test rig
├── DOCS.md                       Generated provider documentation
├── protoc-gen-terraform-*.yaml   ~12 YAML configs driving codegen for groups of resources
└── go.mod
```

The schema generator is the unusual part: **`protoc-gen-terraform-*.yaml`** files each describe how to project one Teleport proto into Terraform schema. The generator (`build.assets/tooling/cmd/...` or a vendored `protoc-gen-terraform`) reads each yaml and produces `tfschema/<resource>/v1/<resource>.go`.

Authentication: provider accepts an `identity_file` (preferred) or `addr`/`cert_path`/`key_path` set. The credentials map to the standard `api/client` constructors.

### 3c. Terraform MWI Provider — `integrations/terraform-mwi/`

A **second**, smaller Terraform provider for Machine & Workload Identity (`teleportmwi_kubernetes` ephemeral resource and data source). Separate module so it can ship on a different cadence than the main provider — Terraform plugin-SDK constraints are notoriously narrow.

```
integrations/terraform-mwi/
├── main.go
├── provider/                     Provider plumbing
├── examples/
│   ├── data-sources/teleportmwi_kubernetes/
│   └── ephemeral-resources/teleportmwi_kubernetes/
├── templates/  gen/  build/
├── go.mod  CONTRIBUTING.md  README.md
```

The "ephemeral" Terraform pattern (TF 1.10+) is well-suited to short-lived workload credentials — they're never written to state.

### 3d. Terraform Modules — `integrations/terraform-modules/`

Not a Terraform provider. It's a **renderer for `terraform-modules` deployment helpers** (`teleport/discovery/aws/`, `teleport/discovery/azure/`). Used to assemble + lint reusable HCL modules. Has its own `Makefile` and `gen/` output.

### 3e. Event-Handler — `integrations/event-handler/`

Forwards Teleport audit events to external SIEMs. **Separate Go module.**

```
integrations/event-handler/
├── main.go                       alecthomas/kong CLI
├── lib/                          Forwarding pipeline
├── build.assets/  example/  testdata/  tpl/
├── go.mod
```

**Sink:** only **Fluentd over mTLS**. (Splunk / Datadog / Elastic are reached *through Fluentd* with appropriate Fluentd plugins — there is **no direct sink** for them.) The event-handler maintains a disk-backed queue (`peterbourgon/diskv/v3`) and rate-limits with `sethvargo/go-limiter` so an outage at the sink doesn't lose events.

### 3f. Kube-Agent-Updater — `integrations/kube-agent-updater/`

Watches deployed `teleport-kube-agent` Helm releases in a Kubernetes cluster and rolls them forward when a new Teleport version is released.

```
integrations/kube-agent-updater/
├── cmd/teleport-kube-agent-updater/main.go
├── pkg/
│   ├── controller/               controller-runtime reconcilers
│   ├── img/                      cosign image signature verification
│   ├── maintenance/              Maintenance-window logic
│   └── podutils/                 Pod helpers
├── hack/  DEBUG.md  README.md  version.go
```

Update sources: **RFD-184 proxy-driven** (queries the Teleport proxy's update endpoint) and **RFD-109 HTTP-channel** (legacy fallback). Pull image references are validated with cosign before any rollout.

Shares the root `go.mod`. Released independently from Teleport itself.

### 3g. Access Plugins — `integrations/access/<plugin>/`

Per-vendor binaries that subscribe to `access_request` events from Teleport and turn them into vendor-specific approval flows (Slack messages, Jira tickets, PagerDuty incidents, …). All share a common scaffold:

```
integrations/access/
├── common/                       Shared scaffolding
│   ├── auth/                     Credential loading + storage
│   ├── teleport/                 Teleport-facing helpers
│   ├── recipient.go              Vendor-recipient resolution (user → channel/queue/ID)
│   ├── bot.go                    common.BaseApp — the event-loop & lifecycle base class
│   ├── annotations.go            Reads access_request annotations
│   ├── app.go  config.go  constants.go  response.go  status.go
├── accesslist/  accessmonitoring/  accessrequest/   Cross-plugin features
├── slack/   cmd/teleport-slack/      + slack/testlib/
├── jira/    cmd/teleport-jira/       + jira/testlib/
├── pagerduty/  cmd/teleport-pagerduty/  + pagerduty/testlib/
├── opsgenie/   testlib/              [library-only — no cmd/]
├── servicenow/ testlib/              [library-only]
├── mattermost/ cmd/teleport-mattermost/  …
├── msteams/   cmd/teleport-msteams/     + msapi/ + _tpl/
├── datadog/   cmd/teleport-datadog/
├── discord/   cmd/teleport-discord/
├── email/     cmd/teleport-email/
├── common.mk  Dockerfile
```

**Surprise**: `opsgenie` and `servicenow` are *library-only* — they don't have a `cmd/teleport-<plugin>/` binary. They run as **Teleport Cloud-hosted plugins** (Gravitational runs the binary on behalf of the customer); the OSS-vended binaries are the chat tools.

**Helm charts** for the runnable plugins live in `examples/chart/access/<plugin>/`.

### 3h. Integrations Shared Library — `integrations/lib/`

Cross-plugin utilities used by the access plugins and the operator:

| Sub-package | Purpose |
| --- | --- |
| `embeddedtbot/` | Run `tbot` inside the integration's process (used by operator and most access plugins). |
| `plugindata/` | Read/write the per-plugin annotation on `access_request` resources to track plugin state across restarts. |
| `watcherjob/` | Restartable watcher loop with backoff. |
| `tar/` | tar helpers for tests. |
| `tctl/` | Programmatic `tctl` invocation (test-only). |
| `testing/` | Integration-test rig (`testing/integration/`). |
| `logger/` | Logging helpers. |
| `credentials/` | Identity-file loading helpers. |
| `addr.go`, `bail.go`, `email.go`, `escape.go`, `http.go`, `process.go`, `runner.go`, `signals.go`, `errors.go` | Top-level utilities. |

---

## 4. Build & Test

```bash
make -C integrations/operator build              # operator binary
make -C integrations/operator test               # operator tests (needs envtest)
make -C integrations/operator crd                # regenerate CRDs

make -C integrations/terraform build             # provider binary
make -C integrations/terraform gen               # regenerate tfschema/*
make -C integrations/terraform docs              # regenerate DOCS.md from schema
make -C integrations/terraform test

make -C integrations/terraform-mwi build
make -C integrations/event-handler build

make -C integrations/access/slack build          # any one plugin
```

CI workflows: each integration has its own subset of workflows under `.github/workflows/` (operator-*, terraform-*, plugin-*, event-handler-*).

---

## 5. Deployment (Helm)

| Integration | Helm chart |
| --- | --- |
| Operator | bundled as a subchart inside `examples/chart/teleport-cluster/templates/operator/` |
| Kube-agent | `examples/chart/teleport-kube-agent/` (the agent itself, not the updater) |
| Kube-agent-updater | `examples/chart/teleport-kube-updater/` |
| Terraform provider | run *outside* the cluster (it's a CLI plugin) |
| event-handler | `examples/chart/event-handler/` |
| access/slack | `examples/chart/access/slack/` |
| access/jira | `examples/chart/access/jira/` |
| access/pagerduty | `examples/chart/access/pagerduty/` |
| access/msteams | `examples/chart/access/msteams/` |
| access/mattermost | `examples/chart/access/mattermost/` |
| access/discord | `examples/chart/access/discord/` |
| access/datadog | `examples/chart/access/datadog/` |
| access/email | `examples/chart/access/email/` |
| tbot | `examples/chart/tbot/` |
| tbot SPIFFE daemonset | `examples/chart/tbot-spiffe-daemon-set/` |

The opsgenie / servicenow plugins do not ship Helm charts (Cloud-hosted only).

---

## 6. Integration Seams

(See `integration-architecture.md`.)

| Seam | This part ↔ | Channel |
| --- | --- | --- |
| S9 | `integrations/operator/controllers/` ↔ Auth gRPC | gRPC, embedded tbot |
| S10 | `integrations/terraform/provider/` ↔ Auth gRPC | gRPC, identity file |
| S11 | `integrations/access/<plugin>/` ↔ Auth gRPC (`access_request` watcher) | gRPC stream, MachineID |
| S12 | `integrations/event-handler/` ↔ Auth gRPC (audit stream) + Fluentd | gRPC inbound, mTLS Fluentd outbound |
| S13 | `integrations/kube-agent-updater/` ↔ Kubernetes API + Teleport version registry | controller-runtime + HTTPS |

---

## 7. Pointer Index

| Concept | File |
| --- | --- |
| Operator main | `integrations/operator/main.go` |
| Operator config | `integrations/operator/config.go` |
| Operator CRD generator | `integrations/operator/crdgen/cmd/protoc-gen-crd/main.go` |
| Operator generated CRDs | `integrations/operator/config/crd/bases/` |
| Modern proto-driven CR types | `integrations/operator/apis/resources/v1/` |
| Generic reconciler | `integrations/operator/controllers/reconcilers/` |
| Terraform provider entry | `integrations/terraform/main.go` |
| Terraform per-resource schema | `integrations/terraform/tfschema/<resource>/v1/<resource>.go` |
| Terraform codegen configs | `integrations/terraform/protoc-gen-terraform-*.yaml` |
| Terraform MWI provider | `integrations/terraform-mwi/main.go` + `provider/` |
| Event-handler entry | `integrations/event-handler/main.go` |
| Event-handler forwarding | `integrations/event-handler/lib/` |
| Kube-agent-updater entry | `integrations/kube-agent-updater/cmd/teleport-kube-agent-updater/main.go` |
| Kube-agent-updater image verification | `integrations/kube-agent-updater/pkg/img/` |
| Access plugin base class | `integrations/access/common/bot.go::BaseApp` |
| Access plugin auth/storage | `integrations/access/common/auth/storage/` |
| Embedded tbot | `integrations/lib/embeddedtbot/` |
| Watcher loop | `integrations/lib/watcherjob/` |
| Plugin data annotation | `integrations/lib/plugindata/` |

For everything else, refer to `findings/integrations-rust-bpf-tools.md`.
