# Component Inventory

> **Scope.** This document inventories *reusable building blocks* across the codebase: UI components (web + Teleterm), shared Go packages, RPC services, and integration helpers. The aim is to make "is there already a thing for X?" answerable in one place.

---

## 1. Web UI Components

### 1a. Design system — `web/packages/design/src/`

The base component library. Apache-2.0. Used by `web/packages/teleport` and `web/packages/teleterm`. Each folder is one primitive, each with its own story.

| Category | Components |
| --- | --- |
| **Layout & containment** | `Box`, `Flex`, `Card`, `CardError`, `CardSuccess`, `CardIcon`, `CardTile`, `MultiRowBox`, `CollapsibleInfoSection`, `Modal`, `Dialog`, `DialogConfirmation`, `Tabs`, `SlideTabs`, `StepSlider` |
| **Buttons & menus** | `Button`, `ButtonIcon`, `ButtonLink`, `ButtonSelect`, `ButtonWithMenu`, `Menu` |
| **Inputs (raw)** | `Input`, `TextArea`, `Checkbox`, `RadioButton`, `RadioGroup`, `Toggle`, `FieldRadio`, `Label`, `LabelInput` |
| **Display / data** | `DataTable`, `Text`, `Indicator`, `LabelState`, `Mark`, `Pill`, `Status`, `StatusIcon`, `SyncStamp`, `Tag`, `ShimmerBox`, `AnimatedProgressBar` |
| **Media / icons** | `Icon`, `ButtonIcon`, `ResourceIcon`, `Image`, `SVGIcon` |
| **Overlays** | `Popover`, `Tooltip`, `Alert` |
| **Navigation** | `Link`, `TopNav` |
| **Themeing** | `Theme`, `ThemeProvider` (light + dark) |
| **System / utility** | `system/` (styled-system responsive props), `utils/`, `datetime/` |

### 1b. Shared components — `web/packages/shared/components/`

Higher-level components reused across the web app *and* Teleterm.

| Component | Purpose |
| --- | --- |
| `AccessRequests/*` | Access request submit / review flows (shared between web + Teleterm) |
| `AdvancedSearchToggle` | Filter pill row |
| `AnimatedTerminal`, `DemoTerminal` | Marketing-grade animated terminal |
| `AwsLaunchButton` | "Launch in AWS" CTA |
| `ButtonFileUpload`, `ButtonSso`, `ButtonTextWithAddIcon`, `ButtonWithAddIcon` | Higher-level button variants |
| `Controls` | Form control group wrappers |
| `CopyButton` | Copy-to-clipboard with feedback |
| `DesktopSession` | RDP-over-TDP session UI (consumes `libs/ironrdp` WASM) |
| `Editor` | Code editor wrapper (CodeMirror) |
| `EmptyState` | Friendly empty list state |
| `ErrorSuspenseWrapper` | Suspense + ErrorBoundary combo |
| `FieldCheckbox`, `FieldInput`, `FieldMultiInput`, `FieldSelect`, `FieldTextArea` | react-hook-form integrated form fields |
| `FileTransfer` | SCP/SFTP transfer UI |
| `Highlight` | Search highlight wrapper |
| `LatencyDiagnostic` | Latency probe UI used in session diagnostics |
| `ListFilters` | Generic filter sidebar |
| `Markdown` | Sanitised markdown renderer |
| `MenuAction`, `MenuLogin`, `MenuLoginWithActionMenu` | Action / login dropdown menus |
| `MissingPermissionsTooltip` | "You can't because…" tooltip |

### 1c. Web-app-specific components — `web/packages/teleport/src/components/`

Feature-scoped components live with their features (`src/Servers/components/`, `src/AccessRequests/components/`, etc.). Cross-feature shared components live in `src/components/` (sidebar, footer, header, router shim, etc.).

### 1d. Teleterm-specific components — `web/packages/teleterm/src/ui/`

Documents (tabs), cluster connect flows, in-app notifications, the VNet wizard. Same design system as the web app — different shell.

### 1e. Browser-side libraries — `web/packages/shared/libs/`

- `ironrdp/` — Rust crate compiled to WASM for browser-side RDP frame decode. See [`architecture-rust-rdp.md`](./architecture-rust-rdp.md).
- `tdp/` — TypeScript implementation of the Teleport Desktop Protocol.

---

## 2. Backend Reusable Packages (Go)

These are the most-imported `lib/` packages. If you're tempted to write a new utility, check here first.

### 2a. Utilities — `lib/utils/`

A grab-bag of ~50 sub-packages. Highlights:

