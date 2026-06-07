# Backend API Surface

Documentation of the Teleport backend public API surface: gRPC (proto-defined) and HTTP/REST (`lib/web`, `lib/httplib`). Generated from sampling code; specific files cited inline. Sampled the entire `proto/`, `api/proto/`, `api/client/`, `lib/auth/grpcserver.go`, `lib/web/apiserver.go`; did not exhaustively read every per-service proto file or every v1 handler implementation.

---

## 1. Public Go SDK (`github.com/gravitational/teleport/api`)

The Go SDK lives in `/home/daniel/repos/teleport/api/` and is a separate Go module (`api/go.mod`) intended for external automation, plugins, tbot, terraform provider, etc.

**Top-level layout** (`api/`):
- `client/` — the `Client` struct (`api/client/client.go`, 6353 LOC) and credential constructors (`api/client/credentials.go`)
- `client/proto/` — generated bindings for the legacy monolithic `AuthService` (`api/client/proto/authservice_grpc.pb.go`)
- `client/<resource>/` — focused helpers wrapping per-resource v1 gRPC clients: `accesslist`, `accessmonitoringrules`, `crownjewel`, `databaseobject`, `discoveryconfig`, `dynamicwindows`, `externalauditstorage`, `gitserver`, `kubewaitingcontainer`, `linuxdesktop`, `okta`, `proxy`, `scim`, `scopes/access`, `secreport`, `statichostuser`, `summarizer`, `userloginstate`, `usertask`, `vnetconfig`, `webclient`
- `gen/proto/go/` — generated Go bindings for protos in `api/proto/`
- `types/` — hand-written Go types and converters for older resources
- `proto/` — *.proto sources owned by the public SDK module (re-published)
- `identityfile/`, `profile/`, `mfa/`, `accessrequest/`, `breaker/`, `metadata/`, `observability/`, `workloadidentity/` — utility packages

**Client construction.** `api/client/client.go:191` — `client.New(ctx, Config)`. The `Config` requires `Credentials` and either `Addrs` (list of `host:port`) or `Dialer`. The client tries each combination of credential and connection method in parallel (`api/client/client.go:274 connect()`); the first to succeed wins. Connection methods (lines 421-498):
- `authConnect` — direct gRPC to Auth Server
- `tunnelConnect` — through proxy reverse tunnel (needs `SSHClientConfig`)
- `proxyConnect` — through proxy via SSH
- `tlsRoutingConnect` — through proxy using ALPN/TLS routing
- `tlsRoutingWithConnUpgradeConnect` — TLS routing with ALPN connection upgrade (for HTTP/1.1-only load balancers)

**Auth methods (Credentials).** Defined in `api/client/credentials.go`:

| Constructor | Purpose |
|---|---|
| `LoadTLS(*tls.Config)` (L75) | raw TLS config, direct-to-Auth only |
| `LoadKeyPair(certFile, keyFile, caFile)` (L116) | static cert/key/CA files |
| `KeyPair(certPEM, keyPEM, caPEM)` (L652) | in-memory cert/key/CA bytes |
| `LoadIdentityFile(path)` (L185) | tctl-generated identity file (preferred for bots) |
| `LoadIdentityFileFromString(content)` (L267) | identity file from string (for env vars) |
| `LoadProfile(dir, name)` (L343) | `tsh login` profile from `~/.tsh/` — also yields `DefaultAddrs()` |
| `NewDynamicIdentityFileCreds(path)` (L462) | watches file for changes; for tbot |

All implement the `Credentials` interface (L46): `TLSConfig()`, `SSHClientConfig()`, `Expiry()`. `CredentialsWithDefaultAddrs` (L64) extends to provide default addresses (used by `LoadProfile`).

**Composite client type** (L147): `AuthServiceClient` embeds the legacy `proto.AuthServiceClient` plus per-resource clients (`AuditLogServiceClient`, `UserPreferencesServiceClient`, `NotificationServiceClient`, `RecordingEncryptionServiceClient`, `ScopedJoiningServiceClient`). New resource methods are typically reached via factory functions like `NewOktaClient`, or by calling `client.<ResourceClient>()` accessors on the returned `*Client`.

**Special clients** spun off from `Client`:
- `NewTracingClient` (L209) — OTLP trace forwarder
- `NewOktaClient` (L219) — Okta client wrapper
- `JoinServiceClient` — embedded `*JoinServiceClient` field, served on both Auth and Proxy

`api/client/README.md` points to `pkg.go.dev` and `goteleport.com/docs/admin-guides/api/` for usage examples.

---

## 2. Proto file layout

There are two top-level proto trees, with two separate Go modules generating from them:

| Tree | Purpose | Go module |
|---|---|---|
| `api/proto/teleport/` | Public, re-published in the SDK. Stable surface. | `github.com/gravitational/teleport/api` (output to `api/gen/proto/go/`) |
| `proto/teleport/` | Internal Teleport types/services. Not part of public SDK. | `github.com/gravitational/teleport` (output to `gen/proto/go/`) |
| `proto/accessgraph/`, `proto/prehog/` | External-facing: access graph stream from Teleport to the access graph service; prehog usage reporting | `gen/proto/go/{accessgraph,prehog}/...` |

**Naming convention.** Every modern proto declares `package teleport.<service>.<version>;` (e.g. `teleport.notifications.v1`, `teleport.dbobject.v1`, `teleport.scopes.access.v1`). Service names are PascalCase, version is always `v1` or `v1alpha` / `v1alpha1`. Each resource lives in its own directory with two files: `<resource>.proto` (messages, including the `<Resource>V1` type) and `<resource>_service.proto` (the gRPC service definition). Every modern message uses `option go_package = "github.com/gravitational/teleport/api/gen/proto/go/teleport/<svc>/v1;<svc>v1";`.

**The `legacy/` split.** `api/proto/teleport/legacy/` holds the original pre-v1 protos. They have flat `package proto;` declarations (no version suffix) and use gogoproto extensions for backwards-compatible field tagging:
- `api/proto/teleport/legacy/client/proto/authservice.proto` — the monolithic `AuthService` (274 RPCs)
- `api/proto/teleport/legacy/client/proto/proxyservice.proto` — `ProxyService` for proxy-to-proxy DialNode
- `api/proto/teleport/legacy/client/proto/joinservice.proto` — `JoinService` (also runs on proxy)
- `api/proto/teleport/legacy/types/types.proto` — `*V1`/`*V2` resource definitions (`UserV2`, `RoleV6`, `ServerV2`, etc.)
- `api/proto/teleport/legacy/types/events/events.proto` — every audit event type (`Session.Start`, `UserLogin`, etc.)
- `api/proto/teleport/legacy/types/{metadata,mfa_device,webauthn,wrappers,trusted_device_requirement,device,resources}.proto`

