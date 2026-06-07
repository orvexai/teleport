# Teleport Backend Protocol Subsystems

A factual map of how Teleport's Go backend proxies SSH, Kubernetes, databases,
HTTP apps, desktops (RDP), Git, and MCP through one product. All paths are
relative to the repo root unless otherwise noted.

---

## 1. The "Service" abstraction

### 1.1 Supervisor and service registration (`lib/service`)

The process supervisor is the outer wiring layer. Every protocol subsystem is
registered against it as a critical function and is started by the same
state-machine.

- `lib/service/supervisor.go:42` defines the `Supervisor` interface. The
  implementation is `LocalSupervisor` (line 154).
- `lib/service/supervisor.go:638` defines `Service`, the minimal contract any
  background runner must satisfy: `Serve()`, `String()`, `Name()`,
  `IsCritical()`.
- `LocalSupervisor.RegisterCriticalFunc(name, fn)` (`supervisor.go:272`) wraps a
  function into a `LocalService` with `Critical = true`; failure of any
  critical service tears the whole process down.
- The supervisor also exposes a pub/sub event bus
  (`BroadcastEvent`/`WaitForEvent`) used to gate one subsystem on another's
  identity readiness; the per-role events are `AuthIdentityEvent`,
  `SSHIdentityEvent`, `ProxyIdentityEvent`, etc., emitted in
  `lib/service/connect.go`.
- The top-level wiring is in `lib/service/service.go`. `TeleportProcess`
  (line 631) is the type that owns every connector, listener, and
  subsystem. The per-role boot methods are:
  - `initAuthService` (`service.go:2208`)
  - `initSSH` (`service.go:3481`)
  - `initProxy`, `initProxyEndpoint` (`service.go:4533`, `service.go:4943`)
  - `initApps` (`service.go:6713`)
  - `initDatabases`/`initDatabaseService` (`lib/service/db.go:47,53`)
  - `initKubernetes`/`initKubernetesService` (`lib/service/kubernetes.go:47,75`)
  - `initWindowsDesktopService` (`lib/service/desktop.go:54,267`)
  - `initDiscoveryService` (`lib/service/discovery.go:40,46`)

The pattern in each of these is: build a config struct, instantiate the
typed `*Server` from `lib/srv/<protocol>/`, then register a critical func that
calls `Serve(listener)` or `Start()`. Heartbeats are wired separately through
`lib/srv/heartbeatv2.go` (factory constructors at lines 83, 119, 139, 159, 179
for SSH, app, database, Kubernetes and relay servers respectively).

### 1.2 The `Server` interface for SSH-shaped subsystems (`lib/srv`)

`lib/srv/ctx.go:128` declares the `Server` interface that both `lib/srv/regular`
(SSH node) and `lib/srv/forward` (forwarding node) implement. The interface
funnels everything an SSH-style handler needs through one object:

- `events.StreamEmitter` for audit and recording
- `ID()`, `HostUUID()`, `GetNamespace()`, `AdvertiseAddr()`, `Component()`
- `GetAccessPoint()` returning the cached `AccessPoint` (`ctx.go:95`)
- `GetClock()`, `GetDataDir()`, `GetPAM()`, `GetBPF()`, `GetLockWatcher()`
- `GetHostUsers()`, `GetHostSudoers()` for `useradd`-style provisioning
- `EventMetadata()` returning `apievents.ServerMetadata`
- `Context() context.Context` so child goroutines can chain off shutdown

`AccessPoint` (`ctx.go:95`) is the read-mostly cached client every subsystem
sees instead of the raw auth gRPC. It composes `authclient.Announcer`,
`types.Semaphores`, `services.ScopedRoleReader`, and a handful of cluster-config
accessors.

`ServerContext` (`ctx.go:317`) is the per-connection state object that hangs
off a `Server`. It holds the executable, terminal, the `IdentityContext`
(`ctx.go:219`) and the per-SSH-session `MonitorConfig`.
`IdentityContext` is the post-cert "what is this user allowed to do" struct;
it contains `AccessPermit` (the SSH decision permit), `ProxyingPermit`,
`GitForwardingPermit`, the original `*sshca.Identity`, `MappedRoles`, and
device-trust hooks.

Two non-SSH services have their own parallel "Server" types because they don't
speak SSH on the wire:

- `lib/srv/db/server.go:375` (`db.Server`)
- `lib/srv/app/server.go:140` (`app.Server`)
- `lib/srv/desktop/windows_server.go:125` (`desktop.WindowsService`)
- `lib/kube/proxy/server.go:209` (`kube.TLSServer`)
- `lib/srv/discovery/discovery.go:389` (`discovery.Server`)
- `lib/srv/mcp/server.go:109` (`mcp.Server`)
- `lib/srv/git/forward.go:177` (`git.ForwardServer`)

These don't implement `srv.Server`; instead they expose their own
`HandleConnection(net.Conn)` (or `Serve(net.Listener)`) entry points that the
proxy or supervisor calls into. All of them share the same helpers from
`lib/srv` for session control (`session_control.go`), session tracking
(`sessiontracker.go`), and the connection monitor (`monitor.go`).

---

## 2. Per-protocol service

### 2.1 SSH node (`lib/srv/regular/sshserver.go`)

- **Entry point:** `regular.New` (`sshserver.go:844`) constructs a
  `*regular.Server`, which embeds `sshutils.Server` (`sshserver.go:95`) for the
  raw SSH transport.
- **Main type:** `regular.Server` (`sshserver.go:86`) - implements the
  `srv.Server` interface from `lib/srv/ctx.go`.
- **Listening:** `Start()` (`sshserver.go:434`) opens a TCP listener via the
  embedded `sshutils.Server`. `Serve(l net.Listener)` (`sshserver.go:452`) is
  the variant used when the listener is provided externally (proxy mux,
  reverse tunnel agent). If `useTunnel` is true, no socket is opened; the
  reverse tunnel agent pushes accepted connections through
  `HandleConnection` (`sshserver.go:489`).
- **Authentication (post-proxy):** `HandleNewConn` (`sshserver.go:1411`)
  hands the verified `*ssh.ServerConn` to `AuthHandlers.CreateIdentityContext`
  (`lib/srv/authhandlers.go:182`), which parses the cert into an
  `IdentityContext`. The publickey callback that produces those certs is
  `AuthHandlers.PublicKeyCallback` (`authhandlers.go:374`), with the actual
  RBAC decision in `ahLoginChecker.evaluateSSHAccess` (`authhandlers.go:1247`)
  or the scoped variant `evaluateScopedSSHAccess` (`authhandlers.go:1114`).
  Both produce a `decisionpb.SSHAccessPermit` that flows into the
  `IdentityContext`. Session control runs immediately after via
  `SessionController.AcquireSessionContext` (`sshserver.go:1418`).
- **Channel dispatch:** `HandleNewChan` (`sshserver.go:1481`) splits
  `direct-tcpip` (port-forwarding) and `session` channels. Sessions go to
  `handleSessionRequests` (`sshserver.go:1713`), which builds a
  `ServerContext`, starts the per-session keep-alive loop, and routes each
  SSH request through `dispatch` (`sshserver.go:1836`).
- **Session recording:** A `*srv.session` (from `lib/srv/sess.go:725`) is
  created lazily on `pty-req`/`exec` via `SessionRegistry.OpenSession`
  (`sess.go:357`) or `OpenExecSession` (`sess.go:413`). `newRecorder`
  (`sess.go:1573`) constructs an `events.SessionPreparerRecorder` for the
  session unless cluster recording mode is "proxy"; in the proxy-record case
  the node returns a `DiscardRecorder` because the forwarding proxy is
  recording.
