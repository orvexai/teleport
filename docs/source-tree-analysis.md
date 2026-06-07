# Source Tree Analysis

Annotated tree of the Teleport monorepo as of `2026-05-24`. Folders marked with **★** are *critical paths* — most architectural change happens here. Counts in parentheses are file counts at scan time (`5893 .go`, `2815 .ts/.tsx`, `21 .rs`, `238 .proto`).

> Convention: each entry is one line. `→` marks an *integration seam* (this directory talks to that one across the part boundary).

```
teleport/
│
├── api/                              ★ Public Go SDK module — github.com/gravitational/teleport/api
│   ├── client/                       ★ Go client (used by tctl, tsh, tbot, integrations, third parties)
│   │   ├── proto/                    Connection-level proto bindings
│   │   ├── proxy/                    Proxy-aware dial helpers
│   │   ├── accesslist/  accessmonitoringrules/  crownjewel/  databaseobject/  …
│   │                                 Per-resource v1 client adapters (mirror of lib/auth/<resource>/)
│   ├── proto/teleport/               ★ Proto IDL for the public SDK (one sub-package per resource)
│   │   └── legacy/                   Pre-v1 monolithic AuthService proto (still active)
│   ├── gen/proto/go/                 Generated Go bindings (checked in)
│   ├── types/                        ★ Canonical resource types (Role, User, Node, Database, App, KubeCluster,
│   │                                 CertAuthority, TrustedCluster, OIDC/SAML/GitHub connectors, Lock,
│   │                                 SessionTracker, WindowsDesktop, AccessList, AccessRequest, etc.)
│   ├── utils/                        Util packages: keys (hardwarekey, piv), sshutils, retryutils, …
│   ├── mfa/  workloadidentity/  identityfile/  profile/  metadata/  observability/  …
│   └── go.mod                        Separate module (Go 1.25.10) — keeps SDK surface stable
│
├── proto/                            ★ Root proto IDL (NOT in api/ — internal services)
│   ├── teleport/lib/                 Per-subsystem internal RPC:
│   │   ├── multiplexer/              Multiplexer test proto
│   │   ├── teleterm/                 ★ Teleterm tshd daemon API   → consumed by web/packages/teleterm
│   │   ├── vnet/                     VNet daemon API
│   │   └── web/                      Web app gRPC over HTTP       → consumed by web/packages/teleport
│   ├── teleport/quicpeering/         Proxy-to-proxy QUIC peering
│   ├── teleport/relaypeering/        Relay peering
│   ├── teleport/relaytunnel/         Relay reverse tunnel
│   ├── teleport/storage/             On-disk storage formats
│   ├── accessgraph/                  Access Graph stream (v1, v1alpha) — for Identity Security product
│   └── prehog/                       Internal usage-event funnel
│
├── gen/                              Generated code (Go + proto). Result of `make grpc`.
│   ├── go/                           Generated Go (root module)
│   ├── proto/                        Other generators' outputs
│   └── preset-roles.json             Generated preset RBAC roles (consumed by docs ref generator)
│
├── lib/                              ★ Internal libraries (101 top-level packages, 429 dirs total)
│   │
│   ├── auth/                         ★ Auth Service — issuer of certificates, gRPC API endpoint
│   │   ├── webauthn/  webauthncli/  webauthntypes/  webauthnwin/  touchid/ — MFA
│   │   ├── machineid/                MachineID (bot) authentication
│   │   ├── join/                     Join tokens / methods (ec2, iam, k8s, github, azure, gcp, oracle)
│   │   ├── keystore/  keygen/        CA key management (HSM, software)
│   │   ├── moderation/  loginrule/   Policy primitives
│   │   ├── recordingencryption/      Age-encrypted session recordings (RFD 0042)
│   │   ├── trust/                    Trusted-cluster API
│   │   ├── webauthn/ + okta/ + saml*/oidc*/github*/  scim/
│   │   ├── accessmonitoringrules/  notifications/  presence/  dbobject/  okta/  …
│   │   │                             v1 gRPC services (one per resource, replacing monolithic AuthService)
│   │   └── scopes/  workloadcluster/ Scoped RBAC, workload identity
│   │
│   ├── authz/                        ★ Authorization (RBAC, role evaluation, AccessChecker)
│   ├── cryptosuites/                 Pluggable crypto suites (RSA / ECDSA / Ed25519 / HSM-backed)
│   ├── jwt/                          JWT signing/verification (app access, MachineID, MCP)
│   ├── devicetrust/  devicetpm/      Device trust + TPM attestation
│   ├── hardwarekey/  secret/  secretsscanner/
│   ├── tlsca/  sshca/  subca/        Cert authorities (x509 + SSH + sub-CA)
│   ├── winpki/                       Windows PKI (smartcard cert issuance for desktop)
│   ├── scopes/  loginrule/           Scoped access + login rules
│   ├── decision/                     Policy decision engine
│   ├── boundkeypair/                 Bound key pair for MachineID
│   │
│   ├── service/                      ★ Service supervisor — wires services together at startup
│   ├── modules/                      Build-tag-gated OSS vs Enterprise feature toggle
│   ├── config/                       Config file parser + validator (file-config / cluster-config)
│   ├── configurators/                Init-time auto-configurators (cloud IAM, etc.)
│   ├── componentfeatures/            Feature gates per component
│   │
│   ├── srv/                          ★ Protocol-specific services (the workload Teleport proxies)
│   │   ├── regular/                  SSH node implementation (sshserver.go)
│   │   ├── forward/                  Forwarding-node SSH (proxy → upstream OpenSSH)
│   │   ├── git/                      Git protocol proxy
│   │   ├── mcp/                      Model Context Protocol proxy
│   │   ├── db/                       ★ Database access — engines for postgres, mysql, mongodb, redis,
│   │   │   ├── postgres/             …  snowflake, clickhouse, dynamodb, opensearch, sqlserver, oracle,
│   │   │   ├── mysql/  mongodb/      …  redis (incl. elasticache/memorydb), spanner, cassandra
│   │   │   ├── snowflake/  spanner/  Each engine implements db/common.Engine
│   │   │   ├── elasticsearch/  cloud/  healthchecks/  endpoints/  secrets/  vnet/
│   │   ├── app/                      App access (HTTP)        → aws/, azure/, gcp/ for cloud-console SSO
│   │   ├── desktop/                  ★ Desktop access (RDP)
│   │   │   ├── rdp/                  ★ Rust workspace (rdpclient, decoder)   ← CGo FFI
│   │   │   ├── rdpstate/  tdp/       Teleport Desktop Protocol (browser ↔ desktop)
│   │   ├── discovery/                Cloud resource discovery (EC2/RDS/EKS/AKS/GKE/Azure/GCP)
│   │   ├── alpnproxy/                ★ ALPN multiplexer (one TCP port → SSH/HTTPS/DB/RDP)
│   │   ├── ingress/  transport/      Ingress (PROXY protocol), gRPC transport
│   │   └── server/                   Generic agent + installer
│   │
│   ├── proxy/                        ★ Teleport Proxy service (terminates user traffic)
│   ├── reversetunnel/  reversetunnelclient/  relaytunnel/  relaytransport/  relaypeer/
│   │                                 Reverse tunnel / relay mesh (agents → proxies)
│   ├── multiplexer/                  PROXY proto + ALPN routing at the wire level
│   │
│   ├── kube/                         Kubernetes specifics (kubeconfig, kube-exec)
│   ├── openssh/  sshutils/  sshagent/  agentless/  asciitable/  asciitable_test/
│   ├── player/  session/             Session recording playback + session model
│   ├── plugin/  plugins/             Plugin SDK (loaded at runtime by access plugins)
│   │
│   ├── backend/                      ★ Storage backend abstraction
│   │   ├── etcdbk/                   etcd
│   │   ├── dynamo/                   DynamoDB         (most common production)
│   │   ├── firestore/                GCP Firestore
│   │   ├── pgbk/                     Postgres
│   │   ├── spanner/                  GCP Spanner
│   │   ├── lite/                     SQLite           (dev / single-node)
│   │   ├── kubernetes/               k8s ConfigMaps   (used by tbot in k8s)
│   │   └── memory/  test/            Test backends
│   ├── services/                     ★ Resource CRUD + watch (built on lib/backend)
│   │   └── local/                    The production implementation
│   ├── cache/                        ★ Auth-side cache (replays watcher streams to clients)
│   │
│   ├── events/                       ★ Audit events + session recordings
│   │   ├── athena/                   Athena/Glue (S3-backed event store)
│   │   ├── dynamoevents/             DynamoDB events
│   │   ├── firestoreevents/  pgevents/
│   │   ├── s3sessions/  gcssessions/  azsessions/    Session recording stores
│   │   ├── filesessions/  recorder/                   Local + recording machinery
│   │
│   ├── web/                          ★ HTTP REST + WebSocket API consumed by the React UI
│   │   ├── apiserver.go              Master HTTP router (`h.GET/POST/PUT/DELETE(...)`)
│   │   ├── terminal/  desktop/  session/  scripts/  mfajson/  app/  templates/  ui/
│   │   ├── recordingplayback*  ssh_io.go  conn_upgrade.go
│   │   │                             WebSocket session, recording playback, HTTP-upgrade dance
│   │   └── join_tokens / mwi_wizards / okta / machineid / managed_updates / …
│   │                                 Per-feature HTTP handlers
│   ├── httplib/                      Shared HTTP helpers (CSRF, error mapping)
│   │
│   ├── client/                       ★ tsh / programmatic client library
│   │   ├── ssh/  sso/  identityfile/  proxy/  reexec/  conntest/
│   │   ├── db/  mcp/  mfa/  mfatypes/  terminal/  tncon/  escape/  debug/
│   │   └── clientcache/              Cached resource lookups for the CLI
│   │
│   ├── tbot/                         MachineID agent library (used by tool/tbot/ + embedded)
│   ├── teleterm/                     ★ tshd daemon (Go) consumed by the Electron app   → web/packages/teleterm
│   │
│   ├── vnet/                         ★ Teleport VNet — TUN-device transparent proxy
│   │   ├── daemon/  db/  dns/  diag/  polkit/  systemdresolved/
│   │
│   ├── healthcheck/  inventory/  watcher/                  Operational
│   ├── automaticupgrades/  autoupdate/  versioncontrol/    Release management
│   │   ├── autoupdate/agent/  autoupdate/lookup/  autoupdate/report/  autoupdate/rollout/  autoupdate/tools/
│   ├── usagereporter/  release/                            Phoning home + release advertisement
│   │
│   ├── bpf/  cgroup/                                       eBPF SSH session observability   → bpf/
│   │
│   ├── cloud/                                              Cloud SDK adapters (AWS/Azure/GCP)
│   ├── aws/  gcp/  msgraph/                                Cloud-specific helpers
│   ├── linux/  darwin/  system/  systemd/  windowsexec/  windowsservice/   Per-OS bits
│   ├── connectmycomputer/                                  "Connect My Computer" flow (Teleterm)
│   ├── healthcheck/  fipscheck/  resourceusage/  limiter/  Operational
│   ├── benchmark/                                          tsh bench / DB bench
│   ├── ui/                                                 (legacy) shared UI types for the web layer
│   ├── itertools/  fixtures/  expression/  labels/  uds/  tlscatest/  tpm/  …
│   ├── utils/                                              ★ Huge utilities grab-bag (~50 sub-packages)
│   │   ├── log/  log/eventlog/  log/oslog/                 Structured logging across OS-specific sinks
│   │   ├── grpc/  proxy/  process/  registry/  retryutils/  signal/  parse/  …
│   │   ├── mcptest/  mcputils/                             MCP testing helpers
│   │   └── interval/  fanoutbuffer/  genmap/  sortcache/  …
│   ├── observability/                                      OpenTelemetry wiring
│   ├── accessgraph/                                        Access Graph stream client (Identity Security)
│   ├── accesslists/  accessmonitoring/                     Access Lists + Monitoring evaluation
│   ├── usertasks/                                          User Tasks (Identity Security action items)
│   ├── puttyhosts/                                         PuTTY .ppk export
│   ├── relaypeer/  relaytransport/  relaytunnel/           Relay subsystem
│   ├── reversetunnel/  reversetunnelclient/                Reverse tunnel
│   ├── join/                                               Join token plumbing (shared with lib/auth/join)
│   ├── plugin/  plugins/  loglimit/  resumption/           Misc
│
├── tool/                             ★ CLI binaries (all Go except fdpass-teleport which is Rust)
│   ├── teleport/                     The `teleport` daemon — `teleport start --config=…`
│   │   ├── main.go                   Binary entry
│   │   ├── common/                   Sub-commands (configure, status, version, install, debug, scp, sftp)
│   │   └── testenv/                  In-process test cluster helper
│   ├── tctl/                         Admin CLI
│   │   ├── main.go
│   │   ├── common/                   `tctl get/create/edit/rm/auth/tokens/users/roles/…` + per-resource cmds
│   │   │   └── accessgraph/ accessmonitoring/ clusterconfig/ databaseobject/ discovery/ loginrule/ mfa/ plugin/ recordings/ resources/ subca/ top/  …
│   │   └── sso/                      `tctl sso configure | tester` for SSO connectors
│   ├── tsh/                          User / agent CLI
│   │   ├── main.go
│   │   └── common/                   Mountain of subcommands: login, ssh, app, db, kube, proxy, request,
│   │                                 mfa, daemon, ports, scan, status, headless, mcp, gh, recordings, …
│   ├── tbot/                         MachineID agent CLI       → wraps lib/tbot/
│   ├── teleport-update/              Auto-updater binary       → uses lib/autoupdate/
│   ├── fdpass-teleport/              ★ Rust binary (NOT in Cargo workspace) — passes file descriptors over
│   │                                 Unix-domain sockets. Used by tsh for FIPS/PIV scenarios where the
│   │                                 child must own the FD lifetime.
│   └── common/                       Shared CLI helpers (fido2/, touchid/, webauthnwin/)
│
├── integration/                      Integration tests against a real teleport cluster
├── integrations/                     ★ Separate-binary integrations (some have own go.mod)
│   ├── operator/                     ★ Kubernetes operator (kubebuilder + controller-runtime)
│   │   ├── apis/resources/{v1,v2,v3,v5}/     Generated CRD API types
│   │   ├── crdgen/cmd/{protoc-gen-crd,protoc-gen-crd-docs}/   Generates CRDs from Teleport protos
│   │   ├── controllers/{reconcilers,resources}/                Per-resource reconcilers
│   │   ├── config/crd/bases/                  Generated CRD YAML
│   │   ├── main.go  config.go  namespace.go
│   ├── terraform/                    ★ Terraform provider (own go.mod, hashicorp/terraform-plugin-framework)
│   │   ├── tfschema/<resource>/v1/   Generated tfschema per resource (~40 resources)
│   │   ├── examples/resources/<resource>/      HCL examples per resource
│   │   ├── provider/                 Provider plumbing
│   │   └── gen/                      Generator outputs
│   ├── terraform-mwi/                Separate Terraform provider for Machine & Workload Identity (own go.mod)
│   ├── terraform-modules/            Terraform-module renderer (generators + templates)
│   ├── event-handler/                Audit-event egress to SIEMs (own go.mod, alecthomas/kong + diskv queue)
│   ├── kube-agent-updater/           K8s agent rolling updater (controller-runtime; shares root go.mod)
│   ├── access/                       ★ Access plugins (chat/ITSM integrations for access_request flows)
│   │   ├── common/                   Shared plugin scaffolding (auth/storage, teleport client, recipient)
│   │   ├── slack/  jira/  pagerduty/  opsgenie/  servicenow/  msteams/  mattermost/  datadog/  discord/  email/
│   │   │   ├── cmd/teleport-<plugin>/    Per-plugin binary
│   │   │   └── testlib/                   Per-plugin test rig
│   │   ├── accesslist/  accessmonitoring/  accessrequest/   Cross-plugin features
│   ├── lib/                          Shared library: embeddedtbot/, plugindata/, watcherjob/, tar/, tctl/, …
│   └── hack/                         Build hacks (get-version, etc.)
│
├── web/                              ★ TypeScript / React workspace (pnpm) — Apache-2.0
│   ├── packages/
│   │   ├── teleport/                 ★ The web app served at https://<cluster>/web
│   │   │   ├── src/                  Feature folders: Apps, Servers, Sessions, Audit, AccessRequests,
│   │   │   │                          Discover, Roles, Users, Welcome, Login, Authn, Console, Cluster,
│   │   │   │                          Notifications, MCP, services/, components/
│   │   ├── design/                   ★ Design system (~60 components in src/<Component>/)
│   │   │   └── src/{Alert,Box,Button,Card,DataTable,Dialog,Flex,Icon,Image,Indicator,Input,Label,
│   │   │             Link,Menu,Modal,Popover,Tabs,Text,Toggle,Tooltip,Theme,…}
│   │   ├── shared/                   ★ Cross-package utilities + components + ironrdp WASM wrapper
│   │   │   ├── components/           Many feature-shared components (AccessRequests, AnimatedTerminal,
│   │   │   │                          DesktopSession, Editor, FieldInput/Select/CheckBox, FileTransfer,
│   │   │   │                          LatencyDiagnostic, MenuLogin, Markdown, Highlight, …)
│   │   │   └── libs/ironrdp/         ★ Rust crate compiled to WASM (cdylib)   → in Cargo workspace
│   │   ├── build/                    Build tooling (babel/swc presets, vite config, jest config)
│   │   └── teleterm/                 ★ Electron app "Teleport Connect" (separate license: Apache-2.0)
│   │       ├── src/                  Renderer + main process (TS)
│   │       ├── electron-builder-config.js
│   │       └── build_resources/
│   ├── scripts/                      Build helpers (`run-storybook.sh`, `list-installed-versions.sh`,
│   │                                 `clean-up-ironrdp-artifacts.mjs`, `print-coverage-link.sh`)
│   └── .storybook/                   Storybook config + mocks
│
├── e/                                ★ Enterprise overlay (git submodule → teleport.e) — NOT cloned here
│                                     Referenced from e_imports.go, webassets_embed_ent.go.
│                                     OSS build is self-contained; enterprise build needs this submodule.
│
├── bpf/                              ★ eBPF C programs
│   ├── enhancedrecording/            Process + network + file tracers for SSH session recording
│   ├── arm64/  x86/                  Per-architecture build outputs / vmlinux references
│   └── README.md                     Build instructions       → consumed by lib/bpf/
│
├── examples/                         User-facing deployment recipes
│   ├── chart/                        ★ Helm charts
│   │   ├── teleport-cluster/         Main cluster chart (charts/, templates/, tests/, values.yaml)
│   │   ├── teleport-kube-agent/      Agent chart
│   │   ├── teleport-kube-updater/    Agent-updater chart
│   │   ├── teleport-relay/           Relay chart
│   │   ├── tbot/  tbot-spiffe-daemon-set/    MachineID Helm charts
│   │   ├── event-handler/                    Event-handler chart
│   │   ├── access/{slack,jira,pagerduty,msteams,mattermost,discord,datadog,email}/
│   │   └── CONTRIBUTING.md  Makefile  index.html
│   ├── aws/terraform/{starter-cluster,ha-autoscale-cluster}/   AWS Terraform recipes
│   ├── terraform/  terraform-starter/  workload-clusters/      Other Terraform examples
│   ├── systemd/  upstart/  launchd/                            OS-level init recipes
│   ├── k8s-auth/  desktop-registration/  etcd/  grafana/       Misc one-off recipes
│   ├── go-client/  api-sync-roles/  service-discovery-api-client/  Example API consumers (Go)
│   ├── athena/  dynamoathenamigration/                         Storage migration helpers
│   ├── identity-activity-center/  jwt/  mcp-servers/           Feature examples
│   ├── access-plugin-minimal/                                  Minimal access-plugin skeleton
│   ├── teleport-usage/  bench/                                 Usage + benchmarking
│   └── resources/                                              Stand-alone example resource YAMLs
│
├── docs/                             User-facing docs (Docusaurus MDX site)
│   ├── pages/                        Sidebar tree (see existing-documentation-inventory.md)
│   ├── img/  vale-styles/  config.json  sidebar.json  cspell.json
│   ├── README.md  preflight.md  prerelease.md  postrelease.md
│   ├── findings/                     ← AI-generated findings (THIS workflow)
│   ├── architecture-*.md             ← AI-generated per-part architectures (THIS workflow)
│   └── index.md / project-*.md / *.md ← AI-generated supporting docs
│
├── rfd/                              230 Requests For Discussion (design docs)
│
├── build.assets/                     ★ Build infrastructure (buildboxes, Docker images, signing)
│   ├── README.md  arch.mk  build-common.sh  charts/  download-hashes/  dump-preset-roles/
│   ├── buildbox/                     Buildbox image source
│   ├── Dockerfile  Dockerfile-arm  Dockerfile-bpf  Dockerfile-centos7  Dockerfile-grpcbox  Dockerfile-node
│   ├── tooling/cmd/{benchfind,buf-plugin-linters,difftest,render-helm-ref,resource-ref-generator,gobuildverify}/
│   ├── pam/  fips-files/  gpg/  macos/  pkgconfig/  rpm/  rpm-sign/  windows/
│   └── tools/                        Auxiliary scripts
│
├── .github/                          GitHub-hosted CI/CD
│   ├── workflows/                    51 YAML workflows
│   ├── actions/                      Custom composite actions
│   ├── services/                     Service definitions for workflows
│   ├── CODEOWNERS  PULL_REQUEST_TEMPLATE.md  ISSUE_TEMPLATE/
│   ├── dependabot.yml  renovate.json
│
├── e2e/                              End-to-end test harness (Playwright; pnpm workspace member)
│   ├── tests/  helpers/  config/  runner/  scripts/  testdata/  node/  aws/
│
├── entitlements/                     macOS code-signing entitlements
├── fixtures/                         Test fixtures (alpine/, certs/, ci-teleport-rbac/, etcdcerts/, kind/, login.defs/)
├── fuzz/                             Go fuzz harnesses
├── session/                          Generated session protobuf assets (legacy location)
│
├── skills/                           In-repo agent skills (not authored by Teleport eng — see skills/README.md)
├── assets/                           Marketing assets, install scripts, load-test rigs (assets/loadtest/)
│
├── go.mod / go.sum                   Root Go module (`github.com/gravitational/teleport`, Go 1.25.10)
├── api/go.mod                        Separate API module
├── integrations/{terraform,terraform-mwi,event-handler}/go.mod    Separate integration modules
├── Cargo.toml / Cargo.lock           Rust workspace (3 crates: rdpclient, decoder, ironrdp WASM)
├── tool/fdpass-teleport/Cargo.toml   Standalone Rust binary (NOT in workspace)
├── rust-toolchain.toml               Pinned Rust toolchain
├── package.json / pnpm-lock.yaml     Web workspace root
├── pnpm-workspace.yaml               Workspace packages (web/packages/*, e/web/*, e2e, e/e2e)
├── tsconfig.{base,node,}.json        TypeScript configs
├── babel.config.js  jest.config.js  eslint.config.mjs  svgo.config.mjs  .pnpmfile.cjs
├── .oxlintrc.jsonc  .oxfmtrc.json    Linter / formatter configs (NOT eslint/prettier)
├── .golangci.yml                     Go lint config
├── buf.yaml  buf-{go,connect-go,gogo,ts,legacy}.gen.yaml  buf.lock     Proto codegen + lint
├── Makefile  common.mk  version.mk  darwin-signing.mk                  Build orchestration
├── constants.go  version.go  doc.go  metrics.go                        Top-level Go (root package)
├── e_imports.go  webassets_embed{,_ent,_noembed}.go                    Enterprise / webassets glue
├── README.md  AGENTS.md  CONTRIBUTING.md  SECURITY.md  CHANGELOG.md
├── CODE_OF_CONDUCT.md  BUILD_macos.md  CLA.md  LICENSE
└── .gitignore  .gitattributes  .gitmodules  .npmrc  .tflint.hcl  .trivyignore
```