| Sub-pkg | Purpose |
| --- | --- |
| `lib/utils/log/` | Structured logging via `log/slog`. Sub-pkgs: `eventlog/` (Windows), `oslog/` (macOS unified logging). |
| `lib/utils/grpc/` + `lib/utils/grpc/interceptors/` + `lib/utils/grpc/stream/` | gRPC interceptors + streaming helpers |
| `lib/utils/process/`, `lib/utils/signal/` | Process and signal management |
| `lib/utils/retryutils/` | Exponential backoff |
| `lib/utils/parse/` | Trait + label expression parser |
| `lib/utils/registry/` | Windows registry helpers |
| `lib/utils/interval/`, `lib/utils/typical/` | Periodic execution + statistical sampling |
| `lib/utils/fanoutbuffer/`, `lib/utils/spreadwork/` | Concurrency primitives |
| `lib/utils/sortcache/`, `lib/utils/sortmap/`, `lib/utils/genmap/`, `lib/utils/set/`, `lib/utils/slices/` | Generic data structures |
| `lib/utils/proxy/` | Proxy URL handling |
| `lib/utils/mlock/` | `mlock` syscall wrapper |
| `lib/utils/docsconfigs/` | Docs-config helpers |
| `lib/utils/diagnostics/` | Diagnostics endpoints |
| `lib/utils/dns/` | DNS helpers |
| `lib/utils/listener/` | Socket listener helpers |
| `lib/utils/hostid/` | Host identification |
| `lib/utils/teleportassets/` | Teleport-asset URL resolution |
| `lib/utils/packagemanager/`, `lib/utils/packaging/` | OS package installer integration |
| `lib/utils/socks/` | SOCKS proxy |
| `lib/utils/oidc/` | OIDC URL building |
| `lib/utils/mcptest/`, `lib/utils/mcputils/` | MCP testing utilities |
| `lib/utils/downloadretrier/` | Download retry with backoff |
| `lib/utils/darwinbundle/` | macOS bundle introspection |
| `lib/utils/gcp/` | GCP authentication helpers |
| `lib/utils/once/` | Run-once primitive |
| `lib/utils/iterutils/` (and `api/utils/iterutils/`) | Iterator helpers |

Public-SDK equivalents live under `api/utils/` (smaller, narrower API — designed for external consumers).

### 2b. Client library — `lib/client/`

The library that backs `tsh`. Used by Teleterm, integrations, and the operator.

| Sub-pkg | Purpose |
| --- | --- |
| `lib/client/` (top level) | `TeleportClient`, login, profile management |
| `lib/client/clientcache/` | Resource lookup caching |
| `lib/client/conntest/` | Connection diagnostics |
| `lib/client/db/` | Database client (drivers per engine) |
| `lib/client/debug/` | Client-side debug |
| `lib/client/escape/` | Shell escape handling |
| `lib/client/identityfile/` | Identity-file reader/writer |
| `lib/client/mcp/` | MCP client |
| `lib/client/mfa/`, `lib/client/mfatypes/` | MFA prompting |
| `lib/client/proxy/` | Proxy connection management |
| `lib/client/reexec/` | Re-exec helpers (for FIPS/PIV scenarios) |
| `lib/client/ssh/` | SSH client |
| `lib/client/sso/` | SSO callback handling |
| `lib/client/terminal/` | Local terminal control |
| `lib/client/tncon/` | Touch / native console |

### 2c. Auth helpers — `lib/auth/`

Reusable building blocks within Auth (CA + sign), see [`architecture-backend.md` §3a](./architecture-backend.md).

### 2d. Common service infrastructure

| Package | What it provides |
| --- | --- |
| `lib/service/` | `TeleportProcess` — the supervisor that wires services together |
| `lib/observability/` | OpenTelemetry initialization, tracing helpers |
| `lib/limiter/` | Rate-limiter middleware |
| `lib/inventory/` | Inventory Control Stream protocol |
| `lib/watcher/` | Resource watchers |
| `lib/cache/` | Server-side cache (see [`data-models.md` §5](./data-models.md)) |
| `lib/release/`, `lib/versioncontrol/`, `lib/autoupdate/` | Release / version / auto-update |
| `lib/healthcheck/` | Health-check endpoints |
| `lib/usagereporter/` | Usage telemetry (prehog) |
| `lib/resourceusage/`, `lib/loglimit/`, `lib/resumption/` | Operational concerns |
| `lib/plugin/`, `lib/plugins/` | Plugin SDK (loaded by access plugins) |
| `lib/asciitable/` | Pretty CLI tables (used by `tctl`, `tsh`) |
| `lib/itertools/` | Iterator helpers |
| `lib/expression/` | Predicate-language compiler (for `where` conditions) |
| `lib/labels/` | Label parsing and matching |

### 2e. Cloud-provider helpers

| Package | Provider |
| --- | --- |
| `lib/cloud/aws/`, `lib/aws/`, `lib/aws/identitycenter/`, `lib/aws/awsconfigfile/` | AWS (~60 SDK packages used) |
| `lib/cloud/azure/`, `lib/cloud/` (various Azure helpers) | Azure |
| `lib/cloud/gcp/`, `lib/gcp/`, `lib/msgraph/` | GCP + Microsoft Graph |