- **Engine plug-points:** The local exec path is `localExec` (`exec.go:121`);
  the forwarding-node path is `remoteExec` (`exec.go:414`). Terminal handling
  is `terminal`/`remoteTerminal` (`term.go:145, 557`). Selection happens in
  `NewExecRequest` (`exec.go:92`) based on `ctx.srv.Component()`.

### 2.2 SSH forwarding node (`lib/srv/forward`)

- **Entry point:** `forward.New(ServerConfig)` (`sshserver.go:329`).
- **Purpose:** an *in-memory* SSH server that the proxy spins up per-
  connection to record sessions for OpenSSH-only targets ("recording proxy").
  See the doc comment at `sshserver.go:67`.
- **Main type:** `forward.Server` (`sshserver.go:87`). It wires a
  `utils.DualPipeNetConn` between the client and a fake server, runs an
  `*ssh.ServerConn` over the server half, and uses a `tracessh.Client` to
  speak to the actual upstream over `TargetConn`.
- **Listening:** none. There is no listener. `Dial()` returns the client end
  of the in-memory pipe; the proxy hands it to the SSH client. `Serve()` runs
  the in-memory server.
- **Authentication:** identical `srv.AuthHandlers` instance is built at
  `sshserver.go:385`; certs are validated against the *target* cluster's
  access point, not the proxy's local one.
- **Session recording:** the proxy's `*srv.SessionRegistry` records every
  channel here. Component is `teleport.ComponentForwardingNode`, which makes
  `NewExecRequest` return `remoteExec` so wire bytes are tee'd through the
  recorder while being forwarded.

### 2.3 Database (`lib/srv/db`)

- **Entry point:** `db.New(ctx, Config)` (`server.go:467`). Engines are
  registered at package init via `common.RegisterEngine` (`server.go:84-96`);
  the registry lives in `lib/srv/db/common/engines.go`.
- **Main type:** `db.Server` (`server.go:375`). It holds the proxy reverse-
  tunnel connection (registered through `srv/server` heartbeats), a
  `monitoredDatabases` reconciler (line 416), and `getEngineFn` callback.
- **Listening:** the database service does *not* listen on a TCP port.
  `Start` (`server.go:915`) opens the reverse tunnel to the proxy and
  registers a heartbeat. `HandleConnection(conn net.Conn)` (`server.go:1161`)
  is invoked by the reverse tunnel for each incoming proxy-side connection.
- **Authentication (post-proxy):** `HandleConnection` upgrades the conn to
  TLS with the service cert (`server.go:1169`), then `middleware.WrapContextWithUser`
  extracts the client cert identity. `authorize` (`server.go:1347`) confirms
  the identity, resolves the requested `RouteToDatabase.ServiceName` to a
  concrete `types.Database`, and computes auto-user mode.