---

## Critical Folder Quick Reference

| Folder | Role |
| --- | --- |
| `lib/service/` | The supervisor — wires every service together at process start. |
| `lib/auth/` | Auth Service. Issues all certificates. The gRPC API root. |
| `lib/authz/` | Authorization checker (RBAC, AccessChecker). |
| `lib/srv/` | The protocol-specific *workloads* Teleport proxies (SSH, DB, App, K8s, Desktop, MCP, Git). |
| `lib/proxy/` | The proxy that fronts the cluster. |
| `lib/backend/` | KV storage abstraction; sub-pkgs are concrete backends. |
| `lib/services/local/` | Resource CRUD layer above `lib/backend`. |
| `lib/cache/` | Server-side cache that fans out resource changes via watchers. |
| `lib/events/` | Audit-event spine + session-recording stores. |
| `lib/web/` | HTTP REST + WebSocket API consumed by the React UI. |
| `lib/client/` | The shared client library used by `tsh` and embedded by integrations. |
| `lib/teleterm/` | `tshd` daemon that backs Teleport Connect. |
| `lib/srv/desktop/rdp/` | Go ↔ Rust FFI border for RDP. |
| `lib/vnet/` | TUN-device transparent proxy (VNet). |
| `lib/autoupdate/` | Auto-updater logic; binary lives in `tool/teleport-update/`. |
| `lib/bpf/` + `bpf/` | eBPF SSH session observability. |
| `api/` | Public Go SDK (separate module). |
| `proto/` + `api/proto/` | Proto IDL (all RPC is defined here). |
| `gen/` + `api/gen/` | Generated bindings (checked in). |
| `tool/teleport/` | Daemon binary. |
| `tool/tctl/` | Admin CLI. |
| `tool/tsh/` | User CLI. |
| `tool/tbot/` | MachineID CLI. |
| `tool/fdpass-teleport/` | **Rust** FD-passing helper (outside the workspace). |
| `integrations/operator/` | Kubernetes operator. |
| `integrations/terraform/` + `terraform-mwi/` | Terraform providers. |
| `integrations/access/<plugin>/` | Per-vendor access plugins. |
| `web/packages/teleport/` | React web UI. |
| `web/packages/design/` | Design system. |
| `web/packages/shared/libs/ironrdp/` | Browser-side RDP (Rust → WASM). |
| `web/packages/teleterm/` | Electron renderer + main. |
| `rfd/` | 230 RFDs — the canonical *why* archive. Search by filename keyword. |
| `examples/chart/` | Helm charts users actually deploy. |
| `build.assets/` | Build infrastructure. |
| `.github/workflows/` | 51 CI workflows. |
| `e/` | Enterprise submodule (not present in this workspace). |