### 2f. OS-specific

| Package | OS |
| --- | --- |
| `lib/darwin/`, `lib/utils/darwinbundle/`, `lib/utils/log/oslog/` | macOS |
| `lib/windowsexec/`, `lib/windowsservice/`, `lib/winpki/`, `lib/auth/webauthnwin/`, `lib/utils/log/eventlog/` | Windows |
| `lib/linux/`, `lib/cgroup/`, `lib/bpf/` | Linux |
| `lib/systemd/`, `lib/system/`, `lib/uds/` | Unix |
| `lib/tpm/`, `lib/devicetpm/` | TPM 2.0 |

---

## 3. RPC Services (Generated Bindings)

Catalogue lives in [`api-contracts.md` §4](./api-contracts.md) + [`findings/backend-api-surface.md` §3-4](./findings/backend-api-surface.md). One-line summary: **~55 modern v1 gRPC services + the legacy 274-RPC monolithic `AuthService` + ~220 HTTP routes**.

---

## 4. Integration Helpers — `integrations/lib/`

Reusable building blocks across all integrations:

| Sub-pkg | What |
| --- | --- |
| `embeddedtbot/` | Run a `tbot` inside an integration's process. Used by the operator and most access plugins. |
| `plugindata/` | Read/write a plugin's annotation on `access_request` so plugin state survives restarts. |
| `watcherjob/` | Restartable watcher loop with backoff. |
| `tar/` | tar helpers for tests. |
| `tctl/` | Programmatic `tctl` invocation (tests). |
| `testing/` (`testing/integration/`) | Integration-test rig. |
| `logger/` | Logging helpers. |
| `credentials/` | Identity-file loading. |
| Top-level: `addr.go`, `bail.go`, `email.go`, `escape.go`, `http.go`, `process.go`, `runner.go`, `signals.go`, `errors.go` | Util grab-bag. |

---

## 5. Access-Plugin Scaffolding — `integrations/access/common/`

Shared chassis for the chat / ITSM access plugins. Reuse this, don't fork it.

| File / sub-pkg | Purpose |
| --- | --- |
| `bot.go::BaseApp` | The event-loop & lifecycle base class plugins extend |
| `app.go`, `config.go`, `constants.go`, `response.go`, `status.go` | App scaffolding |
| `annotations.go` | Reads `access_request` annotations |
| `recipient.go` | Vendor-recipient resolution (Teleport user → Slack channel / Jira queue / …) |
| `auth/`, `auth/storage/` | Credential loading and persisted state |
| `teleport/` | Teleport-side helpers (resource lookup, request status updates) |

Cross-plugin features (used by multiple plugin binaries):

| Sub-pkg | What |
| --- | --- |
| `accesslist/` | Access-list-driven notification logic |
| `accessmonitoring/` | Access monitoring rule evaluation |
| `accessrequest/` | Access-request shared logic |

---

## 6. Build & Codegen Helpers — `build.assets/tooling/cmd/`

Internal CLIs used during build / CI. Treat as opaque tools, not as libraries.

| Tool | Purpose |
| --- | --- |
| `apiversion/` | Generates `api/version.go` from `Makefile:VERSION` |
| `update-plist-version/` | Bumps `tsh.app` Info.plist version |
| `benchfind/` | Discovers benchmarks for `make bench` |
| `buf-plugin-linters/` | Custom buf-lint plugin (RFD-0153 `PAGINATION_REQUIRED`) |
| `difftest/` | Selective Go test runner by git diff |
| `render-helm-ref/` | Generates Helm chart reference docs from `values.yaml` |
| `resource-ref-generator/` | Generates Teleport-resource reference docs from Go struct tags |
| `gobuildverify/` | Verifies Go binary build provenance (see `spec.md`) |
| `apiversion`, `check`, `gci`, `goda`, `benchstat`, `gotestsum`, `helm-janitor` | Various test/lint helpers (under `build.assets/tools/` and `build.assets/tooling/`) |

---

## 7. Cross-References

- Per-part architecture: [`architecture-backend.md`](./architecture-backend.md), [`architecture-integrations.md`](./architecture-integrations.md), [`architecture-web.md`](./architecture-web.md), [`architecture-teleterm.md`](./architecture-teleterm.md), [`architecture-rust-rdp.md`](./architecture-rust-rdp.md).
- API surface: [`api-contracts.md`](./api-contracts.md).
- Resource catalog: [`data-models.md`](./data-models.md).
- Web UI deep dive: [`findings/web-and-teleterm.md`](./findings/web-and-teleterm.md).
- Integrations deep dive: [`findings/integrations-rust-bpf-tools.md` §1-6](./findings/integrations-rust-bpf-tools.md).