`buf.yaml` excludes these from modern lint rules (lines 27-36) and they're routed to `protoc-gen-gogofast` via `buf-gogo.gen.yaml`. The rest go through `protoc-gen-go` via `buf-go.gen.yaml`.

### Major services (api/proto/teleport, modern v1+)

From `grep -r "^service " api/proto/teleport/ | grep -v legacy`:

| Service | Proto file | Notes |
|---|---|---|
| `AccessListService` | `accesslist/v1/accesslist_service.proto` | CRUD for access lists |
| `AccessMonitoringRulesService` | `accessmonitoringrules/v1/access_monitoring_rules_service.proto` | |
| `AppAuthConfigService`, `AppAuthConfigSessionsService` | `appauthconfig/v1/` | App auth config + sessions |
| `AuditLogService` | `auditlog/v1/auditlog.proto` | StreamUnstructuredSessionEvents, GetUnstructuredEvents, ExportUnstructuredEvents (streaming) |
| `AutoUpdateService` | `autoupdate/v1/autoupdate_service.proto` | Managed updates RFD-184 |
| `BeamService` | `beams/v1/beam_service.proto` | |
| `BotService`, `BotInstanceService`, `SPIFFEFederationService`, `WorkloadIdentityService` | `machineid/v1/` | tbot/MachineID |
| `ClusterConfigService` | `clusterconfig/v1/clusterconfig_service.proto` | NetworkingConfig, AuthPreference, SessionRecording, AccessGraphSettings, ClusterAuditConfig, ClusterName |
| `CrownJewelService` | `crownjewel/v1/crownjewel_service.proto` | |
| `DatabaseObjectService`, `DatabaseObjectImportRuleService` | `dbobject/v1/`, `dbobjectimportrule/v1/` | DB object import rules |
| `DecisionService` | `decision/v1alpha1/decision_service.proto` | Policy Decision Point (PDP), unstable |
| `DelegationSessionService` | `delegation/v1/delegation_session_service.proto` | |
| `DeviceTrustService` | `devicetrust/v1/devicetrust_service.proto` | Trusted device enrollment |
| `DiscoveryConfigService` | `discoveryconfig/v1/discoveryconfig_service.proto` | |
| `DynamicWindowsService` | `dynamicwindows/v1/dynamicwindows_service.proto` | |
| `ExternalAuditStorageService` | `externalauditstorage/v1/externalauditstorage_service.proto` | BYO-bucket audit |
| `GitServerService` | `gitserver/v1/git_server_service.proto` | |
| `HardwareKeyAgentService` | `hardwarekeyagent/v1/hardwarekeyagent_service.proto` | YubiKey/PIV |
| `HealthCheckConfigService` | `healthcheckconfig/v1/health_check_config_service.proto` | |
| `IdentityCenterService` | `identitycenter/v1/service.proto` | AWS IC integration |
| `IntegrationService`, `AWSOIDCService`, `AWSRolesAnywhereService` | `integration/v1/` | Cloud integrations |
| `InventoryService` | `inventory/v1/inventory_service.proto` | Connected agent inventory (counts) — note: legacy `InventoryControlStream` still lives on `AuthService` |
| `IssuanceService` | `issuance/v1/service.proto` | |
| `JoinService` (v1) | `join/v1/joinservice.proto` | Newer join service; coexists with legacy in `legacy/client/proto/joinservice.proto` |
| `KubeService` | `kube/v1/kube_service.proto` | Kube proxy |
| `KubeWaitingContainersService` | `kubewaitingcontainer/v1/kubewaitingcontainer_service.proto` | |
| `LinuxDesktopService` | `linuxdesktop/v1/linux_desktop_service.proto` | |
| `LoginRuleService` | `loginrule/v1/loginrule_service.proto` | |
| `MFAService` (v1 + v2) | `mfa/v1/service.proto`, `mfa/v2/service.proto` | v1 kept for browser-MFA backcompat |
| `NotificationService` | `notifications/v1/notifications_service.proto` | In-product notifications |
| `OktaService` | `okta/v1/okta_service.proto` | |
| `PluginService` | `plugins/v1/plugin_service.proto` | |
| `PresenceService` | `presence/v1/service.proto` | RemoteCluster, ReverseTunnel |
| `RecordingEncryptionService` | `recordingencryption/v1/recording_encryption_service.proto` | RFD: at-rest recording encryption |
| `RecordingMetadataService` | `recordingmetadata/v1/recordingmetadata_service.proto` | |
| `ResourceUsageService` | `resourceusage/v1/resourceusage_service.proto` | Quota/usage |
| `SAMLIdPService` | `samlidp/v1/samlidp.proto` | |
| `SCIMService` | `scim/v1/scim_service.proto` | SCIM provisioning |
| `ScopedAccessService`, `ScopedJoiningService` | `scopes/access/v1/`, `scopes/joining/v1/` | New scoped authorization model |
| `SecReportsService` | `secreports/v1/secreports_service.proto` | |
| `SecretsScannerService` | `access_graph/v1/secrets_service.proto` | Access Graph |
| `ServiceConfigDiscoveryService` | `grpcclientconfig/v1/grpcclientconfigservice.proto` | gRPC client load balancing config |
| `SessionSearchService` | `sessionsearch/v1/session_search.proto` | |
| `SigstorePolicyResourceService`, `WorkloadIdentityResourceService`, `WorkloadIdentityIssuanceService`, `WorkloadIdentityRevocationService`, `X509OverridesService` | `workloadidentity/v1/` | SPIFFE / WI |
| `StableUNIXUsersService` | `stableunixusers/v1/stableunixusers.proto` | Stable UID mapping |
| `StaticHostUsersService` | `userprovisioning/v2/statichostuser_service.proto` | |
| `SubCAService` | `subca/v1/subca_service.proto` | |
| `SummarizerService` | `summarizer/v1/summarizer_service.proto` | Session summarisation |
| `TransportService` | `transport/v1/transport_service.proto` | gRPC SSH transport from proxy |
| `TrustService` | `trust/v1/trust_service.proto` | Cert authority + trusted cluster CRUD |
| `UsersService` | `users/v1/users_service.proto` | |
| `UserLoginStateService` | `userloginstate/v1/userloginstate_service.proto` | |
| `UserPreferencesService` | `userpreferences/v1/userpreferences.proto` | |
| `UserTaskService` | `usertasks/v1/user_tasks_service.proto` | |
| `VnetConfigService` | `vnet/v1/vnet_config_service.proto` | |
| `WorkloadClusterService` | `workloadcluster/v1/workloadcluster_service.proto` | |