---

## Integration Seams (Across Parts)

These are the places where two parts meet — change carefully.

| From | To | Channel |
| --- | --- | --- |
| `web/packages/teleport/src/services/` | `lib/web/` | HTTP + WebSocket over HTTPS |
| `web/packages/teleport/src/teleterm/` (consumer) — wait, `web/packages/teleterm/` | `lib/teleterm/` | gRPC over a local Unix socket / named pipe |
| `lib/srv/desktop/rdp/rdpclient/` (Go) | `lib/srv/desktop/rdp/rdpclient/Cargo.toml` (Rust) | CGo + cbindgen-generated C header |
| `web/packages/shared/libs/ironrdp/` (browser JS) | `web/packages/shared/libs/ironrdp/Cargo.toml` (Rust) | WASM via `wasm-bindgen`, built by `make build-ironrdp-wasm` |
| `lib/bpf/` (Go) | `bpf/*.bpf.c` | `cilium/ebpf` loader; programs compiled in `Dockerfile-bpf` |
| `tool/tsh/common/` (Go) | `tool/fdpass-teleport/` (Rust) | Unix socket; tsh spawns fdpass and passes an FD |
| `integrations/operator/controllers/` (Go) | `lib/auth/` via gRPC (api/client) | gRPC, authenticated by an embedded `tbot` |
| `integrations/terraform/provider/` (Go) | `lib/auth/` via gRPC (api/client) | gRPC, authenticated by an identity file |
| `integrations/access/<plugin>/` (Go) | `lib/auth/` via watcher | gRPC stream of `access_request` events |
| `lib/proxy/` (Go) | other Teleport proxies | proxy-to-proxy gRPC peering (`lib/proxy/peer/`); QUIC variant (`proto/teleport/quicpeering/`) |
| `lib/reversetunnel/` | agents (anywhere) | SSH-tunnel + gRPC, reused for trusted clusters |
| `lib/teleterm/apiserver/` (Go daemon) | `web/packages/teleterm/src/` (Electron renderer) | gRPC over the proxied Electron-main channel |
| `lib/web/scripts/node-join/` (Go template) | shell installer script run on remote hosts | text/template render → bash |