- **Session lifecycle:** `handleConnection` (`server.go:1197`) creates a
  session tracker (`s.trackSession`), opens a recorder via `newSessionRecorder`,
  applies the connection monitor (`monitor.go`'s `ConnectionMonitor.MonitorConn`),
  then `dispatch` (`server.go:1299`) calls `createEngine` and invokes
  `engine.InitializeConnection(clientConn, sessionCtx)` followed by
  `engine.HandleConnection(ctx, sessionCtx)`.
- **Engine plug-points:** `common.Engine` interface (`db/common/interfaces.go:73`)
  exposes `InitializeConnection`, `SendError`, `HandleConnection`. Each
  engine lives in its own directory (`postgres/`, `mysql/`, `mongodb/`,
  `redis/`, `sqlserver/`, etc.) and implements the interface. See
  Section 7 for an inventory.

### 2.4 Application (`lib/srv/app`)

- **Entry point:** `app.New(ctx, *Config)` (`app/server.go:197`). The actual
  connection plumbing lives in
  `lib/srv/app/connections_handler.go:NewConnectionsHandler` (line 218).
- **Main type:** `app.Server` (`app/server.go:140`) owns app reconciliation
  (`monitoredApps`, line 175) and heartbeats. The runtime work is delegated to
  `app.ConnectionsHandler` (`connections_handler.go:181`).
- **Listening:** like the database service, the app service speaks back over a
  reverse tunnel to the proxy. The proxy calls
  `ConnectionsHandler.HandleConnection(conn)` (`connections_handler.go:344`),
  which wraps the conn in a single-connection listener fed into an internal
  HTTP server (`newHTTPServer`, line 746) or TCP server
  (`handleTCPApp`, line 736).
- **Authentication (post-proxy):** `getConnectionInfo` (line 850) terminates
  TLS, then `authorizeContext` (line 538) extracts the identity, locates the
  `types.Application` by public address, and runs RBAC.
- **Session recording:** `newSessionChunk` (`app/session.go:97`) creates a
  per-5-minute "chunk" with a session tracker (`createTracker`, line 317)
  and recorder (`newSessionRecorder`, line 285). `expireSessions`
  (`connections_handler.go:328`) evicts chunks.
- **Engine plug-points:** the per-request "handler" is set by a `sessionOpt`.
  Built-in opts:
  - `withJWTTokenForwarder` (`app/session.go:151`) - HTTP apps with the
    Teleport JWT injected
  - `withAWSSigner` (line 190) - AWS sigv4-signed forwarder
    (`lib/srv/app/aws/handler.go`)
  - `withAzureHandler` (line 195) - `lib/srv/app/azure/handler.go`
  - `withGCPHandler` (line 200) - `lib/srv/app/gcp/handler.go`
  The AWS web console federation path is `serveAWSWebConsole`
  (`connections_handler.go:510`) which calls `cloud.GetAWSSigninURL`
  (`app/cloud.go:140`) and HTTP-redirects.

### 2.5 Desktop / RDP (`lib/srv/desktop`)

- **Entry point:** `desktop.NewWindowsService(WindowsServiceConfig)`
  (`windows_server.go:344`).
- **Main type:** `WindowsService` (`windows_server.go:125`). Owns LDAP
  client, AD CA management (`certificateStoreClient`), TDP cache, and
  discovery reconciler.
- **Listening:** `Serve(plainLis net.Listener)` (`windows_server.go:608`)
  wraps the listener with the service TLS config and accepts proxy
  connections. Each accepted `*tls.Conn` is dispatched to
  `handleConnection` (`windows_server.go:651`).
- **Authentication (post-proxy):** `Handshake()` completes mTLS,
  `middleware.WrapContextWithUser` extracts identity, `Authorizer.Authorize`
  runs RBAC. The target desktop name is carried in the TLS SNI suffix
  `.desktop.teleport.cluster.local` (`desktop.go:30`) and stripped at
  `windows_server.go:710`.
- **Session recording:** `connectRDP` (line 754) creates `sessionID`, looks
  up `SessionRecordingConfig`, and installs `makeTDPSendAuditor`
  (line 1083) and `makeTDPReceiveAuditor` (line 1127) interceptors on the
  TDP message stream. Each TDP frame (`ServerHello`, `FastPathPDU`,
  `PNGFrame`, `Alert`, `ClientScreenSpec`, mouse/keyboard) is fed into
  `recordEvent` (line 1054) which forwards to the
  `events.SessionPreparerRecorder` returned by `newSessionRecorder`
  (line 464).
- **Engine plug-point:** the bottom of the stack is the IronRDP Rust
  library, called through CGo from
  `lib/srv/desktop/rdp/rdpclient/client.go` (build tag
  `desktop_access_rdp`). See Section 9.

### 2.6 Kubernetes (`lib/kube/proxy`)

- **Entry point:** `kube.NewTLSServer(TLSServerConfig)`
  (`lib/kube/proxy/server.go:232`).
- **Main type:** `kube.TLSServer` (`server.go:209`) wraps an `http.Server`
  whose handler is a `*kube.Forwarder` (`forwarder.go:376`) wrapped by an
  `authz.Middleware` and a per-IP `limiter`.
- **Listening:** `Serve(listener, ...)` (`server.go:334`) starts an internal
  `multiplexer.Mux` (because Kubernetes traffic may arrive with a PROXY
  header from the Teleport proxy), then runs the HTTP server over TLS.
- **Authentication (post-proxy):** mTLS terminates inside the HTTP server's
  `TLSConfig`; `authz.Middleware.WrapContextWithUser` then identifies the
  user. `Forwarder.authenticate` (`forwarder.go:552`) builds the
  `authContext` (`forwarder.go:434`), keyed by `(user, cluster, kube cluster,
  groups)`. Note `EnableCredentialsForwarding=true` (`server.go:277`) lets a
  Teleport proxy forward the original identity via headers when this is a
  leaf-cluster Kubernetes service.
- **Session recording:** for `exec`/`portforward`/`attach`, the recording
  hook lives inside `remotecommand.go` and `remotecommand_websocket.go`.
  Audit events flow through the Forwarder's `kubernetesSessionEmitter`.
- **Engine plug-point:** `Forwarder.ServeHTTP` (`forwarder.go:428`) routes
  with httprouter to the SPDY/WebSocket handlers for exec, portforward
  (`portforward_spdy.go`, `portforward_websocket.go`), and the resource
  passthrough.

### 2.7 Git (`lib/srv/git`)

- **Entry point:** `git.NewForwardServer(*ForwardServerConfig)`
  (`forward.go:207`).
- **Main type:** `git.ForwardServer` (`forward.go:177`). It is an in-memory
  SSH server analogous to the SSH forwarding node, specialized for
  proxying `git-upload-pack` / `git-receive-pack` to remote git hosts
  (GitHub today).
- **Listening:** none; `Dial()` (`forward.go:201`) returns the in-memory
  client side; `Serve()` (`forward.go:262`) runs the in-memory server.
- **Authentication:** `userKeyAuth` (`forward.go:301`) verifies the user's
  SSH cert from the Teleport CA, evaluates RBAC via
  `evaluateGitForwarding` (`lib/srv/authhandlers.go:1063`) producing a
  `GitForwardingPermit`, then `onConnection` and `onChannel`
  (`forward.go:365`, `390`) accept the channel.
- **Outbound auth:** `MakeGitHubSigner` (`github.go:233`) signs a per-
  request GitHub user cert via the connected GitHub integration; the cert
  is downloaded with the GitHub CA in `githubKeyDownloader`
  (`github.go:47-145`).
- **Session recording:** `handleExec` (`forward.go:505`) inspects the
  `git-upload-pack`/`git-receive-pack` command, builds a
  `CommandRecorder` (`command.go`), and emits `apievents.GitCommand`
  events via `emitEvent` / `makeGitCommandEvent` (`forward.go:554, 564`).
- **Engine plug-point:** `Command.parseSSHCommand` (`command.go:58`)
  parses the SSH "command" string to determine which git verb is
  requested and which repo.

### 2.8 MCP (`lib/srv/mcp`)

- **Entry point:** `mcp.NewServer(ServerConfig)` (`server.go:116`).
- **Main type:** `mcp.Server` (`server.go:109`). MCP is wired *as an app*
  on the wire (it rides the app TCP path) and is dispatched into this
  package once the app subsystem identifies the protocol as MCP.
- **Listening:** none directly. Two transports are supported:
  - stdio: `Server.handleStdio` (`stdio.go:79`) execs a local MCP server
    binary and proxies JSON-RPC over its pipes.
  - HTTP/SSE: `Server.handleStreamableHTTP` (`http.go:72`) reverse-proxies
    JSON-RPC over a streamable HTTP transport with a SSE listen stream
    (`sse.go`).
- **Authentication:** the app service has already authorized the user and
  hands a fully-populated `SessionCtx` to `Server.HandleSession`
  (`server.go:142`).
- **Session recording:** all JSON-RPC traffic is filtered through
  `sessionHandler` (`session.go:143`). `onClientRequest` (line 223) and
  `onServerResponse` (line 241) audit each call and enforce per-tool RBAC
  via `checkAccessToTool` (line 185).
- **Engine plug-point:** stdio (`stdio.go`), HTTP/SSE (`http.go`, `sse.go`).
  Demo server lives at `demo.go`.

### 2.9 Discovery (`lib/srv/discovery`)

- **Entry point:** `discovery.New(ctx, *Config)`
  (`lib/srv/discovery/discovery.go:470`).
- **Main type:** `discovery.Server` (`discovery.go:389`) - this is a *control-
  plane* worker, not a connection accepter. It owns watchers for EC2, GCP
  VMs, Azure VMs, EKS/GKE/AKS, RDS/Aurora/etc., plus access-graph fetchers
  and the SSM/Azure/GCP installer scripts.
- **Listening:** none.
- **Authentication:** uses its own service identity to call the auth API.
- **Session recording:** N/A; this service produces audit events about
  discovered resources, not session events.
- **Plug-points:** fetchers in `lib/srv/discovery/fetchers/`, installers in
  `lib/srv/server/`.

---

## 3. The Proxy (`lib/proxy`, `lib/srv/alpnproxy`)

### 3.1 What the proxy is

The Teleport Proxy is the single multi-protocol front door. On a typical
install it listens on a single TCP port (default 443) and rides ALPN/SNI to
demux SSH, TLS, HTTPS, DB protocols, RDP-over-TDP, kube, gRPC, and reverse-
tunnel traffic onto independent handlers. The wiring lives in
`lib/service/service.go:initProxyEndpoint` (line 4943).

### 3.2 ALPN router (`lib/srv/alpnproxy/proxy.go`)

- `alpnproxy.Proxy` (`proxy.go:242`) is the listener-level component.
- `alpnproxy.Router` (`proxy.go:88`) holds the rule table.
- `Router.Add(HandlerDecs)` (`proxy.go:179`) registers one rule.
  `MatchFunc` is built by either `MatchByProtocol(protocols...)`
  (`proxy.go:101`) or `MatchByALPNPrefix(prefix)` (`proxy.go:113`).
  `HandlerDecs.ForwardTLS` (true for things like kube and reverse-tunnel
  gRPC) means "do not terminate TLS, hand the wrapped conn to the
  handler". `TLSConfig` allows a per-rule override (used for SSH where
  the ALPN list must be `teleport-proxy-ssh`).
- Rule registration for the proxy is centralized in
  `lib/service/service.go` near lines 6147-6644; the dispatch table
  effectively covers DB (`teleport-mysql`, `teleport-postgres`,
  `teleport-mongodb`, `teleport-oracle`, `teleport-redis`,
  `teleport-snowflake`, `teleport-sqlserver`, `teleport-cassandra`,
  `teleport-spanner`), kube, web (HTTP/HTTP2/ACME), reverse-tunnel
  (`teleport-reversetunnel`, `-v2`), proxy SSH (`teleport-proxy-ssh`,
  `teleport-proxy-ssh-grpc`), gRPC (insecure/secure), app HTTPS tunnel
  (`teleport-app-https-ping`), and auth (`teleport-auth@<cluster>` ALPN
  prefix).

### 3.3 Per-connection dispatch (`Proxy.handleConn`)

Order of operations in `alpnproxy/proxy.go:384`:

1. `readHelloMessageWithoutTLSTermination` (line 531) reads the TLS
   ClientHello with a side-effect callback so the SNI and ALPN list are
   recorded without consuming the bytes.
2. `getHandlerDescBaseOnClientHelloMsg` (line 605) picks the matching
   `HandlerDecs` from the router.
3. If `ForwardTLS` is true, the original (still un-TLSed) conn is handed to
   the handler.