### Internal services (root `proto/`)

| Service | Proto | Purpose |
|---|---|---|
| `ProxyService` (`package proto`) | `api/proto/teleport/legacy/client/proto/proxyservice.proto` | Proxy↔Proxy dialing (DialNode, Ping) — wired in `lib/proxy/peer/service.go` |
| `AccessGraphService` | `proto/accessgraph/v1alpha/access_graph_service.proto` | Teleport→Access Graph: `EventsStream`, `EventsStreamV2`, `AuditLogStream`, `AWSCloudTrailStream`, `KubeAuditLogStream`, `Query`, `GetFile`, `Register`, `ReplaceCAs` |
| `SessionRecordingService` | `proto/accessgraph/v1/session_search.proto` | |
| `TeleportReportingService` (`prehog.v1`, `prehog.v1alpha`), `ConnectReportingService`, `TbotReportingService` | `proto/prehog/...` | Anonymous usage telemetry. Uses **connect-rpc** transport (see §9). |
| `TerminalService`, `TshdEventsService`, `AutoUpdateService`, `VnetService`, `PtyHostService` | `proto/teleport/lib/teleterm/...`, `proto/teleport/web/teleterm/...` | Teleport Connect (electron) ↔ tshd |
| `ClientApplicationService` | `proto/teleport/lib/vnet/v1/client_application_service.proto` | VNet daemon API |
| `DiscoveryService` | `proto/teleport/relaytunnel/v1alpha/discovery_service.proto` | Relay tunnel discovery |
| `Pinger` | `proto/teleport/lib/multiplexer/test/ping.proto` | Test only |

Other root proto trees: `proto/teleport/quicpeering/v1alpha/dial.proto`, `proto/teleport/relaypeering/v1alpha/dial.proto` (QUIC-based proxy peering), `proto/teleport/storage/local/stableunixusers/v1/stableunixusers.proto` (internal storage layout).

---

## 3. Auth Service v1 gRPC services (per-resource split)

Teleport is migrating away from the monolithic `proto.AuthService` (`api/proto/teleport/legacy/client/proto/authservice.proto`, ~3070 lines, 274 RPCs) toward focused per-resource v1 services. Each new resource lives in `lib/auth/<resource>/<resource>v1/` as a package containing a single `service.go` that defines a `type Service struct { ... }` implementing the generated `*ServiceServer` interface.

**Standard pattern** (cf. `lib/auth/notifications/notificationsv1/service.go`):

```go
type ServiceConfig struct { Backend Backend; Authorizer authz.Authorizer; ... }
type Service struct {
  notificationsv1.UnimplementedNotificationServiceServer
  authorizer authz.Authorizer; backend Backend; ...
}
func NewService(cfg ServiceConfig) (*Service, error) { ... }
```

The `Backend` is an interface in the package itself, scoped to only the storage methods that service needs, decoupling from `lib/services`. The `Authorizer` is `lib/authz.Authorizer` (an RBAC checker). The service is registered in `lib/auth/grpcserver.go`'s `NewGRPCServer()`.

**The 37 v1/v2 service packages found in `lib/auth/`** (sorted):

```
accessmonitoringrules/accessmonitoringrulesv1
appauthconfig/appauthconfigv1
autoupdate/autoupdatev1
clusterconfig/clusterconfigv1
crownjewel/crownjewelv1
dbobject/dbobjectv1
dbobjectimportrule/dbobjectimportrulev1
delegation/delegationv1
discoveryconfig/discoveryconfigv1
dynamicwindows/dynamicwindowsv1
gitserver/gitserverv1
grpcclientconfig/grpcclientconfigv1
healthcheckconfig/healthcheckconfigv1
integration/integrationv1
inventory/inventoryv1
issuance/v1                    # note: not issuancev1
kubewaitingcontainer/kubewaitingcontainerv1
linuxdesktop/linuxdesktopv1
loginrule/loginrulev1
machineid/machineidv1
machineid/workloadidentityv1
mfa/mfav1                      # legacy browser MFA
mfa/mfav2                      # current
notifications/notificationsv1
presence/presencev1
recordingencryption/recordingencryptionv1
recordingmetadata/recordingmetadatav1
secreports/secreportsv1
summarizer/summarizerv1
trust/trustv1
userloginstate/userloginstatev1
userpreferences/userpreferencesv1
userprovisioning/userprovisioningv2  # static host users
users/usersv1
usertasks/usertasksv1
vnetconfig/vnetconfigv1
workloadcluster/workloadclusterv1
```

Additional non-`*v1` v1 services live in `lib/auth/`:
- `lib/auth/scopes/access/` — Scoped Access Control
- `lib/auth/scopes/joining/` — Scoped Joining
- `lib/auth/stableunixusers/` — stable UID mapping
- `lib/decision/decisionv1/` — Policy Decision Point (outside lib/auth tree)
- `lib/auth/accessmonitoringrules` — (also has v1 nested dir)

OSS stubs (return `NotImplemented`) registered when running OSS-only:
- `loginrulev1.NotImplementedService{}` for `LoginRuleService`
- `secreportsv1.NotImplementedService{}` for `SecReportsService`
- `workloadidentityv1.NewSigstorePolicyResourceService()` similar pattern

---

## 4. gRPC server wiring

