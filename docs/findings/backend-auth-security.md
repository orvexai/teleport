# Teleport Backend: Authentication, Authorization, and Cryptography

This document covers the Go subsystems under `lib/auth`, `lib/authz`,
`lib/cryptosuites`, `lib/jwt`, `lib/devicetrust`, `lib/devicetpm`,
`lib/oidc`, `lib/saml`, `lib/tlsca`, `lib/sshca`, `lib/subca`,
`lib/scopes`, `lib/loginrule`, `lib/winpki`, `lib/hardwarekey`,
`lib/secret`, `lib/secretsscanner`, `lib/boundkeypair`, `lib/join`,
`lib/decision`, and adapter code in `lib/services/local`. It is organized
to be consumed both as a top-to-bottom narrative and as a lookup index
for AI agents tracing identity, certificates, and policy.

All file paths are absolute under `/home/daniel/repos/teleport`.

---

## 1. High-Level Model

Teleport is fundamentally a **certificate-authority service**: every
human, agent, bot, or service holds an x509 certificate (TLS) and/or an
SSH certificate signed by a Teleport CA. The Auth Service is the only
issuer of those certificates and the only enforcer of root-of-trust
policy.

### 1.1 Request lifecycle (user)

1. **User runs `tsh login`** (client). The client locally generates a
   keypair via `cryptosuites.GenerateUserSSHAndTLSKey()`
   (`lib/cryptosuites/suites.go:444`).
2. Client opens a TLS connection to the Proxy and hits the SSO/local
   login endpoint. Local credential checking happens via
   `lib/auth/methods.go::authenticateUserInternal` (line 382), which
   dispatches between WebAuthn, OTP, passwordless, or password-only
   paths.
3. The Auth Service's `Server.AuthenticateSSHUser`
   (`lib/auth/methods.go:785`) and `Server.AuthenticateWebUser`
   (`lib/auth/methods.go:718`) call `authenticateUserLogin`
   (`lib/auth/methods.go:136`), build a scoped or unscoped
   `AccessCheckerContext`, then call `Server.GenerateUserCerts`
   (`lib/auth/auth.go:3601`) → `generateCert`
   (`lib/auth/auth.go:3610`).
4. `generateCert` performs (a) lock check
   (`verifyLocksForUserCerts`), (b) hardware-key attestation if a
   role-required policy is set (`attestHardwareKey`), (c) signature
   suite resolution, (d) calls into `sshca.UserCertificateRequest`
   and `tlsca.CertAuthority.GenerateCertificate`
   (`lib/tlsca/ca.go:1619`) to sign with keys held by
   `keystore.Manager` (`lib/auth/keystore/manager.go:125`).
5. The cert is returned to the user; subsequent requests to Proxy/Node
   present this cert via mTLS. The Proxy and Node use the same TLS
   middleware (`lib/authz/middleware.go`) to extract identity from
   `tls.ConnectionState.PeerCertificates[0]`, decode it via
   `tlsca.FromSubject`, classify the caller (LocalUser / RemoteUser /
   BuiltinRole / RemoteBuiltinRole), and inject it into context.
6. Per-request authorization flows through
   `lib/authz/permissions.go::authorizer.Authorize` (line 427) which
   produces a fully populated `authz.Context` (line 251) carrying the
   `services.AccessChecker` (`lib/services/access_checker.go:52`).
   Resource handlers then call `Context.CheckAccessToKind`,
   `CheckAccessToResource`, `CheckAccess`, etc.

### 1.2 Where roles live

Roles are stored in the backend as `types.RoleV6` (definition
`api/types/role.go:67` + the protobuf in
`api/types/types.pb.go`). The storage adapter is
`lib/services/local/access.go::AccessService` (`NewAccessService`
line 46, `GetRole` line 220, `UpsertRole` line 184). Roles in memory
become a `services.RoleSet` (`lib/services/role.go:1061`) which is the
underlying "policy database" that backs every `AccessChecker`. RoleSet
gives you `CheckAccess`, `CheckAccessToRule`, `AdjustSessionTTL`,
`CheckKubeGroupsAndUsers`, etc.

### 1.3 Trait interpolation

User traits (from local user, login state, or SSO claims) are merged
into role templates via
`lib/services/role.go::ApplyTraits` (line 494) →
`ApplyTraitsWithContext` (line 500) → `applyLabelsTraits` (line 691)
→ `ApplyValueTraitsWithContext` (line 728) which uses
`lib/utils/parse.Expression`'s `InterpolateWithUser`. The
`RoleTemplateContext` (line 487) carries `{Username, Traits,
LoginTraits, ClusterName, LoginRuleEvaluator}`.

Traits flow:

- SSO callback → claim mapping via
  `lib/services/traits.go::TraitsToRoles` (line 42), backed by
  `types.TraitMappingSet`.
- Local user trait → role mapping happens at role binding time inside
  `services.FetchRolesForUser` (`lib/services/role.go:1043`).
- Login Rules (`lib/loginrule/evaluator.go`) post-process traits
  before roles are applied; the Evaluator interface (line 44) is
  injected into the auth server via
  `lib/auth/auth.go::Server.SetLoginRuleEvaluator` (line 1627). The
  Evaluator can rewrite `(roles, traits)` between SSO validation and
  RoleSet construction. See RFD `rfd/0078-login-rules.md`.

### 1.4 Scoped vs. unscoped identities

A new "scopes" subsystem (RFD `rfd/0229a-scoped-join-tokens.md`,
`rfd/0155-scoped-webauthn-credentials.md`) introduces a scope pin
(`api/gen/proto/.../scopes/v1`) embedded into the identity. Scoped
identities use `services.ScopedAccessChecker`
(`lib/services/scoped_access_checker.go`) and reject many operations:
remote-cluster routing, role impersonation, app sessions, access
requests, etc. (See `lib/auth/auth_with_roles.go:3747` for the gate
in `generateUserCerts`.) Validation lives in `lib/scopes/scopes.go`
(`StrongValidate` line 77, `WeakValidate` line 115).

---

## 2. The Auth Service (`lib/auth`)

### 2.1 Package layout (highlights)

- `auth.go` (9013 lines) – the `Server` struct (line 1280), its
  constructor `NewServer` (line 225), CA initialization helpers,
  certificate issuance entry points (`GenerateUserCerts`,
  `GenerateHostCert`, `GenerateOpenSSHCert`,
  `AugmentUserCertificates`).
- `auth_with_roles.go` (8745 lines) – `ServerWithRoles`, the RBAC
  wrapper. Every gRPC call enters via this type. Method
  `generateUserCerts` (line 3721) is the source of truth for what a
  caller is allowed to ask for in a certificate.
- `grpcserver.go` (6986 lines) – `GRPCServer` (line 223) implements
  the `proto.AuthService` interface. Notable RPCs:
  `GenerateUserCerts` (line 705), `GenerateHostCerts` (line 842),
  `GenerateOpenSSHCert` (line 863), `AssertSystemRole` (line 881),
  `InventoryControlStream` (line 903).
- `init.go` (1872 lines) – `Init` (line 481), `initCluster` (line
  539), and `initializeAuthorities` (line 803) bootstrap or load all
  CAs on startup. The `InitConfig` struct (line 106) is the assembly
  recipe.
- `methods.go` (992 lines) – login surface
  (`AuthenticateSSHUser`, `AuthenticateWebUser`,
  `authenticateUserInternal`, `authenticatePasswordless`,
  `authenticateHeadless`).
- `register.go` (173 lines) – `LocalRegister` (line 38) and
  `ReRegister` (line 124) for in-process / re-key host registration.
- `join.go`, `join_*.go` – per-method legacy join handlers (most
  superseded by `lib/join`).
- `apiserver.go` – HTTP/1.x server surface (the gRPC `grpcserver.go`
  is the primary modern API).
- `middleware.go` – `Middleware` (line 184) wraps the HTTP/gRPC
  servers, calls into `authz.Middleware` to extract identity from
  TLS, and adds rate limiting, account-recovery limits, and metrics.
- `rotate.go` – CA rotation state machine (see §8).
- `keystore/` – CA private key backends (software / PKCS#11 / AWS KMS /
  GCP KMS).
- `webauthn/`, `webauthncli/`, `webauthnwin/`, `webauthntypes/` –
  server, CLI, and Windows-native WebAuthn implementations plus type
  definitions.
- `mfa/mfav1`, `mfa/mfav2` – gRPC services for MFA challenge
  management (mfav2 is the newer in-band SSH MFA per RFD
  `rfd/0234-in-band-mfa-ssh-sessions.md`).
- `machineid/machineidv1`, `machineid/workloadidentityv1` –
  Machine ID / SPIFFE workload identity services.
- `keygen/`, `keystore/`, `state/` – key generation + state file
  storage (host identity persistence).
- `userloginstate/`, `loginrule/`, `okta/` – user provisioning and
  rule evaluation.
- `okta`, `summarizer`, `recordingencryption`, `recordingmetadata`,
  `accessmonitoringrules`, etc. – feature-specific subservices.
- `touchid/`, `webauthnwin/` – platform-specific authenticators.
- `join/`, `join/iam/`, `join/oracle/`, `join/boundkeypair/` – join
  helpers used by both registration and validation paths.

### 2.2 Key types