4. Otherwise a `tls.Server` is wrapped on the conn, the handshake runs,
   then `checkCertIPPinning` enforces the role-level IP pinning constraint
   early so a bad client doesn't reach a backend.
5. If the negotiated ALPN protocol is one of the "ping" variants
   (`*-ping`), `handlePingConnection` (line 486) wraps the conn so the
   client can keep an otherwise idle connection alive.
6. Finally `handlerDesc.handle` runs - which in most cases means calling
   `MuxListenerWrapper.HandleConnection` to feed the conn into a
   per-protocol listener.

### 3.4 SSH and DB sub-proxies

- The SSH proxy itself is the same `regular.Server` package, started in
  proxy mode via `regular.SetProxyMode` (`sshserver.go:522`). It accepts
  the post-ALPN proxy-SSH connections from
  `listeners.ssh` and runs proxy-only subsystems (e.g. `proxy:` lookup,
  `proxy_jump`).
- The DB proxy lives in `lib/srv/db/proxyserver.go`. `NewProxyServer`
  (line 155) builds it. There is a separate listener per "native" wire
  protocol:
  - `ServePostgres` (line 182) - direct Postgres listener
  - `ServeMySQL` (line 208) - direct MySQL listener
  - `ServeMongo` (line 232)
  - `ServeTLS` (line 268) - generic per-protocol TLS handler reached
    through ALPN ("Mongo/Oracle/Redis/Snowflake/SQL Server/Cassandra/Spanner")
  - In the multiplexed ALPN case, the ALPN router forwards Postgres/MySQL
    to `PostgresProxy()` / `MySQLProxy()` (lines 358-403); for the rest
    the router terminates ALPN-TLS and pushes the inner conn through
    `AddDBTLSHandler`'s registered handler
    (`service.go:6641`: `router.AddDBTLSHandler(webTLSDB.HandleConnection)`).

The proxy never speaks the upstream DB wire protocol itself; it just
authorizes the request and then dials the *database service* over the
reverse tunnel using `localCluster.Dial` (`lib/reversetunnel/local_cluster.go:279`),
where the database service does the protocol work.

### 3.5 Lower-level multiplexer (`lib/multiplexer`)

`alpnproxy` runs on top of TLS; sometimes the proxy *also* needs to
distinguish SSH/TLS/HTTP/Postgres from a single raw TCP port (e.g. the
classic pre-ALPN deployment). That is the `multiplexer.Mux` in
`lib/multiplexer/multiplexer.go`.

- `multiplexer.Mux` (`multiplexer.go:178`) accepts raw conns and routes
  them to one of four registered sub-listeners: `SSH()` (line 199), `TLS()`
  (line 209), `DB()` (Postgres-only, line 219), `HTTP()` (line 229).
- `Serve()` (line 264) does the accept loop; `detectAndForward`
  (line 320) dispatches a goroutine per conn.
- `detect` (line 556) reads bytes through a `bufio.Reader`, optionally
  consuming up to two PROXY-protocol lines (v1 text or v2 binary). It
  alternates between protocol detection (`detectProto`, line 851) and PROXY
  consumption. Signed PROXY-v2 lines from another Teleport proxy are
  verified against the host CA via
  `ProxyLine.VerifySignature` (line 622-639). Unsigned PROXY lines are
  gated by `PROXYProtocolMode` (line 657).
- The PROXY signer/verifier itself is `multiplexer.PROXYSigner`
  (`multiplexer.go:894`); `SignPROXYHeader` (line 912) emits a JWT-signed
  PROXY v2 header so internal proxy-to-service hops can carry the real
  client IP without trusting unauthenticated PROXY lines from outside.

---

## 4. Reverse tunnel & relay

### 4.1 Classical reverse tunnel (`lib/reversetunnel`)

The reverse tunnel is how an agent (SSH node, app server, DB server, kube
service, leaf proxy) that lives behind NAT exposes itself to the cluster.
It is SSH-on-TCP, by design - the agent dials *out* to the proxy on the
reverse-tunnel ALPN (`teleport-reversetunnel[v2]`) and the proxy holds the
connection open and uses it to dial *back* later.

Key pieces:

- **Server side** (proxy): `reversetunnel.NewServer`
  (`lib/reversetunnel/srv.go:325`) returns a
  `reversetunnelclient.Server`. Implementation type is `server`
  (`srv.go:86`).
- `(*server).handleHeartbeat` (`srv.go:817`) is the per-agent demux: it
  reads the system role from the SSH cert extension (`extCertRole`) and
  routes to one of the per-`SystemRole` handlers
  (`Node`, `App`, `Kube`, `Database`, `Proxy`, `WindowsDesktop`, `Okta`).
  Proxy-from-leaf-cluster goes through `handleNewCluster`
  (`srv.go:879`); the rest go through `handleNewService`
  (`srv.go:861`) which `upsertServiceConn` into a `*localCluster` and
  starts `handleHeartbeat` on the resulting `remoteConn`.
- **Local cluster**: `lib/reversetunnel/local_cluster.go`. `localCluster.Dial`
  (`local_cluster.go:279`) is the main "outbound" entry: it takes a
  `DialParams` (target server ID, principals, optional agent getter), and
  either `dialDirect`, `dialTunnel` (over the reverse SSH conn), or
  `dialAndForward` (start a `forward.Server` for recording-proxy mode).
- **Leaf cluster**: `lib/reversetunnel/leaf_cluster.go`. A `leafCluster`
  represents the trusted-cluster connection over which one cluster's
  proxies dial into another's.
- **Agent side**: `lib/reversetunnel/agent.go`. `agent.Start`
  (`agent.go:319`) opens a single SSH connection to the proxy.
  `sendFirstHeartbeat` (`agent.go:443`) declares the role; from then on
  the agent listens for `chanTransport` channels from the proxy and pipes
  each one to a local `transportHandler` (e.g. the SSH node's
  `HandleConnection`).
- **Agent pool**: `lib/reversetunnel/agentpool.go`. `NewAgentPool`
  (`agentpool.go:190`) and `AgentPool.Start` (`agentpool.go:255`) manage
  a configurable number of agents per proxy, coordinated via the
  `track.Tracker` so each proxy gets approximately one agent connection.

### 4.2 Relay (`lib/relaytunnel`, `lib/relaytransport`, `lib/relaypeer`)

The "relay" stack is the newer alternative to classical reverse tunnels.
The relay is a stateless service that sits between the agent and the
proxy/auth, accepting yamux-multiplexed connections from agents and
dialling out to peer relays as needed.

- `lib/relaytunnel/tunnel_server.go` - server side that accepts agent
  yamux tunnels.
  - `NewServer` (line 55), `Server` (line 84).
- `lib/relaytunnel/tunnel_client.go` - agent side that maintains a pool
  of yamux tunnels to relays. `NewClient` (line 71), `Client.Start`
  (line 147), `dialLoopGrouped` (line 293) which keeps the target
  connection count per relay group.
- `lib/relaytunnel/tunnel_common.go` - shared discovery (`discover.go`)
  and `infoholder` used to surface "currently connected relays" to
  heartbeats.
- `lib/relaytransport/sni.go` - `SNIDispatchTransportCredentials` and
  `transcripterConn`/`transcriptedConn`. This is the SNI-aware gRPC
  transport credentials wrapper: it sniffs the TLS ClientHello server name
  before handing the conn to gRPC, so a single gRPC port can also dispatch
  raw yamux tunnel connections by SNI.
- `lib/relaypeer/server.go` - relay-to-relay peering server. A relay that
  receives a request for an agent it isn't directly connected to forwards
  that request to its peers. `ServeTLSListener` (line 116) accepts peer
  connections, `handleTLSConnection` (line 211) demuxes them.
- `lib/relaypeer/client.go` - relay-to-relay peering client. `Client.Dial`
  (line 129) tries to reach an agent through any of a list of peer
  relays.