**Single entrypoint.** `lib/auth/grpcserver.go:6112` — `NewGRPCServer(cfg GRPCServerConfig) (*GRPCServer, error)`. Called from `lib/auth/middleware.go:237` inside `NewTLSServer` (Auth's TLS server constructor).

**gRPC server options** (lines 6142-6160):
- `grpc.Creds(creds)` — custom `httplib.TLSCreds` (not `credentials.NewTLS`, to avoid `NextProtos` mutation that breaks multiplexing). Wrapped in `NewTransportCredentials` (`lib/auth/transport_credentials.go`) that also performs initial auth.
- `grpc.StatsHandler(otelgrpc.NewServerHandler())` — OpenTelemetry trace propagation.
- `grpc.ChainUnaryInterceptor(cfg.UnaryInterceptors...)` and `grpc.ChainStreamInterceptor(...)`.
- `grpc.KeepaliveParams` and `grpc.KeepaliveEnforcementPolicy` — keepalive tuned from `cfg.KeepAlivePeriod`.
- `grpc.MaxConcurrentStreams(defaults.GRPCMaxConcurrentStreams)`.

**Interceptor chain** (`lib/auth/middleware.go:605-632`):

Unary:
1. (optional) `GRPCMetrics.UnaryServerInterceptor()` — Prometheus
2. `interceptors.GRPCServerUnaryErrorInterceptor` — `trail.ToGRPC` error mapping (`api/utils/grpc/interceptors`)
3. `metadata.UnaryServerInterceptor` — client version metadata extraction (`api/metadata`)
4. `a.rateLimitUnaryInterceptor()` — limiter from `lib/limiter`
5. `a.withAuthenticatedUserUnaryInterceptor` — authenticates user from TLS peer cert via `NewTransportCredentials`

Stream:
1. (optional) Prometheus
2. `interceptors.GRPCServerStreamErrorInterceptor`
3. `metadata.StreamServerInterceptor`
4. `a.Limiter.StreamServerInterceptor`
5. `a.withAuthenticatedUserStreamInterceptor`

**Per-RPC ad-hoc rate limiters** in `GRPCServer`:
- `createAuthenticateChallengeLimiter` (lines 240, 6390) — for `CreateAuthenticateChallenge` from unauthenticated callers
- `createAuditStreamSemaphore` (lines 245) — caps in-flight `CreateAuditStream` RPCs
- `resolveSSHTargetRateLimiter` (lines 251, 6413, 5113) — recently added (commit 6413a5ae2e) for `ResolveSSHTarget`

**All `RegisterXxxServer` calls** in `NewGRPCServer` (~55 services). Highlights:

```
authpb.RegisterAuthServiceServer(server, authServer)           // legacy monolith
authpb.RegisterJoinServiceServer(server, legacyJoinServer)     // legacy join
joinv1.RegisterJoinServiceServer(server, join.NewServer(...))  // new join (v1)
auditlogpb.RegisterAuditLogServiceServer(server, authServer)   // audit log unstructured
collectortracepb.RegisterTraceServiceServer(server, authServer)// OTLP trace ingestion
grpc_health_v1.RegisterHealthServer(server, authServer.healthcheck)
```

Plus 50+ `Register<Resource>ServiceServer(server, <resourceService>)` calls (see grep output in section 3).

**Enterprise plugin hook.** `lib/auth/middleware.go:257` — `cfg.PluginRegistry.RegisterAuthServices(ctx, server.grpcServer, ...)` allows the enterprise build to register extra gRPC services on the same server.

**connect-rpc.** Only used as a *client* against the prehog reporting service (`lib/usagereporter/teleport/usagereporter.go:30` imports `connectrpc.com/connect`, line 194 builds `prehogv1ac.NewTeleportReportingServiceClient`). Auth's own server is plain gRPC — no Connect handlers exposed. There is also `lib/usagereporter/daemon/usagereporter.go` for the tsh daemon.

---

## 5. HTTP REST surface (`lib/web`)

The web API is served from `lib/web/apiserver.go` (5853 LOC). It uses **julienschmidt/httprouter** (imported L54) as the router, embedded in `Handler` (L160-189):

```go
type Handler struct {
    sync.Mutex
    httprouter.Router         // embedded
    cfg                 Config
    auth                *sessionCache
    limiter, highLimiter *limiter.RateLimiter
    ...
}
```

`NewHandler` (L540) constructs it; `bindMinimalEndpoints` (L857) registers what's needed when running as just a reverse tunnel; `bindDefaultEndpoints` (L869) adds the full UI surface. Total of **~220 `h.GET/POST/PUT/DELETE/Handle` calls** in `apiserver.go`.

**Path prefix shim.** `lib/web/apiserver.go:713` — routes are defined without the `/v1` prefix; a `notFoundRoutingHandler` (L714) strips a leading `/v1/` and replays the request, restricted to known second segments (`webapi`, `enterprise`, `scripts`, `.well-known`, `workload-identity`, `web`). v2+ paths must be defined explicitly with their prefix.

**Middleware wrappers** (returned `httprouter.Handle`):

| Wrapper | File:Line | Behavior |
|---|---|---|
| `WithAuth(fn)` | L5242 | Authenticate session cookie + bearer token (full CSRF protection) |
| `WithSession(fn)` | L5258 | Session cookie only, no bearer (no CSRF) |
| `WithClusterAuth(fn)` | L4941 | Adds cluster context (proxied per-cluster auth client) |
| `WithClusterAuthWebSocket(fn, opts...)` | L4991 | Upgrades to WebSocket; auth is done over the WS itself |
| `WithClusterClientProvider(fn)` | L5116 | Lazy client provider |
| `WithProvisionTokenAuth(fn)` | L5136 | Authenticates via provision token (used by joining nodes) |
| `WithRedirect`, `WithMetaRedirect` | L5210, 5226 | OIDC/SAML callback redirects |
| `WithLimiter`, `WithHighLimiter` | L5354, 5363 | IP-based rate limit (low and high) |
| `WithUnauthenticatedLimiter`, `WithUnauthenticatedHighLimiter` | L5271, 5279 | Stricter limiter for unauth |
| `WithAccessDeniedLimiter` | L5327 | Extra limiter for 403-returning endpoints |

`httplib.MakeHandler` (`lib/httplib/httplib.go`) is the base adapter — turns `func(w,r,p) (any, error)` into `httprouter.Handle` and JSON-encodes the response.

### Significant route groups

Sampled at `lib/web/apiserver.go:863-1304`. Selected representative paths only (not exhaustive):

| Prefix | Examples | Purpose |
|---|---|---|
| `/.well-known/*` | `/.well-known/jwks.json`, `/.well-known/openid-configuration` | Public JWKS, OIDC IdP discovery |
| `/web/config.js` | L703 | UI bootstrap config (env-injected) |
| `/webapi/find` | L863 | Cheap address-discovery endpoint (used by agents, not rate-limited per IP) |
| `/webapi/ping`, `/webapi/ping/:connector` | L876-877 | Server identity + auth config |
| `/webapi/motd` | L883 | Message of the day |
| `/webapi/scripts/installer/:name`, `/webapi/scripts/databases/configure/...`, `/webapi/scripts/integrations/configure/...` | many | Pre-generated install/configure shell scripts (return bash, see §9 below) |
| `/scripts/:token`, `/scripts/:token/install-node.sh`, `install-app.sh`, `install-database.sh`, `install-discovery.sh` | L1006-1015 | Token-protected node-join scripts |
| `/webapi/host/credentials` | L865 | Issue host credentials (for joining) |
| `/webapi/traces` | L889 | OTLP HTTP trace ingestion |
| `/webapi/sessions/{app,web,web/renew}` | L892-897 | App & web session lifecycle |
| `/webapi/users`, `/v2/webapi/users`, `/webapi/users/:username`, `/webapi/users/password/*`, `/webapi/users/privilege/token` | L898-915 | User CRUD + password reset |
| `/webapi/headless/login`, `/webapi/headless/:id` | L917, 1219 | Headless auth (tsh login `--headless`) |
| `/webapi/sites` | L920 | List trusted clusters |
| `/webapi/sites/:site/info`, `/namespaces`, `/resources`, `/nodes`, `/instances`, `/alerts`, `/locks`, `/sessions`, `/events/search`, `/databases`, `/databaseservices`, `/kubernetes`, `/desktops`, `/integrations`, `/discoveryconfig`, `/usertask`, `/user-groups`, `/machine-id/bot/...`, `/workload-identity`, `/notifications`, `/gitservers`, `/sessionrecording/...` | L925-1300 | Trusted-cluster scoped operations. Heaviest part of the API. |
| `/webapi/sites/:site/connect/ws`, `/kube/exec/ws`, `/db/exec/ws`, `/desktops/.../connect/ws`, `/desktopplayback/.../ws`, `/sessionrecording/.../playback/ws`, `/sessionrecording/.../metadata/ws` | L950-953, 1113-1115, 1300-1301 | WebSocket endpoints (see §6) |
| `/webapi/auth/export`, `/webapi/sites/:site/auth/export` | L983-984 | CA export for trust setup |
| `/webapi/tokens`, `/v2/webapi/tokens`, `/webapi/token`, `/v2/webapi/token`, `/webapi/tokens/yaml` | L987-1001 | Provision token CRUD |
| `/webapi/github/login/web`, `/github/callback`, `/github/login/console`, `/webapi/github`, `/webapi/github/connector/:name` | L1040-1090 | GitHub SSO |
| `/webapi/mfa/login/{begin,finish,finishsession}`, `/webapi/mfa/devices`, `/webapi/mfa/authenticatechallenge`, `/webapi/mfa/token/:token/...`, `/webapi/mfa/browser/:request_id` | L1046-1057, 1222 | WebAuthn MFA HTTP flow (`lib/auth/webauthn*`) |
| `/webapi/devices/webconfirm` | L1062 | Device-trust web confirmation |
| `/webapi/trustedclusters/validate`, `/webapi/trustedcluster`, `/webapi/trustedcluster/:name` | L1065, 1097-1100 | Trusted cluster join/CRUD |
| `/webapi/roles`, `/v2/webapi/roles`, `/webapi/roles/:name`, `/webapi/requestableroles`, `/webapi/presetroles` | L1072-1082 | Roles |
| `/webapi/authconnector/default`, `/webapi/authconnectors` | L1093-1095 | SSO connector default |
| `/webapi/apps/:fqdnHint(/:cluster/:publicAddr)` | L1102-1103 | App-launcher (resolve app URL) |
| `/webapi/yaml/parse/:kind`, `/webapi/yaml/stringify/:kind` | L1105-1106 | YAML resource validation for UI |
| `/webapi/sites/:site/desktops/...`, `/desktopservices`, `/desktopplayback/.../ws` | L1109-1116 | Windows desktops |
| `/webapi/sites/:site/diagnostics/connections/...` | L1119-1121 | Connection diagnostics |
| `/webapi/sites/:site/integrations`, `/integrations/aws-oidc/:name/...`, `/integrations/aws-ra/:name/...`, `/integrations/:name/discoveryrules`, `/integrations/:name/ca`, `/plugins/:plugin/files/...` | L1124-1180 | Integration management |
| `/webapi/spiffe/bundle.json`, `/workload-identity/jwt-jwks.json`, `/workload-identity/.well-known/openid-configuration` | L1194-1196 | SPIFFE/WI federation |
| `/webapi/connectionupgrade` | L1212 | ALPN connection upgrade endpoint (for HTTP/1.1-only ELBs); see §6 |
| `/webapi/precapture`, `/webapi/capture` | L1215, 1217 | User-event capture (prehog) |
| `/webapi/user/preferences(/:site)` | L1227-1236 | User prefs (cluster-scoped + global) |
| `/webapi/connectmycomputer/logins` | L1240 | Connect-my-computer UI |
| `/webapi/automaticupgrades/channel/*request`, `/webapi/managedupdates` | L1244-1250 | Managed updates |
| `/webapi/sites/:site/machine-id/bot/...`, `/machine-id/token`, `/machine-id/bot-instance/...`, `/machine-id/wizards/ci-cd` | L1253-1304 | MachineID/tbot |
| `/web/*` (UI) | L692, dynamic | Static assets via `cfg.StaticFS`. `/` redirects to `/web`. |
| `/robots.txt` | L692 | |

There is **no** separate proto-derived REST API; the web HTTP API is hand-rolled.

### `lib/web/` sub-packages

- `lib/web/app/` — App-access HTTP handler, ALPN tunnels (`handler.go`, `https_tunnel.go`, `transport.go`, `session.go`)
- `lib/web/desktop/` — Desktop playback (`playback.go`)
- `lib/web/terminal/` — WebSocket terminal stream (`terminal.go` + `envelope.pb.go`)
- `lib/web/scripts/` — Node-join script generation (`install.go`, `install_node.go`, `database/sqlserver/...`, `node-join/install.sh`, `install/install.sh`, `oneoff/oneoff.{go,sh}`)
- `lib/web/scripts/oneoff/` — generates "curl ... | bash" one-shot install commands
- `lib/web/session/` — session cookie/cache
- `lib/web/ui/` — UI-shaped JSON types (transformations from internal types into UI-friendly shapes)
- `lib/web/mfajson/` — MFA challenge JSON encoding for the browser
- `lib/web/templates/` — HTML templates (probably login/error pages)

`lib/web/handlers.go` does not exist as a top-level file; route registration lives in `apiserver.go`.

### `lib/httplib/`

Shared HTTP helpers:
- `httplib.go` — `MakeHandler`, `MakeTracingHandler`, `MakeSecurityHeaderHandler`, `RouteNotFoundResponse`, response writers
- `csrf/` — CSRF token helpers
- `grpccreds.go` — TLS credentials variant safe to use with multiplexed listeners
- `httpheaders.go` — strict security headers
- `reverseproxy/` — reverse proxy for app access

---

## 6. WebSockets

Endpoints (all upgraded under `WithClusterAuthWebSocket`):

| Path | Handler | Purpose |
|---|---|---|
| `/webapi/sites/:site/connect/ws` | `h.siteNodeConnect` | Browser SSH terminal |
| `/webapi/sites/:site/kube/exec/ws` | `h.podConnect` | Browser `kubectl exec` |
| `/webapi/sites/:site/db/exec/ws` | `h.dbConnect` | Browser DB REPL |
| `/webapi/sites/:site/desktops/:desktopName/connect/ws` | `h.desktopConnectHandle` (subprotocol `tdpb`) | Windows desktop (TDP protocol) |
| `/webapi/sites/:site/desktopplayback/:sid/ws` | `h.desktopPlaybackHandle` | Desktop session playback |
| `/webapi/sites/:site/sessionrecording/:session_id/playback/ws` | `h.recordingPlaybackWS` | New playback WS |
| `/webapi/sites/:site/sessionrecording/:session_id/metadata/ws` | `h.getSessionRecordingMetadata` | Recording metadata stream |

**Library choices.** `lib/web/terminal/terminal.go:32` imports `github.com/gorilla/websocket` (the main library used). The protocol over the socket is a custom `Envelope` proto (`lib/web/terminal/envelope.pb.go`, defined in `proto/teleport/lib/web/terminal/envelope.proto`). The `WSConn` interface (L46) abstracts gorilla's `*websocket.Conn` so we can wrap it. `WSStream` (L72) multiplexes text/binary frames with named handlers.

**`gobwas/ws`.** Used in `lib/web/conn_upgrade.go:32` only for the ALPN connection-upgrade endpoint `/webapi/connectionupgrade`. The comment (L71-76) explains: when wrapping a raw `net.Conn`, gobwas's bytewise reader doesn't cache read errors like gorilla's `websocket.Conn` does, so it's used server-side here because the client side (`api/client/alpn_websocket.go`) also uses gobwas.

Auth over WebSockets uses an in-band JSON token mechanism — `WithClusterAuthWebSocket` upgrades first and authenticates from the first message (mirrors the gRPC TransportCredentials pattern).

---

## 7. Cross-resource conventions

**Pagination.** All modern v1 services use Google-style pagination (enforced by the custom `PAGINATION_REQUIRED` lint, see §9):

```protobuf
message ListXRequest {
  int32 page_size = 1;
  string page_token = 2;   // empty for first page
  XFilters filters = 3;    // optional, service-specific
}
message ListXResponse {
  repeated X items = 1;
  string next_page_token = 2;  // empty when no more
}
```

Confirmed in `notifications/v1`, `dbobject/v1`, `clusterconfig/v1`, etc. Sample: `api/proto/teleport/notifications/v1/notifications_service.proto:69-94`.

**Legacy pagination** (`api/proto/teleport/legacy/client/proto/authservice.proto:2391-2431` `ListResourcesRequest`):
- `Limit` (int32) instead of `page_size`
- `StartKey` (string) instead of `page_token`
- `NextKey` (string) in response instead of `next_page_token`
- Plus extra knobs unique to unified resources: `Namespace`, `Labels` (map), `PredicateExpression`, `SearchKeywords[]`, `SortBy{field, isDesc}`, `NeedTotalCount`, `WindowsDesktopFilter`, `UseSearchAsRoles`, `UsePreviewAsRoles`, `IncludeLogins`

The legacy `ListResources` RPC is the universal multi-resource lister used by the unified resource view.

**Sorting.** `types.SortBy` (in `legacy/types/types.proto`) is `{field string; isDesc bool}`. Modern v1 services rarely expose sorting; the assumption is that the cache returns deterministic order.

**Filters.** Modern services embed a service-specific `XFilters` message inside `ListXRequest` (e.g. `NotificationFilters` with `username`, `global_only`, `labels map<string,string>`).

**Enforcement.** Pagination rules are enforced at lint time by `build.assets/tooling/cmd/buf-plugin-linters/main.go:30` — rule ID `PAGINATION_REQUIRED`, configured in `default.go`. Methods returning `repeated` must use `page_token`/`page_size`/`next_page_token`. Method names starting with `List` are required to return a repeated field. Streaming methods are exempt. See `default.go` for the skip list.

**Error mapping.** gRPC errors go through `interceptors.GRPCServerUnaryErrorInterceptor` / `StreamErrorInterceptor` (`api/utils/grpc/interceptors`) which uses `trail.ToGRPC` / `trail.FromGRPC` (`api/trail`) to round-trip `trace.Error` types — preserving `IsNotFound`, `IsAccessDenied`, `IsBadParameter` etc. across the wire as gRPC status codes plus a serialized trail header.

---

## 8. Webhook / event-stream APIs

Not classic HTTP webhooks — Teleport's event surfaces are gRPC streaming RPCs.

**Audit log streams** (from agents/clients to Auth):
- `proto.AuthService.CreateAuditStream(stream AuditStreamRequest) returns (stream events.StreamStatus)` — `api/proto/teleport/legacy/client/proto/authservice.proto:3260`. Bidirectional stream used by SSH/Kube/DB servers to push session events into Auth. Rate-limited via `createAuditStreamSemaphore` (`lib/auth/grpcserver.go:245`). The `events.StreamStatus` ack messages tell the producer how many events were durably persisted.
- `proto.AuthService.EmitAuditEvent` (`lib/auth/grpcserver.go:287`) — unary fire-and-forget audit event emission. Schema: `apievents.OneOf` in `api/proto/teleport/legacy/types/events/events.proto`.
- `proto.AuthService.GetEvents(GetEventsRequest) returns (Events)` (L3648) — paginated audit log retrieval.
- `proto.AuthService.StreamSessionEvents(StreamSessionEventsRequest) returns (stream events.OneOf)` (L3668) — replay events from a recorded session.

**Audit log v1 (unstructured)** — newer parallel API on `AuditLogService` (`api/proto/teleport/auditlog/v1/auditlog.proto:25`):
- `StreamUnstructuredSessionEvents(req) returns (stream EventUnstructured)` — session replay with schemaless events (for the UI / Athena)
- `GetUnstructuredEvents(req) returns (EventsUnstructured)` — paginated unstructured retrieval
- `ExportUnstructuredEvents(req) returns (stream ExportEventUnstructured)` — bulk export
- `GetEventExportChunks(req) returns (stream EventExportChunk)` — chunked export support

**Cluster event watching**:
- `proto.AuthService.WatchEvents(Watch) returns (stream Event)` — `authservice.proto:3130`. Server pushes resource change events to subscribed clients. Used by every cached client and by `lib/cache`.
- `proto.AuthService.WatchPendingHeadlessAuthentications(google.protobuf.Empty) returns (stream Event)` (L3936) — narrowly-scoped headless variant.

**Notifications**: `NotificationService.ListNotifications` is poll-based (no stream); push is via `WatchEvents` on the `kind=Notification` resource.

**Access Graph stream** (Teleport → Access Graph): `accessgraph.v1alpha.AccessGraphService` (`proto/accessgraph/v1alpha/access_graph_service.proto`):
- `EventsStream(stream EventsStreamRequest) returns (EventsStreamResponse)` — legacy upstream
- `EventsStreamV2(stream EventsStreamV2Request) returns (stream EventsStreamV2Response)` — bidi current
- `AuditLogStream(stream AuditLogStreamRequest) returns (stream AuditLogStreamResponse)` — Access Graph receives a copy of the audit log
- `AWSCloudTrailStream`, `KubeAuditLogStream` — vendor-specific event sources
- `Register`, `ReplaceCAs` — bootstrap

**Inventory control stream**: `proto.AuthService.InventoryControlStream(stream UpstreamInventoryOneOf) returns (stream DownstreamInventoryOneOf)` (`authservice.proto:3073`) — the heartbeat protocol every agent uses. Counterpart server logic in `lib/inventory`.

**Trace ingestion**: `opentelemetry.proto.collector.trace.v1.TraceService.Export` is registered on the Auth gRPC server (`lib/auth/grpcserver.go:264 Export()`) — tsh/tctl forward client-side spans through Auth.

---

## 9. buf tooling

**Configuration files at repo root**:
- `buf.yaml` — workspace definition (modules `api/proto` and `proto`), lint config, breaking-change config, custom plugin invocation
- `buf-legacy.yaml` — separate lint config applied only to `api/proto` legacy section
- `buf-go.gen.yaml` — `protoc-gen-go` (Google protobuf-go) for modern protos. Output: `github.com/gravitational/teleport` module-relative.
- `buf-gogo.gen.yaml` — `protoc-gen-gogofast` + `protoc-gen-go-grpc` for legacy gogo-protobuf protos. Inputs limited to `legacy/`, `attestation/`, `componentfeatures/`, `mfa/v1/`, `usageevents/`, `web/terminal/envelope.proto`. Output to `./gogogen` then copied (see `genproto.sh`).
- `buf-connect-go.gen.yaml` — `protoc-gen-go` + `protoc-gen-connect-go`, only for `proto/prehog/`. Output `gen/proto/go/prehog/...`.
- `buf-ts.gen.yaml` — `@protobuf-ts/plugin` for TypeScript. Inputs: `api/proto/teleport/userpreferences/`, `api/proto/teleport/desktop/`, `proto/prehog/`, `proto/teleport/lib/teleterm/`, `proto/teleport/lib/vnet/diag/`, `proto/teleport/web/`. Output `gen/proto/ts`.

**Makefile entrypoints** (`Makefile:1605-1671`):
- `make protos/build`, `protos/lint`, `protos/format`, `protos/breaking` — buf validation
- `make grpc` — runs codegen *inside* the buildbox container (`$(MAKE) -C build.assets grpc`)
- `make grpc/host` — runs codegen on host: `./build.assets/genproto.sh`
- `make protos-up-to-date(/host)` — CI guard

**`build.assets/genproto.sh`** (50 lines):
1. `rm -fr api/gen/proto gen/proto`
2. `buf generate --template=buf-gogo.gen.yaml` → produces into `./gogogen/github.com/gravitational/teleport/`, then `cp -r gogogen/github.com/gravitational/teleport/. .`
3. `buf generate --template=buf-go.gen.yaml`
4. `buf generate --template=buf-connect-go.gen.yaml`
5. `buf generate --template=buf-ts.gen.yaml` (unless `--skip-js`)

**Custom lint plugin**: `build.assets/tooling/cmd/buf-plugin-linters/`:
- `main.go` — registers rule `PAGINATION_REQUIRED` (line 30). Walks every RPC method; flags non-streaming RPCs returning a `repeated` field that lack `page_token`/`page_size`. Also enforces that `List*` method names return a repeated field.
- `default.go` — default config: prefix `methodPrefixMustHaveRepeated` (probably `"List"`), skip list, etc.
- `config.go` — configurable per-method skip
- `README.md` references RFD-0153 (resource guidelines).

Invoked from `buf.yaml:100-108` via `env GOWORK=off go -C ./build.assets/tooling run ./cmd/buf-plugin-linters`.

**`proto/teleport/web/README.md`** is a one-liner: "Protos used only by JavaScript servers and clients." Only TypeScript code is generated from this subtree.

**API levels** (`buf-go.gen.yaml:33`): default `API_OPAQUE` for new protos; all existing protos individually pinned to `API_OPEN` via `apilevelM...=API_OPEN` per-file options to allow gradual migration to the opaque API surface.

**Operator CRDs.** `make crds-up-to-date` (`Makefile:1686`) runs `integrations/operator/Makefile crd-manifests` to regenerate Kubernetes CRDs from the proto definitions.

---

## 10. AI-pointer index

| Concept | File |
|---|---|
| Public SDK client | `/home/daniel/repos/teleport/api/client/client.go` |
| Public SDK credentials | `/home/daniel/repos/teleport/api/client/credentials.go` |
| Public SDK module | `/home/daniel/repos/teleport/api/go.mod` |
| Legacy monolithic AuthService proto | `/home/daniel/repos/teleport/api/proto/teleport/legacy/client/proto/authservice.proto` |
| ProxyService (peer↔peer) proto | `/home/daniel/repos/teleport/api/proto/teleport/legacy/client/proto/proxyservice.proto` |
| Legacy JoinService proto | `/home/daniel/repos/teleport/api/proto/teleport/legacy/client/proto/joinservice.proto` |
| New v1 JoinService proto | `/home/daniel/repos/teleport/api/proto/teleport/join/v1/joinservice.proto` |
| Audit event types | `/home/daniel/repos/teleport/api/proto/teleport/legacy/types/events/events.proto` |
| Audit log stream RPC (legacy) | `proto.AuthService.CreateAuditStream` → `api/proto/teleport/legacy/client/proto/authservice.proto:3260` |
| Audit log v1 (unstructured) | `/home/daniel/repos/teleport/api/proto/teleport/auditlog/v1/auditlog.proto` |
| Audit event consumer | `/home/daniel/repos/teleport/lib/events/` |
| Cluster event watch | `proto.AuthService.WatchEvents` → `authservice.proto:3130`; impl `/home/daniel/repos/teleport/lib/auth/grpcserver.go` |
| Inventory control stream | `proto.AuthService.InventoryControlStream` → `authservice.proto:3073`; impl `/home/daniel/repos/teleport/lib/inventory/` |
| gRPC server constructor | `/home/daniel/repos/teleport/lib/auth/grpcserver.go:6112 NewGRPCServer` |
| gRPC interceptor chain | `/home/daniel/repos/teleport/lib/auth/middleware.go:605-632` |
| TLS server (wires gRPC server) | `/home/daniel/repos/teleport/lib/auth/middleware.go:237` |
| Per-resource v1 services | `/home/daniel/repos/teleport/lib/auth/<resource>/<resource>v1/service.go` |
| HTTP API routes | `/home/daniel/repos/teleport/lib/web/apiserver.go` (`bindMinimalEndpoints` L857, `bindDefaultEndpoints` L869) |
| HTTP middleware | `/home/daniel/repos/teleport/lib/web/apiserver.go:4941-5378` (`With*` wrappers) |
| HTTP `MakeHandler` adapter | `/home/daniel/repos/teleport/lib/httplib/httplib.go` |
| HTTP security headers | `/home/daniel/repos/teleport/lib/httplib/httpheaders.go` |
| CSRF | `/home/daniel/repos/teleport/lib/httplib/csrf/` |
| WebSocket envelope proto | `/home/daniel/repos/teleport/proto/teleport/lib/web/terminal/envelope.proto` |
| Browser SSH terminal | `/home/daniel/repos/teleport/lib/web/terminal/terminal.go` |
| Desktop playback handler | `/home/daniel/repos/teleport/lib/web/desktop/playback.go` |
| ALPN conn upgrade (HTTP→TLS) | `/home/daniel/repos/teleport/lib/web/conn_upgrade.go` (uses `gobwas/ws`) |
| App access HTTP handler | `/home/daniel/repos/teleport/lib/web/app/handler.go` |
| Node-join script generation | `/home/daniel/repos/teleport/lib/web/scripts/install_node.go`, `node-join/install.sh` |
| One-shot install script | `/home/daniel/repos/teleport/lib/web/scripts/oneoff/oneoff.go` |
| WebAuthn HTTP flow | `/home/daniel/repos/teleport/lib/auth/webauthn/` + `lib/web/apiserver.go` `/webapi/mfa/*` routes |
| WebAuthn protocol types | `/home/daniel/repos/teleport/lib/auth/webauthntypes/` |
| Proxy peer service (peer↔peer DialNode) | `/home/daniel/repos/teleport/lib/proxy/peer/service.go` |
| Proxy QUIC peering | `/home/daniel/repos/teleport/lib/proxy/peer/quic/` |
| Trusted cluster API | `/home/daniel/repos/teleport/lib/auth/trust/trustv1/service.go` + `api/proto/teleport/trust/v1/trust_service.proto` |
| Pagination convention | `api/proto/teleport/notifications/v1/notifications_service.proto:69-94` (canonical example) |
| Pagination lint enforcement | `/home/daniel/repos/teleport/build.assets/tooling/cmd/buf-plugin-linters/main.go` (rule `PAGINATION_REQUIRED`) |
| buf workspace | `/home/daniel/repos/teleport/buf.yaml` |
| Proto code generation script | `/home/daniel/repos/teleport/build.assets/genproto.sh` |
| Modern Go codegen config | `/home/daniel/repos/teleport/buf-go.gen.yaml` |
| Legacy gogo codegen config | `/home/daniel/repos/teleport/buf-gogo.gen.yaml` |
| connect-rpc codegen (prehog only) | `/home/daniel/repos/teleport/buf-connect-go.gen.yaml` |
| TypeScript codegen | `/home/daniel/repos/teleport/buf-ts.gen.yaml` |
| Make targets | `/home/daniel/repos/teleport/Makefile:1605-1671` |
| Prehog (usage telemetry) client | `/home/daniel/repos/teleport/lib/usagereporter/teleport/usagereporter.go` |
| Access Graph stream proto | `/home/daniel/repos/teleport/proto/accessgraph/v1alpha/access_graph_service.proto` |
| Teleport Connect (electron) RPC | `/home/daniel/repos/teleport/proto/teleport/lib/teleterm/v1/service.proto` |
| VNet client RPC | `/home/daniel/repos/teleport/proto/teleport/lib/vnet/v1/client_application_service.proto` |
| Decision Service (PDP) proto | `/home/daniel/repos/teleport/api/proto/teleport/decision/v1alpha1/decision_service.proto` |
| Scoped Access Service proto | `/home/daniel/repos/teleport/api/proto/teleport/scopes/access/v1/service.proto` |
| Scoped Joining Service proto | `/home/daniel/repos/teleport/api/proto/teleport/scopes/joining/v1/service.proto` |
| Limiter package | `/home/daniel/repos/teleport/lib/limiter/` |
| Cluster authorizer | `/home/daniel/repos/teleport/lib/authz/` |
| Transport (gRPC SSH) proto | `/home/daniel/repos/teleport/api/proto/teleport/transport/v1/transport_service.proto` |