- `Server` (`lib/auth/auth.go:1280`) – the core auth service. Embeds
  `*Services`, `authclient.Cache`, `*ReadOnlyCache`,
  `sshca.Authority`. Holds the `keystore.Manager`, `LockWatcher`,
  per-join-method validators (GHA, GitLab, Azure DevOps, TPM,
  Spacelift, Bitbucket, k8s, GCP, Terraform, env0, CircleCI), and
  pluggable services (`samlAuthService`, `oidcAuthService`,
  `loginRuleEvaluator`, `createDeviceWebTokenFunc`,
  `deviceAssertionServer`, `sigstorePolicyEvaluator`).
- `Services` (`lib/auth/services.go`) – aggregates every storage
  service implemented by `lib/services/local/` (access, identity,
  presence, provisioning, trust, etc.).
- `ServerWithRoles` (`lib/auth/auth_with_roles.go`) – wraps `Server`
  with the caller's `authz.Context`, enforcing RBAC on every method.
  This is what the gRPC server actually calls into.
- `APIConfig` (`lib/auth/apiserver.go`) – the wiring fed to
  `GRPCServer`.
- `GRPCServer` (`lib/auth/grpcserver.go:223`) – embeds
  `authpb.UnimplementedAuthServiceServer`,
  `auditlogpb.UnimplementedAuditLogServiceServer`, plus a
  rate-limiter for `CreateAuthenticateChallenge`.
- `InitConfig` (`lib/auth/init.go:106`) – startup wiring; carries
  `Authority` (sshca), `KeyStore`, all `services.*Internal`
  storage implementations, OIDC/SAML/Github seed connectors,
  bootstrap resources.
- `AuthServiceClient` – generated gRPC client from
  `api/client/proto/authservice.pb.go`.
- `LoginHook` (`lib/auth/auth.go:1254`) – callback invoked on every
  successful login (used by enterprise/userloginstate to push
  derived user state).
- `CreateDeviceWebTokenFunc` (`auth.go:1263`) – device-trust web
  token creator (nil on OSS).

### 2.3 What the gRPC server exposes

`auth.proto` / `authservice.pb.go` is the canonical contract.
Categories of RPCs (with file references):

- **Identity issuance**: `GenerateUserCerts`, `GenerateHostCerts`,
  `GenerateOpenSSHCert`, `AssertSystemRole`,
  `AugmentContextUserCertificates`, `RotateCertAuthority`.
- **Login / sessions**: `Ping`, `CreateAuthenticateChallenge`,
  `CreateRegisterChallenge`, `ChangeUserAuthentication`,
  `CreateWebSession`, `GetWebSession`, headless authn streams.
- **Resource CRUD**: roles, users, access lists, OIDC/SAML/Github
  connectors, integrations, cluster config, lock CRUD, tokens.
- **Watch streams**: `WatchEvents` (line 553) – cache backbone.
- **Inventory**: `InventoryControlStream` (line 903), heartbeats,
  agent metadata. Used by every Teleport instance.
- **Access requests**: `CreateAccessRequest`, `SubmitAccessReview`,
  `ListAccessRequests`.
- **MFA**: `CreateAuthenticateChallenge` (used to issue both session
  MFA and admin-action MFA challenges).
- **Decision / PDP**: `decision.DecisionServiceServer` exposed
  alongside (`lib/decision/decisionv1`).
- **Audit / events**: `EmitAuditEvent`, `CreateAuditStream`,
  `GetEvents`, `GetSessionEvents`.

---

## 3. Authorization (`lib/authz`)

### 3.1 Identifying the caller

`lib/authz/middleware.go::Middleware.GetUser` (line 136) is the
TLS-to-identity bridge:

1. Read `state.PeerCertificates`; only one peer cert is allowed
   ("intermediaries are not supported" at line 141).
2. `tlsca.FromSubject(cert.Subject, cert.NotAfter)` decodes the
   Teleport identity from x509 extensions.
3. Cluster name is taken from the embedded `TeleportCluster` field
   (post-5.0) or, as a fallback, from the certificate issuer DN
   (`tlsca.ClusterName(cert.Issuer)`).
4. Usage restrictions on the cert are enforced: if
   `identity.Usage` is non-empty it must exactly match
   `Middleware.AcceptedUsage`. This stops, e.g., k8s-only client
   certs from being used against the Auth API
   (`middleware.go:180`).
5. If cluster name != local cluster, the caller becomes
   `RemoteBuiltinRole` (when a system role is present) or
   `RemoteUser`. The comment at `middleware.go:189-201` is critical:
   *"the local auth server can not trust remote servers to issue
   certificates with system roles (e.g. Admin), to get unrestricted
   access to the local cluster"*.
6. Local caller with a system role → `BuiltinRole`; otherwise
   `LocalUser`.

`extractIdentityFromImpersonationHeader` (line 250) handles the
`Teleport-Impersonate-User` header used by the Proxy for credential
forwarding. It refuses to impersonate system roles and refuses a
remote proxy impersonating a user from another cluster.

### 3.2 Authorize → AccessChecker

`authz.authorizer.Authorize` (`permissions.go:427`) is what every
service handler calls (via `Authorizer` interface line 160):

1. Pulls `IdentityGetter` from context.
2. Refuses scoped identities on the unscoped path
   (`services.ErrScopedIdentity`).
3. Dispatches `fromUser` to `authorizeLocalUser`,
   `authorizeRemoteUser`, `authorizeBuiltinRole`, or
   `authorizeRemoteBuiltinRole`.
4. `CheckIPPinning` (line 686) – validates `PinnedIP` against the
   observed client address; refuses port 0 (which indicates the
   connection passed through an unverified PROXY header).
5. Lock enforcement: `lockWatcher.CheckLockInForce` against
   `Context.LockTargets()`.
6. `enforcePrivateKeyPolicy` (line 490) – the certificate's
   `PrivateKeyPolicy` extension must satisfy the per-role +
   cluster-wide policy.
7. Device trust: `dtauthz.VerifyTLSUser` against
   `authPref.GetDeviceTrust()` (line 478).
8. `checkAdminActionVerification` (line 532) – if cluster has
   `IsAdminActionMFAEnforced()`, ensures the caller is either a bot
   (skipped), bot-impersonated, admin-impersonated host, or has an
   `mfa.CredentialsFromContext` MFA response with scope
   `CHALLENGE_SCOPE_ADMIN_ACTION`.

### 3.3 `Context.CheckAccess*` helpers

`Context` (`permissions.go:251`) exposes (lines 1691-1782):

- `CheckAccessToKind(kind, verb, ...)` – kind-level RBAC.
- `CheckAccessToResource(resource, verb, ...)` – instance-level.
- `CheckAccessToResource153` – for new `*.proto` Resource153 types.
- `CheckAccessToRule(ruleCtx, kind, verb, ...)` – rules engine.
- `AuthorizeAdminAction()` – fails if `AdminActionAuthState !=
  AdminActionAuthMFAVerified` (rejects MFA-reused too).
- `AuthorizeAdminActionAllowReusedMFA()` – relaxes that to allow
  reused MFA. See RFD `rfd/0131-adminitrative-actions-mfa.md` and
  `rfd/0155-scoped-webauthn-credentials.md`.

### 3.4 `services.AccessChecker` (`lib/services/access_checker.go`)

The interface (line 52) is the policy decision point used everywhere
downstream. `accessChecker` (line 334) embeds a `RoleSet` and an
`AccessInfo` (line 314, holds `ScopePin`, `Roles`, `Traits`,
`AllowedResourceAccessIDs`, `DelegationSessionID`, `Username`).

Core methods:

- `CheckAccess(r, state, matchers...)` (line 634) – calls into
  `validateAccessConditions` (line 651) → `checkAllowedResources` +
  `RoleSet.checkAccess` (`lib/services/role.go:2711`).
- `CheckAccessToRule(ctx, namespace, rule, verb)` →
  `RoleSet.CheckAccessToRule` (`role.go:3342`) → uses Vulcand
  predicate to evaluate `where`/`actions` clauses.
- `CheckKubeGroupsAndUsers`, `CheckDatabaseNamesAndUsers`,
  `CheckAWSRoleARNs`, `CheckAzureIdentities`,
  `CheckGCPServiceAccounts` – enumeration with TTL-clamping.
- `CheckImpersonate(currentUser, impersonateUser, roles)` and
  `CheckImpersonateRoles` – impersonation gate. Critical for the
  `generateUserCerts` path (§4).

`AccessInfo` is produced from:

- `services.AccessInfoFromUserState` for local users
  (`lib/services/access_checker.go`).
- `services.AccessInfoFromRemoteTLSIdentity` for remote users
  (uses `CombinedMapping()` from the cert authority).
- `services.AccessInfoFromUser` for direct user objects (legacy
  paths).

---

## 4. Identity Issuance

### 4.1 User certificate minting

The end-to-end flow:

1. Caller hits `GRPCServer.GenerateUserCerts`
   (`grpcserver.go:705`) which is forwarded to
   `ServerWithRoles.generateUserCerts`
   (`auth_with_roles.go:3721`).