Difference summary:

| Package | Role |
| --- | --- |
| `lib/reversetunnel` | SSH-based reverse tunnel server (lives in proxy) |
| `lib/reversetunnelclient` | Type definitions + client interface used everywhere a "tunnel" is dialled |
| `lib/relaytunnel` | yamux-over-TLS relay server (proxy/relay side) and client (agent side) |
| `lib/relaytransport` | gRPC transport-credentials wrapper that lets one TLS port host both gRPC and yamux relay tunnels (SNI-based dispatch) |
| `lib/relaypeer` | Relay-to-relay peering: forward a tunnel-dial across multiple relays |

---

## 5. Session lifecycle

This describes the SSH session because it is the canonical case; database,
kube, and desktop sessions are slimmer variants using the same building
blocks.

### 5.1 Creation

1. SSH `session` channel opens; `handleSessionRequests` builds
   `srv.ServerContext` (`sshserver.go:1723`).
2. On `shell` or `exec` request, `SessionRegistry.OpenSession` /
   `OpenExecSession` is called (`lib/srv/sess.go:357, 413`).
3. `newSession` (`sess.go:815`) populates an `rsession.Session` struct
   from `lib/session/session.go` (this is the *resource* shape used for
   listing) and constructs a `*srv.session` (`sess.go:725`) which is the
   *runtime* shape.
4. A `SessionTracker` (`lib/srv/sessiontracker.go:38`) is created
   immediately via `NewSessionTracker` (line 50) which both stores the
   tracker in memory and writes a `types.SessionTracker` resource through
   `services.SessionTrackerService`. `UpdateExpirationLoop` (line 83)
   keeps the resource alive.
5. The session evaluates moderation policy via
   `moderation.NewSessionAccessEvaluator` (`sess.go:860`), producing a
   `SessionAccessEvaluator` (`sess.go:711`).

### 5.2 Session control

- `lib/srv/session_control.go` is the per-connection gate. `SessionController`
  (`session_control.go:135`) is built once per server.
  `AcquireSessionContext` (called from `sshserver.go:1418`) enforces:
  - locks (`LockEnforcer.CheckLockInForce`)
  - device-trust (`dtauthz` package)
  - private-key-policy
  - max concurrent sessions (semaphore via
    `Semaphores` in the cluster config)

### 5.3 Join / share semantics (moderated sessions)

- `SessionRegistry.JoinSession` (`sess.go:382`) is the join entry.
- The joining identity is checked via `SessionAccessEvaluator.CanJoin`
  which returns the set of allowed `types.SessionParticipantMode`
  (`peer`, `moderator`, `observer`).
- The "where" condition on join policies is evaluated inside
  `moderation.NewSessionAccessEvaluator` (in `lib/auth/moderation`); it
  receives the participant context and the session tracker fields.
- `session.checkIfStartUnderLock` (`sess.go:2007`) re-runs the policy
  fulfillment check (`FulfilledFor`) whenever a participant joins or
  leaves; if the policies become unfulfilled mid-session the registry
  pauses I/O (the `TermManager` is suspended) until a moderator
  rejoins. The opposite path - first start - is at `sess.go:1432`.
- The internal SSH "join" principal is `teleport.SSHSessionJoinPrincipal`
  (`-teleport-internal-join`); `direct-tcpip` channels from this principal
  are explicitly rejected at `sshserver.go:1591` so a joiner cannot use
  the join cert to bypass RBAC into port-forwarding.

### 5.4 Termination & recording handoff

- `session.Close` (`sess.go:1017`) closes parties, stops the terminal
  (`haltTerminal`, line 988), emits the session-end event
  (`emitSessionEndEvent`, line 1217), and closes the recorder *in a
  goroutine* using the server's close context so that uploads can outlive
  the session.
- `Recorder()` (`sess.go:941`) returns the current `events.SessionPreparerRecorder`;
  `setRecorder` (line 947) is the one place that swaps it for the
  discard recorder when recording is best-effort and fails.

### 5.5 `lib/session` vs `lib/srv/sess.go`

- `lib/session/session.go` defines the *transport* types: `session.ID`
  (line 39), `session.Session` (line 78), `session.Party` (line 168),
  `TerminalParams` (line 192). These are what gets serialized to the auth
  server and shown in `tsh ls`/the UI.
- `lib/srv/sess.go` defines the runtime `*session` (line 725) and the
  registry. These never leak out of the process; they hold the live
  `Terminal`, `TermManager`, party set, recorder, and access evaluator.

---

## 6. Session recording & playback

### 6.1 Where bytes are captured

- **Interactive SSH I/O.** `term.go` defines the `Terminal` interface
  (`term.go:61`). For a local PTY (`terminal`, line 145) the PTY master is
  attached to the channel through a `TermManager`
  (`lib/srv/termmanager.go`) that fans the output to every party and to
  the recorder. The recorder is added at `sess.go:1442`:
  `s.io.AddWriter(sessionRecorderID, utils.WriteCloserWithContext(...))`.
- **Non-interactive SSH exec.** `localExec.Start` (`exec.go:153`)
  installs Go pipes for shell stdout/stderr; the streams are forwarded
  through the same `TermManager`, so the recorder writer captures them.
- **DB queries.** Recording is at the engine layer. Each engine that
  parses the wire protocol (e.g. Postgres `auditQueryMessage`/
  `auditParseMessage`/`auditBindMessage`, `db/postgres/engine.go:378-417`)
  calls `Audit.OnQuery` / `OnResult` which both emit an audit event
  *and* record a `DatabaseSessionQuery` event through
  `events.SessionPreparerRecorder`. The recorder itself is built by
  `db.Server.newSessionRecorder` and threaded into `common.Audit` via
  `NewAudit` (`db/common/audit.go:126`).
- **RDP frames.** Captured in `WindowsService.recordEvent`
  (`desktop/windows_server.go:1054`). The `makeTDPSendAuditor`
  (line 1083) records `ServerHello`, `FastPathPDU`, `PNGFrame`, and
  `Alert` going from the desktop to the user; the
  `makeTDPReceiveAuditor` (line 1127) records `ClientScreenSpec`,
  `MouseButton`, `MouseMove` going from the user to the desktop. Pure
  passthrough (clipboard, shared-directory ack/resp) is *emitted as audit
  events* but not stored in the recording.

### 6.2 Recorder shape

`events.SessionPreparerRecorder` is built per-session through
`recorder.New` (`sess.go:1600`, also used by db, app, desktop). Each one
streams to either:
- a direct upload to the cluster audit handler (if cluster recording is
  not "proxy-sync"/"node-sync"), or
- the local disk cache, replayed by the upload-completer service.

The streamer plumbing is `lib/events/session_writer.go` (`NewSessionWriter`,
line 41) and `lib/events/auditlog.go` (`UploadEncryptedRecording`,
line 629). The high-level streaming protocol is `events.ProtoStream`.

### 6.3 Playback (`lib/player`)

- `player.Player` (`lib/player/player.go:45`) consumes an
  `events.AuditEvent` stream from a `Streamer` (line 96) and emits the
  events at real time (delay-controlled) to its `C()` channel.
- `Player.SetSpeed`, `Player.Pause`, `Player.Play`, `Player.SetPos`
  (lines 188, 303, 311, 320) are the user-facing controls.
- For DB sessions, the events are not displayable as terminal I/O on their
  own; `lib/player/db/postgres.go` provides `PostgresTranslator`
  (line 30) which converts `DatabaseSessionQuery` / `DatabaseSessionCommandResult`
  events into synthetic `SessionPrint` events that look like a terminal
  log (the `tsh play --format=text` view). The translator factory is
  selected through `newSessionPrintTranslatorFunc` (`player.go:105`).