---

## Notes on Multi-Module Builds

The repo is built from **six Go modules**, **two Rust workspaces**, and **one pnpm workspace**:

- Go modules (six):
  1. `github.com/gravitational/teleport` — root
  2. `github.com/gravitational/teleport/api` — public SDK
  3. `github.com/gravitational/teleport/integrations/terraform` — Terraform provider
  4. `github.com/gravitational/teleport/integrations/terraform-mwi` — MWI Terraform provider
  5. `github.com/gravitational/teleport/integrations/event-handler` — audit event forwarder
  6. `tool/fdpass-teleport/`'s sibling — *(no, fdpass is Rust; see below)*
  - **Note**: `integrations/operator/` and `integrations/kube-agent-updater/` are *not* separate modules — they live in the root module.

- Rust workspaces / crates:
  1. Workspace at root `Cargo.toml` — 3 members:
     - `lib/srv/desktop/rdp/rdpclient/` (staticlib for CGo)
     - `lib/srv/desktop/rdp/decoder/`   (staticlib + lib)
     - `web/packages/shared/libs/ironrdp/` (cdylib for WASM)
  2. Standalone crate (own `Cargo.toml`, `[workspace]` declared empty): `tool/fdpass-teleport/`

- Web workspace (pnpm): `web/packages/*`, `e/web/*`, `e2e`, `e/e2e`.