2. The RBAC layer performs:
   - **Device trust check** (`verifyUserDeviceForCertIssuance`, line
     4266) – skipped for App and WindowsDesktop usages.
   - **MFA validation** if `req.MFAResponse != nil` – calls
     `ValidateMFAAuthResponse` with required extensions (scope =
     `CHALLENGE_SCOPE_USER_SESSION`, possibly reuse-allowed).
   - **Scoped guard** – scoped identities can only request
     Kubernetes certs (lines 3747-3754).
   - **Impersonation gates** (lines 3756-3797):
     - `canImpersonate` requires either built-in admin role or
       `Checker.CanImpersonateSomeone()`.
     - Recursive impersonation is forbidden ("Alice impersonating
       Bob, Bob impersonating Grace" is rejected). Impersonated
       certs cannot request new access requests, cannot request new
       roles, cannot impersonate anyone else.
   - **No-impersonate-SSO** – local users cannot have certs issued
     for SSO users (line 3831-3837).
   - **TTL clamping** (lines 3841-3890) – the produced cert TTL is
     clamped to `min(req.Expires, session expiry, role TTL,
     mfa_verification_interval)`. Renewable bots/role-impersonation
     can extend up to `defaults.MaxRenewableCertTTL`.
   - **Admin action MFA** (line 3897) – any cert issuance for
     someone other than self is an admin action.
3. The handler builds a `cert.Request`
   (`lib/auth/internal/cert/request.go`) and calls
   `Server.GenerateUserCerts` (`auth.go:3601`) →
   `generateCert(ctx, a, req, types.UserCA)` (`auth.go:3610`).
4. `generateCert` (lines 3610-4116):
   1. `req.Check()` plus a scopes-feature guard.
   2. Lock check via `verifyLocksForUserCerts` (line 3635). The
      lock check looks at `username`, `mfaVerified`,
      `activeAccessRequests`, `deviceID`, `botInstanceID`,
      `joinToken` – any matching lock fails the request.
   3. TTL adjustment: `certParams.AdjustSessionTTL` and
      `GetSSHLoginsForTTL`; SSH for scoped uses just `req.Login`.
   4. **Hardware key attestation** (`attestHardwareKey` line 4128):
      if a role or cluster auth pref requires
      `hardware_key`/`hardware_key_touch`/`hardware_key_touch_and_pin`,
      the SSH and TLS public keys must be attested as living on a
      YubiKey (or other PIV device). Both keys must produce the
      same attested policy. The optional
      `HardwareKeySerialNumberValidation` requires the serial to be
      in the user's allowlisted traits.
   5. Cluster routing: if `req.RouteToCluster != localCluster`, calls
      `CheckAccessToRemoteCluster` and returns
      `NotFound("remote cluster %q not found")` on access denied to
      avoid leaking existence (line 3791).
   6. Fetches the User CA via
      `GetCertAuthority(ctx, UserCA, true)`.
   7. Adds the special `teleport.SSHSessionJoinPrincipal`
      principal so any user can attempt to join a session (RBAC is
      checked separately on connection).
   8. IP pinning: if `certParams.PinSourceIP()` is on, the cert
      must include the client's `LoginIP`.
   9. Builds an `sshca.Identity` (line 3869) and signs via
      `a.GenerateUserCert(params)` – this calls into
      `lib/auth/keygen` ultimately invoking
      `ssh.NewCertificate(...).SignCert(rand, signer)`. The
      identity carries:
      - principals, roles, scope pin, traits
      - device extensions (`DeviceID`, `AssetTag`,
        `CredentialID`)
      - GitHub identity (UserID, Username) for git access
      - hardware-key policy
      - bot info (`BotName`, `BotInstanceID`)
      - join token name
      - access requests, allowed resource IDs / access IDs
      - `MFAVerified`, `PreviousIdentityExpires`,
        `LoginIP`, `PinnedIP`
      - `Renewable`, `Generation`, `DisallowReissue`
      - `HeadlessAuthenticationID`,
        `ConnectionDiagnosticID`, `JoinAttributes`
  10. Kubernetes cluster existence check (line 3915) – only for
      local-cluster routing.
  11. Enumerates allowed kube groups/users, db names/users, AWS
      roles, Azure identities, GCP service accounts via the
      AccessChecker. Each is TTL-clamped.
  12. Generates AWS Roles-Anywhere credentials for AWS app access
      via `createsession.CreateSession` if applicable.
  13. Builds `tlsca.Identity` (`auth.go:3987`) and signs it with
      `tlsca.CertAuthority.GenerateCertificate`
      (`lib/tlsca/ca.go:1619`). The `CertAuthority` was loaded via
      `a.keyStore.GetTLSCertAndSigner(ctx, ca)` so the actual
      signing call can be to an HSM/KMS.
  14. Optionally includes Host CA certs (`req.IncludeHostCA`).
  15. Emits `events.CertificateCreate` audit
      (`emitCertCreateEvent`, line 4274) and the usage report
      (`submitCertificateIssuedEvent`, line 3552).

### 4.2 Host certificate minting

`Server.GenerateHostCert` (`auth.go:2748`) → `generateHostCert`
(line 2783) builds a `sshca.HostCertificateRequest` (CASigner from
`keyStore.GetSSHSigner`, principals = `[hostID, nodeName, ...]`,
TTL = `defaults.CATTL`). `GRPCServer.GenerateHostCerts`
(`grpcserver.go:842`) is the bulk variant for `tbot`/agents that
also returns the TLS CA chain.

### 4.3 OpenSSH (agentless) certificates

`Server.GenerateOpenSSHCert` (`auth.go:2849`) signs with the
OpenSSH CA (a CA specifically for agentless OpenSSH nodes that
trust Teleport-signed certs). Routes through `generateOpenSSHCert`
(`auth.go:3606`) → `generateCert(.., OpenSSHCA)`. Scoped identities
use this path (kube-only enforcement is bypassed because OpenSSH
certs always carry a single principal).

### 4.4 The `AugmentUserCertificates` flow

`Server.AugmentContextUserCertificates` (`auth.go:3204`) takes
existing certs and re-signs them with **device extensions** so the
user's later requests prove device trust. The
`AugmentWebSessionCertificates` variant
(`auth.go:3251`) handles web sessions. This is the on-cert
representation of the `lib/devicetrust/authn` ceremony.

### 4.5 Embedded extensions

TLS cert extensions are encoded by
`tlsca.Identity.Subject()` (returns a `pkix.Name` with custom OIDs
under the Gravitational arc) and decoded by `tlsca.FromSubject`
(`lib/tlsca/ca.go:1157`). SSH cert extensions are written/read by
`sshca.Identity.Encode` / `DecodeIdentity`
(`lib/sshca/identity.go:168` / `:418`).

The `tlsca.Identity` struct (`ca.go:120`) is **the** thing every
downstream server inspects to decide what a request is allowed to
do. Note `IsBot()` (line 1559), `IsMFAVerified()` (line 1554), and
`IsDelegationSession()` (line 1564) as the most common predicates.

### 4.6 TTL rules summary

- Renewable bots: up to `defaults.MaxRenewableCertTTL`.
- Self-renewal: capped at the *current* session expiry (anti-eternal-
  renewal control, `auth_with_roles.go:3884-3889`).
- New issuance for non-self: capped at the role-derived session TTL.
- MFA-required-tap roles: capped at
  `RoleSet.AdjustMFAVerificationInterval(...)`.
- App / DB / Kube ephemeral certs (`isCertWrittenToDiskFlow`):
  derive from the smaller of `default_session_ttl` / role /
  `mfa_verification_interval` / requested expiry.
- Headless auth certs: hardcoded `time.Minute` TTL
  (`methods.go:867`).

---

## 5. MFA / WebAuthn

### 5.1 Devices and storage

`types.MFADevice` (api/types) and its persistence in
`lib/services/local/users.go` (`UpsertMFADevice` etc.). WebAuthn
local auth (the per-user random `WebauthnLocalAuth.UserID` used as
the WebAuthn user handle) lives at `users.go:971-1054`. SSO devices
(virtual MFA devices used for SSO MFA) are managed via
`lib/auth/sso_mfa.go`.

### 5.2 Challenge / response (gRPC)

The Auth Service exposes:

- `CreateAuthenticateChallenge` (`auth.go:4425`) – issue a
  per-scope challenge. Scopes are `CHALLENGE_SCOPE_LOGIN`,
  `CHALLENGE_SCOPE_PASSWORDLESS_LOGIN`,
  `CHALLENGE_SCOPE_USER_SESSION`,
  `CHALLENGE_SCOPE_ADMIN_ACTION`,
  `CHALLENGE_SCOPE_HEADLESS_LOGIN`, etc. The OSS path is rate
  limited via `createAuthenticateChallengeLimiter`
  (`grpcserver.go:240`).
- `CreateRegisterChallenge` (`auth.go:4575`) – for enrolling a new
  device.
- `ValidateMFAAuthResponse` – validates a TOTP, WebAuthn, or SSO
  response and returns `authz.MFAAuthData` (with `User`, `Device`,
  `AllowReuse`, `Payload`, `SourceCluster`, `TargetCluster`).
  Implemented in `lib/auth/mfa/*` package.

### 5.3 WebAuthn server flows

`lib/auth/webauthn/login.go`:

- `loginFlow.begin` (line 81) – issues a credential assertion. For
  passwordless / discoverable login uses `BeginDiscoverableLogin`;
  otherwise `BeginLogin(u, opts...)` with the user's existing
  devices. Filters out devices whose `CredentialRpId` doesn't match
  the configured RPID (logs an error – RPID changes are not
  supported). Sorts non-resident keys first so `tsh` picks them for
  MFA (instead of triggering UV).
- `loginFlow.finish` (line 255) – validates the response with
  `webauthn.FinishLogin` / `FinishDiscoverableLogin`, decodes the
  `ChallengeExtensions` stored alongside session data, enforces
  `Scope` and `AllowReuse` checks against the caller's
  `requiredExtensions`.

`lib/auth/webauthn/register.go::RegistrationFlow`:

- `Begin` (line 133) issues credential creation params, supports
  passwordless (sets `requireResidentKey`).
- `Finish` (line 269) parses the attestation, verifies it (with the
  configured `AttestationAllowedCAs` / `AttestationDeniedCAs`),
  records `hasCredPropsRK` for tracking actual resident-key support,
  and persists a new `types.MFADevice`.

`lib/auth/webauthn/login_passwordless.go::PasswordlessFlow` (line
48) is the resident-key-only variant used for initial passwordless
login.

U2F legacy compatibility is preserved through `appid` extension
handling (`login.go:163-168`) — see RFD
`rfd/0040-webauthn-support.md`.

### 5.4 SSO MFA

`lib/auth/sso_mfa.go::BeginSSOMFAChallenge` (line 39) creates an
SSO-backed MFA "challenge" by redirecting to the SAML/OIDC IdP and
then `VerifySSOMFASession` (line 98) validates the IdP callback as
MFA. The user-side connector is marked
`mfa_settings.enabled`. RFD `rfd/0180-sso-mfa.md`.

### 5.5 Browser MFA / in-band MFA

`lib/auth/browser_mfa.go` implements the browser-resident MFA
challenge initiated by `tsh` (RFD
`rfd/0233-tsh-browser-mfa.md`). `lib/auth/mfa/mfav2/service.go`
(`CreateSessionChallenge` line 170,
`ValidateSessionChallenge` line 339) implements in-band MFA for
SSH sessions (RFD `rfd/0234-in-band-mfa-ssh-sessions.md`),
including replication and challenge sharing across clusters
(`ReplicateValidatedMFAChallenge` line 460).

### 5.6 Headless auth

`Server.authenticateHeadless` (`methods.go:600`) implements the
headless flow where a `tsh ssh` on a headless machine creates a
`types.HeadlessAuthentication` record, the user opens a URL on a
trusted device, MFAs, and approves the request. Used heavily for
`tsh ssh` from devices without browsers (RFD
`rfd/0105-headless-authentication.md`,
`rfd/0149-headless-kube.md`).

### 5.7 Native (TOTP)

`lib/auth/password.go::checkPassword` (line 234) validates the OTP
token in concert with the password (line 235:
`checkPasswordWOToken` then `OTPSecret` validation).

---

## 6. SSO Connectors

### 6.1 OIDC (`lib/auth/oidc.go`)

The `OIDCService` interface (line 34) is satisfied by an enterprise
implementation injected via
`Server.SetOIDCService` (line 1579). The Auth Service forwards
`CreateOIDCAuthRequest`, `CreateOIDCAuthRequestForMFA`, and
`ValidateOIDCAuthCallback` to it (lines 130, 141, 152). Both the
connector storage (`UpsertOIDCConnector`, `CreateOIDCConnector`,
`UpdateOIDCConnector`, `DeleteOIDCConnector`) and validation are
RBAC-guarded in `ServerWithRoles`.

Connector storage adapter:
`lib/services/oidc.go` (`UnmarshalOIDCConnector`,
`GetRedirectURL`).

Auth request side-effects:
the per-request `ClientRedirectURL` is validated against the
connector's `ClientRedirectSettings` (see github.go below — same
helper in `lib/client/sso/redirector.go::ValidateClientRedirect`).