- Desktop sessions play back through the same `Player`; the consumer is
  the web client which renders TDP `PNGFrame`/`FastPathPDU` events into
  the same `<canvas>` it uses for live sessions.

---

## 7. Database engines inventory

All engines live under `lib/srv/db/<engine>/engine.go` and are registered
in `lib/srv/db/server.go:84-96` via `common.RegisterEngine`.

| Engine | Protocol const | TLS/auth approach (`connector.go` / `connect.go`) | Audit events emitted |
| --- | --- | --- | --- |
| Postgres / CockroachDB | `ProtocolPostgres`, `ProtocolCockroachDB` | mTLS via `Auth.GetTLSConfig`; RDS/Aurora & Cloud SQL use `GetRDSAuthToken` / `GetCloudSQLAuthToken` to fetch a 15-minute IAM password (`postgres/connector.go:62-100`); pure self-hosted uses the engine's signed client cert. Cancel-request flow has its own `sendCancelRequest` path. | `DatabaseSessionStart{,Failure}`, `DatabaseSessionEnd`, `DatabaseSessionQuery{,Failed}`, `DatabaseSessionParse`, `DatabaseSessionBind`, `DatabaseSessionExecute`, `DatabaseSessionClose`, `DatabaseSessionFunctionCall`, `DatabaseSessionPermissionsUpdate`, `DatabaseSessionUserCreate`, `DatabaseSessionUserDeactivate` |
| MySQL / Aurora MySQL / MariaDB | `ProtocolMySQL` | mTLS like Postgres; RDS uses RDS IAM tokens; Cloud SQL injects a Cloud SQL password via `GetCloudSQLPassword`. AWS Aurora and Redshift IAM tokens supported. (`mysql/engine.go`, `mysql/connector.go`). Includes a custom handshake to capture the server's protocol version (`mysql/handshake.go`) so `tsh db connect` advertises the real version. | `DatabaseSessionStart`, `DatabaseSessionEnd`, `DatabaseSessionQuery` (statements parsed from `COM_QUERY` packets) |
| MongoDB | `ProtocolMongoDB` | mTLS to the upstream; mongo-driver-style wire protocol. The auth path is at `mongodb/connect.go`. Auto-users: `mongodb/autousers.go`/`autousers_admin.go`. | `DatabaseSessionStart`, `DatabaseSessionEnd`, `DatabaseSessionQuery` |
| Redis | `ProtocolRedis` | RESP3-aware proxy. ElastiCache uses `GetElastiCacheRedisToken`; MemoryDB uses `GetMemoryDBToken`; Azure Cache uses `GetAzureCacheForRedisToken`. (`redis/engine.go`, `redis/connection/*`). | `DatabaseSessionStart`, `DatabaseSessionEnd`, `DatabaseSessionQuery` |
| SQL Server | `ProtocolSQLServer` | Kerberos / NTLM via `gokrb5`. `sqlserver/auth.go:29` builds the Kerberos client from the Teleport-managed AD config; `sqlserver/connect.go:74` performs the TDS handshake. Requires either a keytab or `kinit` flow (see `db/common/kerberos/kinit/`). | `DatabaseSessionStart`, `DatabaseSessionEnd`, `DatabaseSessionRPCRequest` |
| Snowflake | `ProtocolSnowflake` | HTTPS reverse proxy that injects a server-generated JWT (`snowflake/engine.go:88` and the token handlers at lines 221-505). Auth uses Snowflake key-pair JWT created by the engine signing with the cluster CA's database key. Outbound TLS goes through `getRoundTripper`. | `DatabaseSessionStart`, `DatabaseSessionEnd`, plus an HTTP-level audit per Snowflake operation |
| ClickHouse (native and HTTP) | `ProtocolClickHouse`, `ProtocolClickHouseHTTP` | Two transports: native ClickHouse binary protocol and HTTP. mTLS to the upstream. (`clickhouse/engine_native.go`, `clickhouse/engine_http.go`.) | `DatabaseSessionStart`, `DatabaseSessionEnd`, query events parsed from the native or HTTP protocol |
| DynamoDB | `ProtocolDynamoDB` | HTTPS reverse proxy using AWS sigv4 (`dynamodb/engine.go:134, 322-487`). The engine signs every request with credentials assumed from the Teleport service IAM role. Endpoint rewriting normalises FIPS, regional, and DAX endpoints. | `DatabaseSessionStart`, `DatabaseSessionEnd`, per-request `DatabaseSessionQuery` carrying the AWS target (`X-Amz-Target`) and status |
| Elasticsearch / OpenSearch | `ProtocolElasticsearch`, `ProtocolOpenSearch` | HTTPS reverse proxy; OpenSearch with sigv4 signing for AWS-hosted clusters (`opensearch/sigv4`). (`elasticsearch/engine.go`, `opensearch/engine.go`.) | `DatabaseSessionStart`, `DatabaseSessionEnd`, request-level events with category metadata |
| Redshift | (uses `ProtocolPostgres` engine) | `Auth.GetRedshiftAuthToken` / `GetRedshiftServerlessAuthToken` (`db/common/auth.go:345-505`) generates an IAM password for the Postgres connector. | Same as Postgres |
| Cassandra / ScyllaDB | `ProtocolCassandra` | mTLS to the upstream, wire-protocol parser in `cassandra/protocol`. (`cassandra/engine.go`, `cassandra/handshake.go`.) | `DatabaseSessionStart`, `DatabaseSessionEnd`, `DatabaseSessionQuery` |
| GCP Spanner | `ProtocolSpanner` | gRPC, OAuth via `Auth.GetSpannerTokenSource` (`db/common/auth.go:541`). The engine is itself a gRPC server (`spanner/grpcserver.go`) that proxies to Spanner's gRPC. | `DatabaseSessionStart`, `DatabaseSessionEnd`, per-RPC events through `spanner/interceptors.go` |
| Oracle | `ProtocolOracle` | Enterprise-only. No engine directory in the OSS tree; ALPN slot is reserved in `lib/service/service.go:6171` and handled via the generic DB TLS path. |  |

`common.Engine` (interface in `lib/srv/db/common/interfaces.go:73`) and
`common.Audit` (`lib/srv/db/common/audit.go:37`) are the two contracts an
engine must satisfy. `common.Auth` (`auth.go:77`) is the wide interface
that exposes IAM-token, TLS-config, and key-generation helpers to every
engine. Health-checking is registered separately via
`healthchecks.RegisterHealthChecker` in the same `init()` block.

---

## 8. Application access

### 8.1 HTTP apps - JWT signing

Every HTTP request proxied through an app server is forwarded with a per-
request Teleport JWT in `Teleport-Jwt-Assertion`. The JWT is generated by
`common.GenerateJWTAndTraits` (`lib/srv/app/common/jwt.go:43`):

1. The user's identity, the requested app, and the cert expiry are passed
   to `AppTokenGenerator.GenerateAppToken` (an auth-server RPC).
2. The returned JWT is added to a `wrappers.Traits` map under the
   `constants.TraitJWT` key so the app's `rewrite.headers` template can
   reference `{{internal.jwt}}` and other identity fields.
3. The token is attached to a reverse-proxy `transport` built by
   `withJWTTokenForwarder` (`lib/srv/app/session.go:151`); the per-app
   `*reverseproxy.Proxy` uses this transport for the lifetime of the
   session "chunk".

`RolesAndTraitsForAppToken` (`common/jwt.go:73`) honours
`app.GetRewrite().JWTClaims` (`none`/`roles`/`traits`) to scope the claim
set.

### 8.2 AWS console (federation)

`serveAWSWebConsole` (`lib/srv/app/connections_handler.go:510`) is called
when the request was identified as an AWS console application. It builds
an `AWSSigninRequest` and calls `cloud.GetAWSSigninURL`
(`lib/srv/app/cloud.go:140`). The flow inside `getAWSSigninToken`
(`cloud.go:172`) uses STS `AssumeRole` to get short-lived credentials,
then calls the AWS federation endpoint to mint a session URL, finally
HTTP-redirecting the browser to
`https://signin.aws.amazon.com/federation?Action=login&...`.

### 8.3 AWS sigv4 / Azure / GCP APIs

For programmatic API access (e.g. `tsh aws ...`), the per-session
handler is one of:
- `withAWSSigner` -> `lib/srv/app/aws/handler.go` (sigv4)
- `withAzureHandler` -> `lib/srv/app/azure/handler.go` (Azure token
  exchange; the request carries an `Azure-Identity` header and the
  handler exchanges it for an Azure AD token using
  `azure/credential.go`)
- `withGCPHandler` -> `lib/srv/app/gcp/handler.go` (impersonates a GCP
  service account)

In every case the same `*reverseproxy.Proxy` is used; only the
`http.RoundTripper` differs.

---

## 9. Desktop access (RDP via Rust client)

### 9.1 The Go side

`WindowsService.connectRDP` (`lib/srv/desktop/windows_server.go:754`) is
the choreographer. It:

1. Parses target `desktop.GetAddr()` and looks up the session recording
   config.
2. Calls `s.generateUserCert` (line 1291) which uses the Teleport-managed
   AD CA to sign a user cert for the RDP NLA handshake. The cert is
   submitted to the AD CA in real time (Active-Directory-issued by
   `lib/winpki`).
3. Calls `rdpclient.PrepareConnecton`
   (`lib/srv/desktop/rdp/rdpclient/client.go:176`) which reads the
   `ClientHello` from the TDP stream from the browser.
4. Calls `rdpclient.New` (`client.go:188`) to construct a `*Client`.
5. Calls `Client.Run(ctx, certDER, keyDER)` (`client.go:215`).

### 9.2 The Rust side

`Client.Run` enters `startRustRDP` (`client.go:408`) which calls the C
function `client_run` (extern from `librdpclient.a`). The Rust side
(`lib/srv/desktop/rdp/rdpclient/src/`) wraps the IronRDP
(`github.com/Devolutions/IronRDP`) library and:

- Connects to the Windows host and performs the RDP negotiation,
  including CredSSP/NLA when enabled.
- Runs two loops: `run_read_loop` (RDP -> Teleport) and `run_write_loop`
  (Teleport -> RDP).
- For every RDP fast-path PDU (the streamed screen update) it calls back
  into Go via `cgo_handle_fastpath_pdu` (`client.go:1019`), which calls
  `handleRDPFastPathPDU` (`client.go:1028`) to wrap the PDU in a TDP
  `FastPathPDU` and forward it over the TDP connection to the browser.
- License caching: `cgo_read_rdp_license` (`client.go:919`) and
  `cgo_write_rdp_license` (`client.go:957`) round-trip through the Go-
  side license store.

CGo flags (`client.go:60-68`) statically link `librdp_client.a` per
target triple.

### 9.3 The browser side

The browser receives raw TDP messages over a WebSocket. The active
client lives in
`web/packages/shared/libs/tdp/client.ts`. RDP frame decoding (RemoteFX,
fast-path) is done inside the IronRDP WASM bundle at
`web/packages/shared/libs/ironrdp/` (Rust crate, built to
`pkg/ironrdp_bg.wasm`); `client.ts` imports
`ironrdp_bg.wasm?inline` and the typed bindings from `pkg/ironrdp`
(lines 27, 29 of `client.ts`).

---

## 10. Multiplexer & ALPN routing (end-to-end order)

For a single TCP connection arriving at a proxy front-door in the common
"multiplex everything on 443" deployment, the order of operations is:

1. **Outer accept.** Whatever listener is on `:443` is owned by an
   `alpnproxy.Proxy` (`lib/srv/alpnproxy/proxy.go:318` `Serve`).
2. **PROXY-protocol consumption.** If the proxy is configured behind a
   load balancer that injects PROXY headers, this happens *before* TLS in
   `multiplexer.Mux.detect` (`lib/multiplexer/multiplexer.go:556`) - but
   only on deployments that still wrap the front door in a `Mux`. In
   pure-ALPN deployments the load balancer must inject *signed* PROXY v2
   headers (see `multiplexer.PROXYSigner`, `multiplexer.go:894`) which
   are verified later.
3. **ClientHello peek.** `Proxy.readHelloMessageWithoutTLSTermination`
   (`alpnproxy/proxy.go:531`) buffers and parses the TLS ClientHello
   without completing the handshake. SNI and ALPN list are extracted.
4. **Route lookup.** `getHandlerDescBaseOnClientHelloMsg`
   (`alpnproxy/proxy.go:605`) walks `Router.descs` in registration order
   and returns the first `HandlerDecs` whose `MatchFunc` matches.
   `getHandleDescBasedOnALPNVal` (line 616) is the inner function used.
5. **Forward-TLS short-circuit.** If `HandlerDecs.ForwardTLS == true`,
   the still-untemrinated conn is given to the handler (kube, gRPC
   secure, reverse-tunnel-v2, auth-via-`teleport-auth@`).
6. **TLS handshake.** Otherwise `tls.Server` is wrapped on the conn
   (using either the handler's `TLSConfig` or the default web TLS
   config), `HandshakeContext` runs, then `SetReadDeadline(0)`.
7. **IP pinning early-check.** `checkCertIPPinning`
   (`alpnproxy/proxy.go:455`) inspects the peer cert and refuses the
   conn early if role-level IP pinning fails.
8. **Ping wrapping.** If the negotiated ALPN ends in `-ping`, the
   conn is wrapped with a keep-alive layer (`handlePingConnection`,
   line 486).
9. **Dispatch.** `handlerDesc.handle` is called; most handlers feed the
   conn into a per-protocol `MuxListenerWrapper` whose `Accept` is the
   per-subsystem listener (so the SSH proxy, web HTTP, DB proxies,
   gRPC, etc. all see "their" listener even though the bytes came from
   the shared front door).

ALPN protocol constants live in
`lib/srv/alpnproxy/common/protocols.go:35-129`. The list is summarised
in §3.2.

For raw TCP front doors that pre-date ALPN, `multiplexer.Mux` provides
`SSH()`, `TLS()`, `DB()` (Postgres only), and `HTTP()` listeners; the
`Mux` is what implements the PROXY-protocol consumption referenced in
step 2.

---

## 11. AI-pointer index (concept -> file:line)

### Service framework
- Service interface: `lib/service/supervisor.go:638`
- Supervisor interface: `lib/service/supervisor.go:42`
- `TeleportProcess`: `lib/service/service.go:631`
- Per-role bootstrap: `service.go:2208` (auth), `3481` (ssh), `4533` (proxy),
  `6713` (apps); `lib/service/db.go:53`, `lib/service/kubernetes.go:75`,
  `lib/service/desktop.go:54`, `lib/service/discovery.go:46`
- `srv.Server` interface: `lib/srv/ctx.go:128`
- `srv.AccessPoint` interface: `lib/srv/ctx.go:95`
- `srv.IdentityContext`: `lib/srv/ctx.go:219`
- `srv.ServerContext`: `lib/srv/ctx.go:317`

### SSH
- Node server type: `lib/srv/regular/sshserver.go:86`
- Node server constructor: `lib/srv/regular/sshserver.go:844`
- SSH session start: `lib/srv/regular/sshserver.go:1713` `handleSessionRequests`
- SSH channel dispatch: `lib/srv/regular/sshserver.go:1481` `HandleNewChan`
- SSH request dispatch: `lib/srv/regular/sshserver.go:1836` `dispatch`
- Forwarding (recording-proxy) server: `lib/srv/forward/sshserver.go:87`,
  `New` at `:329`