### 6.2 SAML (`lib/auth/saml.go`)

`SAMLService` interface (line 47). Identity comparison and connector
validation done at storage time via
`services.ValidateSAMLConnector` (line 66). The actual SAML signing
happens with `lib/services/saml.go::GetSAMLServiceProvider` (line
319) which builds a `saml2.SAMLServiceProvider` from the connector's
signing key, IdP cert, audience, ACS URL, etc.
`CheckSAMLEntityDescriptor` (line 279) parses metadata XML and
returns the IdP signing certificates that will be used to verify
assertions.

The SAML `ValidateSAMLResponse` is delegated to the enterprise
`samlAuthService` (saml.go:50,223). RFD `rfd/0073-idp-initiated-login.md`,
`rfd/0070-tctl-sso-configure-command.md`.

### 6.3 GitHub (`lib/auth/github.go`)

GitHub is unique because OSS supports it directly. Notable:

- `CreateGithubAuthRequest` (line 142) – validates the client
  redirect URL via
  `sso.ValidateClientRedirect(req.ClientRedirectURL, ceremonyType,
  connector.GetClientRedirectSettings())` (line 159). Builds
  `oauth2.Config` from the connector
  (`newGithubOAuth2Config` line 524) and stores
  `req.RedirectURL = config.AuthCodeURL(req.StateToken)`.
- `ValidateGithubAuthCallback` (line 433) →
  `validateGithubAuthCallbackHelper` (line 438) →
  `ValidateGithubAuthRedirect` (line 538). Fetches user + teams via
  GitHub API, calls `checkGithubOrgSSOSupport` (line 272) to
  enforce that OSS clusters can't use GitHub orgs that have
  external SSO enabled (an enterprise-gated feature).
- `makeGithubAuthResponse` (line 661) builds the final
  `authclient.GithubAuthResponse` and triggers cert issuance for
  the resulting user.

### 6.4 `ValidateClientRedirect` (security-critical)

`lib/client/sso/redirector.go:583`. Header comment is unambiguous:
*"this validation function is critical to SSO security and any
changes to it should be carefully considered from a vulnerability
point of view"*. Rules:

- Reject opaque URLs, userinfo, fragments.
- WebMFA redirect (`/web/sso_confirm`) only allowed for MFA flow
  and only as a relative path with a `channel_id` query param.
- Path must be `/callback` with a single `secret_key` query param.
- Scheme must be http or https.
- `http://localhost` (and `127.0.0.1`, `::1`) is always allowed.
- For test ceremonies, no custom redirect URLs are allowed.
- For non-localhost addresses, only hosts in
  `settings.InsecureAllowedCidrRanges` (CIDR match) or matching one
  of `settings.AllowedHttpsHostnames` patterns are allowed.
- HTTPS port must be empty or 443.

### 6.5 `claims_to_roles` / `traits_to_roles`

Mapping is done by `lib/services/traits.go::TraitsToRoles` (line
42) / `TraitsToRoleMatchers` (line 53). For each
`types.TraitMapping`, all SSO claim/trait values are compared either
literally or via a regex; matching ones produce mapped role names
(possibly with variable substitution).

`OIDCConnectorV3.GetClaimsToRoles` (`api/types/oidc.go:376`),
`SAMLConnectorV2.GetAttributesToRoles`, and
`GithubConnectorV3.GetTeamsToLogins` are all collected into the
same `TraitMappingSet` for evaluation.

---

## 7. Cryptographic Primitives (`lib/cryptosuites`)

`lib/cryptosuites/suites.go`:

- `KeyPurpose` (line 42) lists every key purpose in Teleport
  (UserCATLS, UserCASSH, HostCATLS, HostCASSH, DatabaseCATLS,
  DatabaseClientCATLS, OpenSSHCASSH, JWTCAJWT, OIDCIdPCAJWT,
  SAMLIdPCATLS, SPIFFECATLS, SPIFFECAJWT, OktaCAJWT,
  ProxyToDatabaseAgent, ProxyKubeClient, UserSSH, UserTLS,
  DatabaseClient/Server, HostSSH, HostIdentity,
  BotImpersonatedIdentity, BotSVID, EC2InstanceConnect,
  GitHubProxyCASSH, GitClient, AWSRACATLS, BoundKeypairJoining,
  BoundKeypairCAJWT, RecordingKeyWrapping, WindowsCARDP,
  AppClientCATLS).
- `Algorithm` (line 147): `RSA2048`, `RSA4096`, `ECDSAP256`,
  `Ed25519`.
- `suite` map: each suite assigns an algorithm to each purpose.
- Suites: `legacy`, `balancedV1`, `fipsv1`, `hsmv1` (lines
  192–344). FIPS replaces every Ed25519 with ECDSA P-256. HSM
  variant uses Ed25519 only for non-CA user/host SSH keys (because
  many HSMs support Ed25519 poorly).
- `GetCurrentSuiteFromAuthPreference` (line 363) reads
  `auth_pref.GetSignatureAlgorithmSuite()` to allow runtime suite
  changes.
- `AlgorithmForKey(ctx, getSuite, purpose)` (line 429) is the
  resolver everything calls.
- `GenerateUserSSHAndTLSKey(ctx, getSuite)` (line 444): if legacy
  suite is configured, generates a *single* RSA2048 key used for
  both; otherwise generates two keys with different algorithms.
- `GenerateKeyWithAlgorithm` (line 488) dispatches to
  `generateRSA2048`, `generateRSA4096`, `generateECDSAP256`,
  `generateEd25519` (which use crypto/rand). RSA generation is
  wrapped in `lib/cryptosuites/internal/rsa.GenerateKey` for FIPS
  build compatibility and prime-cache acceleration.

See RFD `rfd/0136-modern-signature-algorithms.md` for the
historical context and migration story.

### 7.1 FIPS

The `fips` flag rides on `Server.fips` (`auth.go:1399`), set from
`InitConfig.FIPS`. It is propagated to:

- Keystore (`KeystoreConfig` and PKCS#11/AWS-KMS configurations).
- `GenerateUserCerts` decisions (alg suite forced to `fipsv1` in
  FIPS-only builds via `modules.Modules.IsFIPSBuild()`).
- TLS handshake cipher suites.

The `IsBoringCrypto` plumbing exists for `boringcrypto` builds
(`lib/auth/auth.go:7785` reports `IsBoring` to the proxy ping
response).

### 7.2 HSM (PKCS#11) and KMS

`lib/auth/keystore/manager.go`:

- `Manager` (line 125) holds `backendForNewKeys` plus
  `usableBackends` (a list in preference order, software is always
  present as a fallback for keys this auth doesn't own).
- `NewManager` (line 233) chooses backend by config: PKCS#11 (HSMs
  via `github.com/ThalesIgnite/crypto11` + `github.com/miekg/pkcs11`),
  GCP KMS, or AWS KMS. Falls back to software.
- `backend` interface (line 142): `generateSigner`,
  `generateDecrypter`, `getSigner`, `getDecrypter`, `canUseKey`,
  `deleteKey`, `deleteUnusedKeys`, `findDecryptersByLabel`,
  `keyTypeDescription`, `name`.

`lib/auth/keystore/pkcs11.go::pkcs11KeyStore` (line 51) uses
`crypto11.Configure(...)` (line 69) plus raw `pkcs11.New(...)` for
querying token info. Keys are identified by `keyID` (line 466)
encoded as `pkcs11:<key-id>:<host-uuid>` so that each HSM-backed
auth server can detect which keys it owns (`canUseKey`,
`checkAccessibleHostID`).

`lib/auth/keystore/aws_kms.go::awsKMSKeystore` (line 70) generates
keys directly in KMS using `KeyUsageType` mapped from
`keyUsage`. Multi-region key support is implemented in
`applyMultiRegionConfig` (line 728).

`lib/auth/keystore/gcp_kms.go` provides the GCP KMS equivalent.

The **key migration story**: when a Teleport auth is started with a
new keystore backend (e.g. switching from software to HSM), the
existing CAs still have keys in the old backend. The auth will not
be able to sign with the existing CA keys, so
`initializeAuthorities` (`init.go:803`) detects this
(`HasUsableActiveKeys` returning `CAHasUsableKeys=false`). For the
Host CA only, `ensureLocalAdditionalKeys` (referenced at
`init.go:960`) writes new local keys into `AdditionalTrustedKeys`
so the local Admin identity can still be signed (special bootstrap
case for `tctl auth sign`). For other CAs, an alert is set asking
the operator to perform CA rotation.

`DeleteUnusedKeys` (`manager.go:907`) cleans up orphaned HSM keys
after rotation but is best-effort (see comment at
`init.go:855-862`).

RFD `rfd/0025-hsm.md`, `rfd/0210-windows-ad-hsm.md`.

### 7.3 JWT (`lib/jwt`)

`lib/jwt/jwt.go::Key` (line 85) wraps a `Config` with a `Clock`,
`PublicKey`, `PrivateKey`, and `ClusterName`. Signing uses
`github.com/go-jose/go-jose/v3` with
`jose.SignatureAlgorithm` derived from public key type
(`AlgorithmForPublicKey` line 183):

- RSA → `RS256`
- ECDSA → `ES256`
- Ed25519 → `EdDSA`

PKCS#11/KMS-resident signers are wrapped via
`cryptosigner.Opaque` (line ~200) so go-jose can use a crypto.Signer
without exposing the raw key.

`SignAWSOIDC`, `SignEntraOIDC`, `SignSnowflake`,
`SignAzureToken`, `SignJWTSVID`, `SignPROXYJWT`,
`SignDBSCChallenge`/`VerifyDBSCChallenge` cover each downstream use
case (lines 259-502).

`lib/jwt/jwk.go` builds JWKs/JWKS for the OIDC IdP, SAML IdP,
SPIFFE JWT SVIDs, and Okta CA endpoints.

OIDC token validation done downstream:
`lib/oidc/token_validator.go` (cached version
`caching_token_validator.go`) for join-method OIDC tokens (GitHub
Actions, GCP, Spacelift, etc.).

---

## 8. Certificate Authorities

### 8.1 CA types

`api/types/trust.go`:

| Type | Value | Purpose |
|------|-------|---------|
| `HostCA` | `"host"` | Issues host x509 + SSH certs for Teleport instances. |
| `UserCA` | `"user"` | Issues user x509 + SSH certs. |
| `DatabaseCA` | `"db"` | Server CA for self-hosted DBs. |
| `DatabaseClientCA` | `"db_client"` | Client CA for DBs. |
| `OpenSSHCA` | `"openssh"` | Signs certs for agentless OpenSSH targets. |
| `JWTSigner` | `"jwt"` | JWTs for app access. |
| `SAMLIDPCA` | `"saml_idp"` | SAML IdP signing key. |
| `OIDCIdPCA` | `"oidc_idp"` | OIDC IdP signing key. |
| `SPIFFECA` | `"spiffe"` | SPIFFE workload identity. |
| `OktaCA` | `"okta"` | Okta integration JWT signer. |
| `AWSRACA` | `"awsra"` | AWS IAM Roles Anywhere CA. |
| `BoundKeypairCA` | `"bound_keypair"` | Signs bound-keypair client state docs. |
| `WindowsCA` | `"windows"` | RDP certs for Windows Desktop Access. |
| `AppClientCA` | `"app_client"` | App access client certs. |

`CertAuthTypes` (line 89) is the canonical list. `addedInMajorVer`
(line 119) drives `NewlyAdded()`, used by trusted-cluster
exchange to tolerate older peers not knowing about new CA types.

### 8.2 `tlsca.CertAuthority`

`lib/tlsca/ca.go:111` – a wrapper around `*x509.Certificate +
crypto.Signer`. Generated x509 certs (`GenerateCertificate` line
1619) always set:

- 128-bit random serial number.
- `NotBefore = clock.Now() - 1m` (skew tolerance).
- `KeyUsage` per caller, plus `ExtKeyUsage = {ServerAuth,
  ClientAuth}` for all certs (because Teleport identities can
  authenticate in either direction).
- `BasicConstraintsValid: true, IsCA: false` – never sign
  intermediates.
- `IPAddresses` vs `DNSNames` split correctly.
- `ExtraExtensions` carry Teleport-specific OIDs (encoded by
  `tlsca.Identity.Subject()`).

`shouldPersistJoinAttrs` (line 1673) is a kill-switch env var
(`TELEPORT_UNSTABLE_DISABLE_JOIN_ATTRS=yes`) for the new
join-attribute embedding behavior.

### 8.3 `sshca` (`lib/sshca`)

`Authority` interface (`lib/sshca/sshca.go:34`) is implemented by
`lib/auth/keygen` and embedded into `Server`
(`auth.go:1300`). `HostCertificateRequest` and
`UserCertificateRequest` define the inputs. The `Identity`
(`lib/sshca/identity.go:44`) is the SSH-side mirror of
`tlsca.Identity` and is encoded/decoded into the certificate's
`Extensions` and `CriticalOptions` maps.

### 8.4 `subca` (`lib/subca`)

`lib/subca/parsed.go`, `lib/subca/certificate.go`,
`lib/subca/feature.go` – implements *CA override*. A cluster can
plug in an externally minted intermediate CA cert + key for things
like SPIFFE / Workload Identity to delegate signing while keeping
the root key offline. `ca_override_resolver.go` (line 1) loads the
override from a `WorkloadIdentityX509CAOverride` resource and
substitutes it for the in-cluster CA at signing time. The
`workloadIdentityX509CAOverrideGetter` is plugged into `Server` at
`auth.go:1380`.

### 8.5 CA rotation (`lib/auth/rotate.go`)

The rotation state machine has phases (`rotate.go:42` comments,
also `api/types/types.proto` `RotationPhase*`):

1. **Standby** – no rotation in progress.
2. **Init** – new CA generated and trusted; old CA still issues.
3. **UpdateClients** – internal clients reconnect and pick up new
   client credentials; servers still serve old.
4. **UpdateServers** – servers swap to the new credentials; old CA
   still trusted (allowing rollback).
5. **Rollback** – revert to old CA (terminal before going back to
   Standby).
6. **Standby (complete)** – old CA fully removed and untrusted.

`RotateCertAuthority` (line 131) is the manual driver;
`AutoRotateCertAuthorities` (line 184) is run periodically and
advances phases on the schedule. `processRotationRequest` (line
284) generates the new key set via `keystore.NewSSHKeyPair`,
`NewTLSKeyPair`, `NewJWTKeyPair` for the rotated CA type. After
rotation completes, unused keys are deleted via
`keystore.DeleteUnusedKeys`.

### 8.6 Trusted clusters

`lib/auth/trustedcluster.go::Server.UpsertTrustedCluster` and
related code share the local cluster's Host CA, User CA, and other
CAs with a peer cluster. Remote CAs are stored in the local backend
with `DomainName != localCluster`. Authorization for remote users
goes through `authorizer.authorizeRemoteUser`
(`permissions.go:740`), which uses `ca.CombinedMapping()` to map
remote roles to local roles per the cluster's `role_map`.

### 8.7 WindowsCA / RDP CRLs

`lib/winpki/certificate_authority.go::CertificateStoreClient` (line
42) and `lib/winpki/ldap.go::LDAPClient` (line 108) handle pushing
CRLs into Active Directory's `CDP` container so domain-joined
Windows hosts trust Teleport's RDP certs. `init_windows_ca.go` /
`init_windows_ca_test.go` initialize the cloned UserCA→WindowsCA on
upgrade (`init.go:729` comment: *"Clone UserCA into WindowsCA. Must
happen before initializeAuthorities()"*). RFD
`rfd/0239-windows-ca-split.md`.

---

## 9. Device Trust (`lib/devicetrust`, `lib/devicetpm`)

### 9.1 Server architecture

The device service interfaces live in
`lib/devicetrust/assertserver/assert.go` (`Ceremony` interface line
41) and `lib/devicetrust/config/`. Enterprise plugs the real
implementation in via
`Server.SetDeviceAssertionServer`
(`auth.go:1768`) and `SetCreateDeviceWebTokenFunc`
(`auth.go:1801`). OSS returns `nil` / a function that errors.

### 9.2 Enrollment ceremony

`lib/devicetrust/enroll/enroll.go::Ceremony` (line 38):

- `RunAdmin` (line 81) – admin enrolls a device with their
  identity, used for the initial onboarding.
- `Run` (line 174) – user-facing enrollment using a previously
  issued `enroll_token`.
- `enrollDeviceMacOS` (line 242) – macOS specific (Secure Enclave
  key).
- `enrollDeviceTPM` (line 267) – Linux/Windows TPM 2.0 path.

The ceremony is a gRPC bidirectional stream:
`init → challenge → response → success`. macOS signs with a
Secure Enclave key (P-256); TPM signs an AK-attested challenge using
go-attestation primitives wrapped in `lib/devicetpm/attest.go` (the
package exists to consolidate go-attestation usage in one place).

### 9.3 Authentication ceremony

`lib/devicetrust/authn/authn.go::Ceremony` (line 39):

- `Run` (line 68) – returns augmented user certs (with device
  extensions).
- `RunWeb` (line 105) – exchanges a `DeviceWebToken` (issued by
  the cluster on web login) for a `DeviceConfirmationToken` that
  the browser POSTs to `/webapi/device/webconfirm`.
- The `run` method (line 134) gets a `DeviceCredential` via
  `native.GetDeviceCredential`, collects device data via
  `native.CollectDeviceData`, dispatches to
  `authenticateDeviceMacOS` (line 198) or
  `authenticateDeviceTPM` (line 235).

### 9.4 Device-aware roles

`tlsca.Identity.DeviceExtensions` (`lib/tlsca/ca.go:334`):
`DeviceID`, `AssetTag`, `CredentialID`. These are written into
the cert during `generateCert` (line 4043) and read by
`dtauthz.IsTLSDeviceVerified` to set `AccessState.DeviceVerified`
(`permissions.go:388`). `RoleOption.DeviceTrustMode = required`
forces `DeviceVerified=true`.

### 9.5 Device web session

`Server.createDeviceWebToken` (`auth.go:1808`) creates a one-shot
token for the device assertion. The browser is then expected to run
`Ceremony.RunWeb` against the cluster to swap the token for a
device-bound web session. The device-confirmed web session has its
`tlsca.Identity` augmented (via `AugmentWebSessionCertificates`,
`auth.go:3251`) with the proper device extensions.

---

## 10. Join Flow (`lib/join`)

### 10.1 Modern flow

`lib/join/server.go::Server.Join` (line 238) implements a stream
protocol over a bidirectional gRPC stream (`messages.ServerStream`).
Sequence:

1. Client sends `ClientInit` with token name and (optional)
   `JoinMethod`. Server records diagnostic info.
2. `authenticate` (line 377) – allow already-joined nodes to
   rejoin without re-presenting the token by reading the existing
   identity from context. Empty / AccessDenied is acceptable.
3. `getProvisionToken` (line 150) tries both **scoped** tokens
   (via `ScopedTokenService.GetScopedToken`) and **classic** tokens
   (via `AuthService.ValidateToken`) concurrently. Ambiguous name
   (matches both) is denied. RFD `rfd/0229a-scoped-join-tokens.md`.
4. `checkJoinMethod` (line 462) ensures requested method matches
   token-configured method. `TokenAllowsRole` (line 478) ensures
   the requested system role is in `token.GetRoles()`.
5. Sends `ServerInit` with the cluster's signature algorithm suite
   so the client knows how to generate its keypair.
6. `handleJoinMethod` (line 330) dispatches to one of the
   per-method handlers:
   - `handleIAMJoin` (`server_iam.go:44`) – sts:GetCallerIdentity
     challenge.
   - `handleOIDCJoin` (`server.go::handleOIDCJoin`) – generic for
     CircleCI, GitHub Actions, GitLab, GCP, Azure DevOps, Spacelift,
     Bitbucket, Terraform Cloud, env0, Kubernetes. Each
     `validate*Token` function (in `server_*.go`) is passed in.
   - `handleEC2Join` (`server_ec2.go`) – validates an EC2 instance
     identity document signed by AWS.
   - `handleAzureJoin` (`server_azure.go`) – Azure attested IMDS
     token.
   - `handleTPMJoin` (`server_tpm.go:46`) – TPM attestation via
     `lib/tpm`.
   - `handleOracleJoin` (`server_oracle.go`) – Oracle Cloud Infra
     instance principals (see RFD `rfd/0231-new-oracle-join-method.md`).
   - `handleBoundKeypairJoin` (`server_boundkeypair.go`) – see §10.2.
   - `handleTokenJoin` (`server_token.go`) – plain secret token.
7. Each handler returns a `messages.Result` containing certs (host
   or bot). Cert generation funnels into
   `lib/auth/join.go::GenerateHostCertsForJoin` / `GenerateBotCertsForJoin`.

### 10.2 Bound keypair (`lib/boundkeypair`, `lib/join/boundkeypair`)

A long-lived join method where the joining entity holds a stable
keypair whose public key was pre-registered on the cluster. On
re-join, the entity proves possession by signing a server-issued
challenge (a JWT containing nonce + token-name) with the keypair;
the server validates with the registered public key. After
successful join, the cluster issues a *join state document* (a JWT
signed by the `BoundKeypairCA`) that the entity must hold and
present on subsequent renewals. This binds the chain of renewals
together, allowing detection of compromised keys (a stolen keypair
re-joining will fail the join-state check because the legitimate
entity has a newer document).

- `lib/boundkeypair/bound_keypair.go::ChallengeValidator` (line
  106) issues and validates challenges.
- `lib/boundkeypair/join_state.go` manages the join-state document.
- `lib/boundkeypair/claims.go` defines JWT claims structure.
- `lib/join/boundkeypair/` is the join-side adapter.
- `lib/auth/bound_keypair_tokens.go` glues the cluster-side
  validator to provision tokens.

RFD list: `rfd/0162-machine-id-token-join-method-bot-instance.md`
covers bot-instance tracking, and other RFDs cover bound-keypair
specifically (see directory listing in
`rfd/0162-...`).

### 10.3 Legacy join path

`lib/auth/join.go::Server.RegisterUsingToken` (line 185) is the
older HTTP register path now wrapped by the streaming
`lib/join/server.go::Server.Join`. Many older methods are
implemented in `lib/auth/join_*.go` (still present, used for
backward compat).

### 10.4 Per-method validators

Each method has its own package:

- `lib/join/iamjoin/iam.go` – AWS STS pre-signed
  `sts:GetCallerIdentity` request validation with challenge.
- `lib/join/ec2join/ec2.go` – validates the AWS EC2 Instance
  Identity Document signed by Amazon's RSA root.
- `lib/join/githubactions/*` – validates GHA OIDC tokens against
  `https://token.actions.githubusercontent.com`'s JWKS.
- `lib/join/gcp/*` – GCP metadata-service ID tokens.
- `lib/join/azurejoin`, `lib/join/azuredevops/*` – Azure attested
  data + Azure DevOps OIDC.
- `lib/join/circleci/*`, `lib/join/spacelift/*`,
  `lib/join/terraformcloud/*`, `lib/join/bitbucket/*`,
  `lib/join/gitlab/*`, `lib/join/env0/*` – OIDC-based.
- `lib/join/tpmjoin/*` – TPM Endorsement Key validation against
  configured EK CAs.
- `lib/join/oraclejoin/*` – OCI instance principal validation.
- `lib/join/boundkeypair/*` – see above.

RFDs: `rfd/0050-join-methods.md` (overview),
`rfd/0041-aws-node-join.md`, `rfd/0094-kubernetes-node-joining.md`,
`rfd/0102-azure-node-join.md`, `rfd/0166-tpm-joining.md`,
`rfd/0079-oidc-joining.md`, `rfd/0143-external-k8s-joining.md`,
`rfd/0192-oci-join.md`, `rfd/0205-improved-onprem-joining.md`,
`rfd/0211-azure-devops-joining.md`.

---

## 11. Security-Critical Invariants

This is a non-exhaustive list of foot-guns and "DO NOT" comments
worth documenting prominently:

### 11.1 Explicit security comments

- **`lib/client/sso/redirector.go:584`** – `ValidateClientRedirect`
  header: *"this validation function is critical to SSO security
  and any changes to it should be carefully considered from a
  vulnerability point of view."* Any change to allowed
  schemes/paths/query-params here is a potential token-exfiltration
  bug.
- **`lib/authz/middleware.go:189-201`** – the local auth server
  refuses to trust system roles in remote certificates. This is
  *the* trust boundary between clusters in a trusted-cluster mesh.
- **`lib/auth/auth.go:7375`** – `GenerateCertAuthorityCRL` warns
  *"This is not safe for use in clusters using HSMs or KMS for
  private key material. Instead, you should prefer using the CRLs
  that are already present in the certificate_authority resource."*
- **`lib/auth/auth_with_roles.go:3770-3797`** – recursive
  impersonation prevention. Impersonated identities cannot
  impersonate anyone (including themselves), cannot request roles,
  cannot request access requests, and `DisallowReissue` is set on
  role-impersonated certs unless the caller explicitly opts in via
  `ReissuableRoleImpersonation` (line 4168).
- **`lib/auth/auth_with_roles.go:3831-3837`** – local users cannot
  issue certs for SSO users (would allow trivial SSO-account
  takeover).
- **`lib/auth/methods.go:528-536`** – when the user sends a
  password-only credential, the server explicitly checks that
  `auth_pref.IsSecondFactorEnforced()` is false AND that the user
  has no registered MFA devices. The comments say *"MFA bypass
  attempt, access denied"* — without these checks a user could
  bypass MFA by simply omitting it.
- **`lib/auth/auth_with_roles.go:4135-4136`** – `LoginIP` is
  always copied from current identity to new identity, *"to avoid
  generateUserCerts() being used to drop IP pinning in the new
  certificates."*
- **`lib/authz/permissions.go:722-728`** – IP pinning is rejected
  when port is `0` (PROXY-protocol-modified connection) because
  the client IP can't be trusted.

### 11.2 `Unstable` / environment kill-switches

Several `TELEPORT_UNSTABLE_*` env vars influence security:

- `TELEPORT_UNSTABLE_DISABLE_MFA_ADMIN_ACTIONS=yes`
  (`permissions.go:569`) – disables admin-action MFA enforcement.
  Comment marks it as a temporary escape hatch ("TODO(Joerger)
  once we have fully transitioned... this env var should be
  removed"). **This is a documented foot-gun.**
- `TELEPORT_UNSTABLE_DISABLE_UNQUALIFIED_LOOKUPS=yes`
  (`auth_with_roles.go:1895`) – disables fuzzy/unqualified lookups
  in resource listing.
- `TELEPORT_UNSTABLE_ALLOW_OLD_CLIENTS=yes` (`middleware.go:167`)
  – disables the min-version client check.
- `TELEPORT_UNSTABLE_DISABLE_JOIN_ATTRS=yes` (`tlsca/ca.go:1673`)
  – stops persisting join attributes into x509 identities.
- `TELEPORT_UNSTABLE_SCOPED_KMS_KEY_DELETION=yes`
  (`keystore/aws_kms.go:548`).
- `TELEPORT_UNSTABLE_SKIP_VERSION_UPGRADE_CHECK=yes`
  (`auth/version.go:91`).
- `TELEPORT_UNSTABLE_VC_SYNC_ON_START=yes`,
  `TELEPORT_UNSTABLE_VC_VERSION=...` (`auth.go:1895, 2283`).
- `TELEPORT_UNSTABLE_KUBE_UPGRADE_SCHEDULE`,
  `TELEPORT_UNSTABLE_SYSTEMD_UPGRADE_SCHEDULE`.

### 11.3 No `InsecureSkipVerify` in the certificate path

A grep of `InsecureSkipVerify` across `lib/auth/`, `lib/authz/`,
`lib/cryptosuites/`, and `lib/tlsca/` returns no production
results (only test fixtures). The `insecureMode` flag on `Server`
(`auth.go:1286`) is set from `InitConfig.InsecureMode` and
controls *insecure listeners* during development only.

### 11.4 cgo / unsafe pointers

`lib/auth/touchid/api_darwin.go` (CGO to Touch ID) and
`lib/auth/webauthnwin/zsyscall_windows.go` (CGO-like syscalls to
Windows WebAuthN APIs) are the only places `unsafe.Pointer` appears
in this subsystem. They're necessary boundaries to the platform
crypto APIs.

### 11.5 Bot self-impersonation

Bot identities (`identity.IsBot()`) are exempt from admin-action
MFA (`permissions.go:585-588`) and from admin-action MFA when the
impersonator is a bot or the Admin role
(`permissions.go:590-609`). This is intentional but means a
compromised bot identity gets blanket admin-action access until it's
locked.

### 11.6 Generation counter & renewal bombs

Bot renewable certs include a `Generation` counter
(`tlsca.Identity.Generation`,
`auth.go:3833-3835/3887-3890`). `updateBotInstance` in
`bot_instance.go` enforces strict monotonicity — if the client
re-presents a generation that doesn't match the server's record,
the bot identity is locked. This is the primary detection for
stolen bot credentials.

### 11.7 TODO/FIXME with security relevance

- `auth_with_roles.go:7943` – *"TODO(sshah): remove MFAVerified
  once the Web UI supports..."* – web UI quirk worth tracking.
- `auth.go:7907` – *"TODO (eriktate/scopes): implement scoped
  MFA"* – scoped MFA is incomplete; meanwhile MFA on scoped
  identities is partially gated by other restrictions.
- `auth_with_roles.go:3750` – *"TODO (eriktate/scopes): Remove this
  restriction once we have more thorough support for scopes with
  other usages."* – scoped certs are currently kube-only out of
  caution.
- `webauthn/login.go:162` – *"TODO(codingllama): Use the 'official'
  appid impl by duo-labs/webauthn."* – U2F appid extension is
  manually injected as the duo-labs library doesn't yet support it.

---

## 12. AI-Pointer Index

Cross-reference: concept → entry-point file:line.

### Authentication

- Local password+MFA login → `lib/auth/methods.go:382` (`authenticateUserInternal`).
- Passwordless login → `lib/auth/methods.go:568` (`authenticatePasswordless`).
- Headless login (`tsh ssh` w/o browser) → `lib/auth/methods.go:600` (`authenticateHeadless`).
- Web session creation → `lib/auth/methods.go:718` (`AuthenticateWebUser`) → `lib/auth/sessions.go::CreateWebSessionFromReq`.
- CLI (tsh) login → `lib/auth/methods.go:785` (`AuthenticateSSHUser`).
- Password-only validation w/ optional OTP → `lib/auth/password.go:234` (`checkPassword`).
- Login hooks → `lib/auth/auth.go:1641,1650` (Register/Call).
- Login rules engine → `lib/loginrule/evaluator.go`.

### Authorization

- TLS identity → context bridge → `lib/authz/middleware.go:136` (`GetUser`).
- Per-request authz → `lib/authz/permissions.go:427` (`authorizer.Authorize`).
- AccessChecker interface → `lib/services/access_checker.go:52`.
- RoleSet.checkAccess → `lib/services/role.go:2711`.
- Rule-based access (RBAC verbs) → `lib/services/role.go:3433` (`checkAccessToRuleImpl`).
- Lock enforcement → `lib/authz/permissions.go:465` and
  `lib/services/local/access.go::AccessService.GetLocks` (line 304).
- IP pinning → `lib/authz/permissions.go:686` (`CheckIPPinning`).
- Admin action MFA → `lib/authz/permissions.go:614` (`authorizeAdminAction`).
- Trait → role mapping → `lib/services/traits.go:42` (`TraitsToRoles`).
- Trait template interpolation → `lib/services/role.go:494` (`ApplyTraits`) → `:728` (`ApplyValueTraitsWithContext`).
- Remote user role mapping → `lib/authz/permissions.go:740` (`authorizeRemoteUser`).
- Scoped access checker → `lib/services/scoped_access_checker.go`.

### Certificate issuance

- User cert issuance (RBAC layer) → `lib/auth/auth_with_roles.go:3721` (`generateUserCerts`).
- User cert issuance (core) → `lib/auth/auth.go:3601` (`GenerateUserCerts`) → `:3610` (`generateCert`).
- Host cert issuance → `lib/auth/auth.go:2748` (`GenerateHostCert`) → `:2783` (`generateHostCert`).
- Bulk host certs (for agents/tbot) → `lib/auth/grpcserver.go:842` (`GenerateHostCerts`).
- OpenSSH cert issuance → `lib/auth/auth.go:2849` (`GenerateOpenSSHCert`).
- Bot cert renewal → `lib/auth/bot.go:669` (`GenerateUserCerts` via bot path).
- Device extension augmentation → `lib/auth/auth.go:3204` (`AugmentContextUserCertificates`).
- TLS signing (lower-level) → `lib/tlsca/ca.go:1619` (`GenerateCertificate`).
- SSH signing → `lib/sshca/sshca.go:34` (Authority interface), implemented in `lib/auth/keygen`.
- Cert TTL clamping → `lib/auth/auth_with_roles.go:3841-3890` and `lib/services/role.go:1395` (`AdjustSessionTTL`).
- Hardware-key attestation → `lib/auth/auth.go:4128` (`attestHardwareKey`).
- Impersonation gates → `lib/auth/auth_with_roles.go:3756-3797`.
- Cert lock check → `lib/auth/auth.go:4216` (`verifyLocksForUserCerts`).

### MFA / WebAuthn

- Create authn challenge (RPC) → `lib/auth/auth.go:4425` (`CreateAuthenticateChallenge`).
- Create register challenge (RPC) → `lib/auth/auth.go:4575` (`CreateRegisterChallenge`).
- WebAuthn login begin/finish → `lib/auth/webauthn/login.go:81,255`.
- WebAuthn registration begin/finish → `lib/auth/webauthn/register.go:133,269`.
- Passwordless flow → `lib/auth/webauthn/login_passwordless.go:48`.
- SSO MFA → `lib/auth/sso_mfa.go:39,98`.
- In-band SSH MFA (mfav2) → `lib/auth/mfa/mfav2/service.go:170,339`.
- Browser MFA → `lib/auth/browser_mfa.go`.
- TOTP secret generation → `lib/auth/auth.go` (`createTOTPPrivilegeToken` line 4633).
- Touch ID (macOS) → `lib/auth/touchid/`.
- Windows WebAuthN → `lib/auth/webauthnwin/`.

### SSO Connectors

- OIDC service interface → `lib/auth/oidc.go:34`.
- OIDC connector CRUD → `lib/auth/oidc.go:43-130`.
- OIDC callback validation → `lib/auth/oidc.go:152` (delegated to enterprise impl).
- SAML service interface → `lib/auth/saml.go:47`.
- SAML connector CRUD → `lib/auth/saml.go:54-201`.
- SAML response validation → `lib/auth/saml.go:223` (delegated).
- SAML SP construction → `lib/services/saml.go:319` (`GetSAMLServiceProvider`).
- GitHub auth flow → `lib/auth/github.go:142,433,538,661`.
- Org-SSO enforcement → `lib/auth/github.go:272` (`checkGithubOrgSSOSupport`).
- Client redirect URL validation → `lib/client/sso/redirector.go:583` (`ValidateClientRedirect`).

### Cryptography

- Key purposes & suites → `lib/cryptosuites/suites.go:42,182`.
- Get current suite → `lib/cryptosuites/suites.go:363` (`GetCurrentSuiteFromAuthPreference`).
- Generate key by purpose → `lib/cryptosuites/suites.go:479` (`GenerateKey`).
- Generate paired SSH+TLS key for users → `lib/cryptosuites/suites.go:444`.
- JWT signer → `lib/jwt/jwt.go:85` (`Key`).
- JWKS generation → `lib/jwt/jwk.go`.
- OIDC token validation (for joins/integrations) → `lib/oidc/token_validator.go`.

### CA / Keystore

- Keystore manager → `lib/auth/keystore/manager.go:125,233`.
- PKCS#11 backend → `lib/auth/keystore/pkcs11.go:51`.
- AWS KMS backend → `lib/auth/keystore/aws_kms.go:70`.
- GCP KMS backend → `lib/auth/keystore/gcp_kms.go`.
- Software backend → `lib/auth/keystore/software.go`.
- CA initialization → `lib/auth/init.go:803` (`initializeAuthorities`), `:866` (`initializeAuthority`).
- CA generation (first start) → `lib/auth/init.go:1080` (`generateAuthority`).
- CA rotation state machine → `lib/auth/rotate.go:131` (`RotateCertAuthority`), `:184` (`AutoRotateCertAuthorities`).
- CA override (workload identity) → `lib/subca/ca_override_resolver.go`.
- CA types enum → `api/types/trust.go:36-86`.
- Windows AD CRL push → `lib/winpki/certificate_authority.go:90`.
- LDAP client (winpki) → `lib/winpki/ldap.go:108`.

### Device trust

- Device authn ceremony → `lib/devicetrust/authn/authn.go:68,105,134`.
- Device enrollment ceremony → `lib/devicetrust/enroll/enroll.go:81,174`.
- TPM attestation primitives → `lib/devicetpm/attest.go:22`.
- Device trust types → `api/types/devicetrust/*` (proto-generated).
- Device extensions in cert → `lib/tlsca/ca.go:334`.
- Device assertion server interface → `lib/devicetrust/assertserver/assert.go:41`.
- Plugged into auth → `lib/auth/auth.go:1768` (`SetDeviceAssertionServer`).
- Web token issuance → `lib/auth/auth.go:1808` (`createDeviceWebToken`).

### Join methods

- Join server → `lib/join/server.go:238` (`Join`).
- Join method dispatch → `lib/join/server.go:330` (`handleJoinMethod`).
- IAM → `lib/join/server_iam.go:44`, validator `lib/join/iamjoin/iam.go`.
- EC2 → `lib/join/server_ec2.go`, validator `lib/join/ec2join/ec2.go`.
- Azure → `lib/join/server_azure.go`, validator `lib/join/azurejoin`.
- TPM → `lib/join/server_tpm.go:46`, validator `lib/join/tpmjoin/`.
- Oracle → `lib/join/server_oracle.go`, validator `lib/join/oraclejoin/`.
- Bound keypair → `lib/join/server_boundkeypair.go`, validator `lib/join/boundkeypair`, core `lib/boundkeypair/bound_keypair.go:106`.
- OIDC family (GHA, GitLab, etc.) → `lib/join/server.go::handleOIDCJoin` + per-method `validate*Token`.
- Provision token types → `api/types/provisioning.go:134` and `:36-93` (JoinMethod consts).
- Token storage → `lib/services/local/access.go` (provisioning tokens land in the access service for tokens) and `lib/services/local/provisioning.go`.
- Scoped tokens → `lib/scopes/joining/`.
- Legacy join (HTTP) → `lib/auth/join.go:185` (`RegisterUsingToken`).

### Decision Service (PDP, in-progress)

- Decision evaluator → `lib/decision/service.go:108` (`EvaluateSSHAccess`).
- gRPC service → `lib/decision/decisionv1/decision_service.go:65-99`.
- SSH identity ↔ proto → `lib/decision/ssh_identity.go`.
- TLS identity ↔ proto → `lib/decision/tls_identity.go`.
- Identity generation (for dry-run) → `lib/decision/identity_generation.go`.

### Bound Keypair / Machine ID

- Bound keypair challenge → `lib/boundkeypair/bound_keypair.go:106`.
- Join state document → `lib/boundkeypair/join_state.go`.
- Bot user CRUD → `lib/auth/bot.go`.
- Bot instance tracking → `lib/services/local/bot_instance.go`.
- Workload identity (SPIFFE) gRPC → `lib/auth/machineid/workloadidentityv1/`.

### Scopes

- Strong validation → `lib/scopes/scopes.go:77`.
- Scoped role access checker → `lib/scopes/access/access.go`.
- Scoped pinning → `api/gen/proto/.../scopes/v1` and
  `lib/auth/methods.go:62` (`AccessCheckerForScope`).
- Scoped joining → `lib/scopes/joining/`.

### Secret management / scanning

- Secret sealing (AES-GCM) → `lib/secret/secret.go` (`NewKey` line 45, `Seal` line 66, `Open` line 95).
- Secret scanner → `lib/secretsscanner/scanner/`, reporter
  `lib/secretsscanner/reporter/`, proxy `lib/secretsscanner/proxy/`.
- Authorized keys watcher (host-side detection) →
  `lib/secretsscanner/authorizedkeys/`.

### Hardware key (PIV)

- Hardware key service factory → `lib/hardwarekey/service.go:32`
  (`NewService`).
- Hardware key agent → `lib/hardwarekey/agent/` (RFD
  `rfd/0199-hardware-key-agent.md`).
- Per-cert attestation in auth → `lib/auth/auth.go:4128`
  (`attestHardwareKey`).

### Services / storage adapters

- AccessService (roles, locks) → `lib/services/local/access.go:46`.
- IdentityService (users, MFA, web sessions) →
  `lib/services/local/users.go:66`.
- Local user MFA device CRUD → `lib/services/local/users.go:971`
  (`UpsertWebauthnLocalAuth`) and surrounding code.

---

## Appendix A — Relevant RFDs (in this repo's `rfd/`)

- `0005-kubernetes-service.md`
- `0007-rbac-oss.md`
- `0009-locking.md`
- `0014-session-2FA.md`, `0015-2fa-management.md`
- `0025-hsm.md`
- `0033-desktop-access.md`, `0034-desktop-access-windows.md`,
  `0035-desktop-access-windows-authn.md`
- `0040-webauthn-support.md`
- `0041-aws-node-join.md`
- `0050-join-methods.md`
- `0052-passwordless.md`, `0053-passwordless-fido2.md`,
  `0054-passwordless-macos.md`, `0088-passwordless-windows.md`
- `0064-bot-for-cert-renewals.md`
- `0066-ip-based-validation.md`
- `0070-tctl-sso-configure-command.md`, `0071-tctl-sso-test-command.md`
- `0073-idp-initiated-login.md`
- `0078-login-rules.md`
- `0079-oidc-joining.md`
- `0080-hardware-key-support.md`
- `0083-machine-id-host-certs.md`
- `0090-db-mfa-sessions.md`
- `0094-kubernetes-node-joining.md`
- `0102-azure-node-join.md`
- `0103-application-access-web-ui-auth-flow.md`
- `0105-headless-authentication.md`
- `0111-support-connection-testers-with-per-session-mfa.md`
- `0119-aws-api-integration-using-oidc.md`
- `0121-kube-mfa-sessions.md`
- `0131-adminitrative-actions-mfa.md`
- `0136-modern-signature-algorithms.md`
- `0143-external-k8s-joining.md`
- `0149-headless-kube.md`
- `0155-scoped-webauthn-credentials.md`
- `0162-machine-id-token-join-method-bot-instance.md`
- `0166-tpm-joining.md`
- `0168-database-ca-split.md`, `0239-windows-ca-split.md`
- `0169-app-mfa-sessions.md`
- `0175-spiffe-federation.md`
- `0180-sso-mfa.md`
- `0185-k8s-access-non-cert-routing.md`
- `0192-oci-join.md`
- `0199-hardware-key-agent.md`
- `0202-db-multi-session-mfa.md`
- `0205-improved-onprem-joining.md`
- `0206-sigstore-workload-attestation.md`
- `0210-windows-ad-hsm.md`
- `0211-azure-devops-joining.md`
- `0229a-scoped-join-tokens.md`
- `0231-new-oracle-join-method.md`
- `0233-tsh-browser-mfa.md`
- `0234-in-band-mfa-ssh-sessions.md`