- AuthHandlers PublicKeyCallback: `lib/srv/authhandlers.go:374`
- SSH access decision: `lib/srv/authhandlers.go:1247`
- Scoped access decision: `lib/srv/authhandlers.go:1114`
- Session control: `lib/srv/session_control.go:135`

### Sessions & recording
- Session registry: `lib/srv/sess.go:103` (`SessionRegistry`)
- Open interactive session: `lib/srv/sess.go:357`
- Open exec session: `lib/srv/sess.go:413`
- Join session: `lib/srv/sess.go:382`
- Runtime session struct: `lib/srv/sess.go:725`
- Recorder construction: `lib/srv/sess.go:1573` `newRecorder`
- Session tracker: `lib/srv/sessiontracker.go:38`
- Transport `Session` resource: `lib/session/session.go:78`
- `events.SessionWriter`: `lib/events/session_writer.go:41`
- Playback engine: `lib/player/player.go:45`
- DB playback translator: `lib/player/db/postgres.go:30`

### Exec / terminal
- Exec interface: `lib/srv/exec.go:63`
- Local exec (real PTY): `lib/srv/exec.go:121`
- Remote exec (recording proxy): `lib/srv/exec.go:414`
- Terminal interface: `lib/srv/term.go:61`
- Local terminal: `lib/srv/term.go:145`
- Remote terminal: `lib/srv/term.go:557`
- Connection / cert / lock monitor: `lib/srv/monitor.go:119`, `:332`

### Database
- Service: `lib/srv/db/server.go:375`
- Constructor: `lib/srv/db/server.go:467`
- Connection entry: `lib/srv/db/server.go:1161`
- Engine dispatch: `lib/srv/db/server.go:1299`
- Engine registry: `lib/srv/db/common/engines.go`
- Engine interface: `lib/srv/db/common/interfaces.go:73`
- Engine init block: `lib/srv/db/server.go:84-96`
- Audit interface: `lib/srv/db/common/audit.go:37`
- Auth interface (IAM tokens, TLS configs): `lib/srv/db/common/auth.go:77`
- DB proxy server: `lib/srv/db/proxyserver.go:59`
- Postgres engine: `lib/srv/db/postgres/engine.go`
- MySQL engine: `lib/srv/db/mysql/engine.go`
- Snowflake JWT injection: `lib/srv/db/snowflake/engine.go:88,221`
- SQL Server Kerberos auth: `lib/srv/db/sqlserver/auth.go:29`,
  `lib/srv/db/sqlserver/connect.go:74`
- DynamoDB sigv4: `lib/srv/db/dynamodb/engine.go:322`

### Application
- Server: `lib/srv/app/server.go:140`, `New` at `:197`
- Connections handler: `lib/srv/app/connections_handler.go:181`,
  `HandleConnection` at `:344`
- HTTP dispatch (entry): `lib/srv/app/connections_handler.go:448`
- Session chunk: `lib/srv/app/session.go:60`
- JWT forwarder: `lib/srv/app/session.go:151`
- AWS console federation: `lib/srv/app/connections_handler.go:510` ->
  `lib/srv/app/cloud.go:140`
- JWT generation: `lib/srv/app/common/jwt.go:43`

### Desktop
- WindowsService: `lib/srv/desktop/windows_server.go:125`,
  `NewWindowsService` at `:344`
- Serve loop: `lib/srv/desktop/windows_server.go:608`
- Per-connection handler: `lib/srv/desktop/windows_server.go:651`
- RDP connect: `lib/srv/desktop/windows_server.go:754`
- TDP send/receive auditors: `:1083`, `:1127`
- Rust RDP client wrapper: `lib/srv/desktop/rdp/rdpclient/client.go`
- TDP browser client: `web/packages/shared/libs/tdp/client.ts`
- IronRDP WASM: `web/packages/shared/libs/ironrdp/`

### Kubernetes
- TLSServer: `lib/kube/proxy/server.go:209`, `NewTLSServer` at `:232`
- Forwarder: `lib/kube/proxy/forwarder.go:376`, `authenticate` at `:552`
- ServeHTTP entry: `lib/kube/proxy/forwarder.go:428`
- Exec (websocket/spdy): `lib/kube/proxy/remotecommand_websocket.go`,
  `lib/kube/proxy/remotecommand.go`
- Port forward: `lib/kube/proxy/portforward_spdy.go`,
  `lib/kube/proxy/portforward_websocket.go`

### Git
- ForwardServer: `lib/srv/git/forward.go:177`,
  `NewForwardServer` at `:207`
- Channel handling: `:390`
- Exec / command parsing: `lib/srv/git/command.go:58`
- GitHub signer: `lib/srv/git/github.go:233`
- Auth decision: `lib/srv/authhandlers.go:1063` `evaluateGitForwarding`

### MCP
- Server: `lib/srv/mcp/server.go:109`, `NewServer` at `:116`
- Stdio transport: `lib/srv/mcp/stdio.go:79`
- HTTP/SSE transport: `lib/srv/mcp/http.go:72`, `lib/srv/mcp/sse.go`
- Session handler: `lib/srv/mcp/session.go:143`

### Discovery
- Server: `lib/srv/discovery/discovery.go:389`
- New: `lib/srv/discovery/discovery.go:470`
- Fetchers: `lib/srv/discovery/fetchers/`
- Installers: `lib/srv/server/`

### Proxy / multiplexer / ALPN
- ALPN Router: `lib/srv/alpnproxy/proxy.go:88`
- ALPN Proxy: `lib/srv/alpnproxy/proxy.go:242`,
  `handleConn` at `:384`
- ALPN protocols: `lib/srv/alpnproxy/common/protocols.go:32`
- Multiplexer Mux: `lib/multiplexer/multiplexer.go:178`,
  `detect` at `:556`
- Proxy router (host resolution / dial): `lib/proxy/router.go:178`,
  `DialHost` at `:216`, `DialWindowsDesktop` at `:314`

### Reverse tunnel & relay
- Reverse-tunnel server: `lib/reversetunnel/srv.go:325`
- Per-role dispatch: `lib/reversetunnel/srv.go:817` `handleHeartbeat`
- Local cluster (dial back into nodes): `lib/reversetunnel/local_cluster.go:279`
- Leaf cluster: `lib/reversetunnel/leaf_cluster.go:58`
- Agent: `lib/reversetunnel/agent.go:179`, `Start` at `:319`
- Agent pool: `lib/reversetunnel/agentpool.go:77`
- Relay tunnel server: `lib/relaytunnel/tunnel_server.go:55,84`
- Relay tunnel client: `lib/relaytunnel/tunnel_client.go:71,147`
- Relay SNI dispatch: `lib/relaytransport/sni.go:37`
- Relay peer server: `lib/relaypeer/server.go:58,93`
- Relay peer client: `lib/relaypeer/client.go:66,109`

### Ingress / heartbeats / transport
- Ingress reporter: `lib/srv/ingress/reporter.go:174`
- Ingress service constants: `lib/srv/ingress/reporter.go:34`
- HeartbeatV2 SSH: `lib/srv/heartbeatv2.go:83`
- HeartbeatV2 App: `lib/srv/heartbeatv2.go:119`
- HeartbeatV2 Database: `lib/srv/heartbeatv2.go:139`
- HeartbeatV2 Kube: `lib/srv/heartbeatv2.go:159`
- HeartbeatV2 Relay: `lib/srv/heartbeatv2.go:179`
- Transport service v1 (gRPC proxy): `lib/srv/transport/transportv1/transport.go:130`,
  `ProxySSH` at `:218`, `ProxyClusterer` at `:153`,
  `ProxyWindowsDesktopSession` at `:454`
