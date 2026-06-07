# OIDC SSO for the Community (OSS) Build — Implementation Plan

**Author:** Mary (Business Analyst, BMAD) · **Date:** 2026-05-24
**Repo:** `orvexai/teleport` (fork) · **Branch of record:** `oidc`
**Target IdP:** Keycloak (generic OIDC)
**Status:** Research complete; ready for build planning

---

## 1. Governing thought (the answer up front)

**Adding OIDC login to the OSS build is a well-scoped, low-surface-area task, not a rewrite.** Roughly **90% of the plumbing already ships in OSS** — the connector resource type, gRPC/proto, backend storage, audit events, web SSO buttons, the `tsh` SSO redirector, and the OIDC libraries are all present. Enterprise gates the feature behind exactly **three seams**, all verified against current code:

| # | Gap | Where | Size |
|---|-----|-------|------|
| 1 | **OIDC auth-flow service is a nil stub** — the real login logic is unimplemented in OSS | `lib/auth/oidc.go:34-40, 128-159` | **L** (the real work) |
| 2 | **License entitlement gate** — `entitlements.OIDC` is omitted from the OSS feature set, so connector CRUD + auth-request creation return AccessDenied | `lib/modules/modules.go:389-405` (enforced at `lib/auth/auth_with_roles.go:4398/4419/4439/4501`) | **S** (one line) |
| 3 | **Proxy web routes missing** — `/webapi/oidc/login/*` + `/webapi/oidc/callback` are never registered in OSS core (enterprise adds them via the plugin registry) | `lib/web/apiserver.go` (~line 1040) | **M** (3 handlers, mirror GitHub) |

The implementation model is **already in the tree**: the fully-OSS GitHub connector (`lib/auth/github.go`, 1,224 lines) is the structural template for both the auth service (Gap 1) and the web handlers (Gap 3). The one place OIDC genuinely diverges from GitHub is **ID-token signature validation**, which the already-vendored `github.com/zitadel/oidc/v3` library handles for us.

**Bottom line:** this is a focused fork patch — on the order of one new auth-service file (~400-700 LoC), three web handlers (~150 LoC), and a one-line entitlement change, plus wiring and tests. No proprietary enterprise source is needed or used (clean-room reimplementation on the AGPL base).

---

## 2. What already exists in OSS (verified inventory)

Everything below is **present and usable today** — do not re-plan it.

| Capability | Status | Evidence (file:line) | Note |
|-----------|--------|----------------------|------|
| `e/` enterprise submodule | **Absent** | no `e/`, no `.gitmodules` | Clean OSS checkout; nothing to pull in |
| `OIDCConnector` resource type | **Present (full)** | `api/types/oidc.go:38-154` | All Keycloak-relevant fields: IssuerURL, ClientID, ClientSecret, RedirectURLs, Scope, ClaimsToRoles, ACR, Provider, PKCE, MFA, ClientRedirectSettings, UserMatchers |
| Proto / gRPC (connector CRUD + auth-request) | **Present (full)** | `api/proto/.../types.proto`, `authservice.proto` | `Create/Update/Upsert/Delete/Get/ListOIDCConnector`, `CreateOIDCAuthRequest`, `GetOIDCAuthRequest` |
| Backend storage | **Present (full)** | `lib/services/local/users.go:1520-1626` (connectors), `:1682` `CreateOIDCAuthRequest`, `:1702` `GetOIDCAuthRequest` | State-token-keyed auth-request persistence |
| Connector CRUD on auth server | **Present (full)** | `lib/auth/oidc.go:42-126` | Upsert/Create/Update/Delete already implemented + emit audit events |
| OIDC / OAuth2 libraries | **Present** | `go.mod`: `github.com/zitadel/oidc/v3 v3.45.0` (direct), `golang.org/x/oauth2 v0.36.0`, `github.com/coreos/go-oidc/v3 v3.17.0` (indirect) | zitadel RP client = discovery + token verification out of the box |
| Audit events (connector lifecycle) | **Present (full)** | `events.proto:2683-2754`; emitted in `lib/auth/oidc.go:48-101` | `OIDCConnectorCreate/Update/Delete` |
| `tsh` SSO client flow | **Partial (generic)** | `lib/client/sso/redirector.go` | Connector-agnostic redirector; works once auth service exists |
| Web UI SSO buttons | **Present** | `web/.../FormLogin/SsoButtons.tsx`, `AuthConnectors/ssoIcons/getSsoIcon.tsx:58-76` | `'oidc'` type already rendered; proxy advertises OIDC connectors to FE at `apiserver.go:2086-2096` |
| Reference implementation (GitHub) | **Present (full)** | `lib/auth/github.go` (1,224 lines) | The template to mirror |
| `OIDCService` seam on auth Server | **Present (stub)** | field `lib/auth/auth.go:1294`; setter `SetOIDCService()` `:1576-1581` | Designed injection point — currently never called in OSS |

---

## 3. The three gaps in detail

### Gap 1 — OIDC auth-flow service (the core work)

`lib/auth/oidc.go` defines the contract and a nil-delegating stub:

```go
// lib/auth/oidc.go:34-40
type OIDCService interface {
    CreateOIDCAuthRequest(ctx context.Context, req types.OIDCAuthRequest) (*types.OIDCAuthRequest, error)
    CreateOIDCAuthRequestForMFA(ctx context.Context, req types.OIDCAuthRequest) (*types.OIDCAuthRequest, error)
    ValidateOIDCAuthCallback(ctx context.Context, q url.Values) (*authclient.OIDCAuthResponse, error)
}
var errOIDCNotImplemented = &trace.AccessDeniedError{Message: "OIDC is only available in enterprise subscriptions"}
```

The three flow methods (lines 128-159) each do `if a.oidcAuthService == nil { return errOIDCNotImplemented }` and otherwise delegate. **No OSS implementation is registered**, so login dead-ends here.

**What to build:** a concrete `OIDCService` implementation, structurally mirroring `lib/auth/github.go`:

| Step | GitHub anchor to mirror | OIDC-specific change |
|------|------------------------|----------------------|
| Build auth/redirect URL + persist request | `CreateGithubAuthRequest` (`github.go:142-179`) | Use OIDC discovery (`issuer/.well-known/openid-configuration`) to get the authorization endpoint; generate state + nonce; persist via existing `CreateOIDCAuthRequest` storage |
| Validate callback | `ValidateGithubAuthRedirect` (`github.go:538-659`) | Verify `state` against stored request; exchange `code`; **verify ID-token signature + nonce against the provider JWKS** (zitadel/oidc) — *this is the one true divergence from GitHub* |
| Extract claims | `getGithubUserAndTeams` / `populateGithubClaims` (`github.go:782-821, 1021-1041`) | Read claims from the verified ID token (and optional userinfo endpoint) rather than the GitHub REST API |
| Map claims → roles | `calculateGithubUser` (`github.go:904-952`) | Reuse the connector's `ClaimsToRoles` + login-rules logic nearly verbatim |
| Create user + session/cert | `createGithubUser` / `makeGithubAuthResponse` (`github.go:954-1019, 661-739`) | Effectively identical; reuse `CreateWebSessionFromReq` / `CreateSessionCerts` |

`CreateOIDCAuthRequestForMFA` is a variant of the first step for MFA-gated flows (lower priority than first login).

### Gap 2 — License entitlement gate

`defaultModules.Features()` enumerates OSS entitlements and **omits OIDC**:

```go
// lib/modules/modules.go:389-405 (OSS)
Entitlements: map[entitlements.EntitlementKind]EntitlementInfo{
    entitlements.App: {Enabled: true}, entitlements.DB: {Enabled: true},
    entitlements.Desktop: {Enabled: true}, entitlements.JoinActiveSessions: {Enabled: true},
    entitlements.K8s: {Enabled: true},
    // entitlements.OIDC is absent → defaults to {Enabled: false}
}
```

`entitlements.OIDC` exists (`entitlements/entitlements.go:49`). Four enforcement points in `lib/auth/auth_with_roles.go` reject the feature when the entitlement is disabled — `UpsertOIDCConnector` (4398), `UpdateOIDCConnector` (4419), `CreateOIDCConnector` (4439), `CreateOIDCAuthRequest` (4501) — all via `modules.Features().GetEntitlement(entitlements.OIDC).Enabled`. GitHub has **no equivalent gate**, which is precisely why it works in OSS.

**Fix:** add one line — `entitlements.OIDC: {Enabled: true, Limit: 0}` — to the map above. That single change unblocks all four sites.
**Scope discipline:** add **only** OIDC. Do *not* also enable `entitlements.SAML` — SAML's flow is likewise unimplemented in OSS, and enabling its entitlement would expose connector CRUD with broken login.

### Gap 3 — Proxy web routes

GitHub registers three proxy routes (`lib/web/apiserver.go`):
- `:1040` `GET /webapi/github/login/web` → `githubLoginWeb` (`:2416`)
- `:1041` `GET /webapi/github/callback` → `githubCallback` (`:2496`)
- `:1042` `POST /webapi/github/login/console` → `githubLoginConsole` (`:2448`)

OIDC has **none** in OSS. The frontend already constructs `/v1/webapi/oidc/login/web?...` (constant `WebConfigAuthProviderOIDCURL`) and the proxy already advertises OIDC connectors to the UI — so the login button renders but **404s**. Enterprise registers these handlers through `plugin.Registry.RegisterProxyWebHandlers()` (`lib/plugin/registry.go`), which is a **no-op empty registry** in OSS.

**Fix (recommended):** add three handlers — `oidcLoginWeb`, `oidcCallback`, `oidcLoginConsole` — directly in `lib/web/apiserver.go`, mirroring the GitHub trio, registered next to the GitHub routes. They simply call the **already-existing** proxy-client methods `CreateOIDCAuthRequest()` / `ValidateOIDCAuthCallback()` (`lib/auth/authclient/clt.go`). Adding routes directly (rather than spinning up an OSS plugin) keeps the diff small and rebase-friendly.

---

## 4. Recommended implementation sequence

### Phase 0 — Spike / de-risk (½–1 day)
- Stand up Keycloak locally (Docker), create a realm + confidential client, add a groups/roles claim mapper.
- Prove the zitadel/oidc relying-party client can discover the Keycloak issuer, complete the code exchange, and verify an ID token. This retires the only real technical unknown before touching Teleport.

### Phase 1 — Open the gate + wiring seam (S)
- Add `entitlements.OIDC: {Enabled: true}` to `lib/modules/modules.go:389-405`.
- **Locate the OSS auth-server init seam** and call `authServer.SetOIDCService(NewOIDCService(...))`. *Open item:* the exact call site (enterprise does this from `e/`) needs to be pinned down — most likely in `lib/service/service.go` where the auth server is constructed. Confirm during implementation.
- At this point connector CRUD works end-to-end (create a Keycloak connector via `tctl`), and login fails cleanly at the new service instead of the entitlement wall.

### Phase 2 — Implement `OIDCService` (L — core)
- New file, e.g. `lib/auth/oidc_service.go` (OSS), implementing the 3-method interface by mirroring `github.go` per the table in §3.
- Start with `CreateOIDCAuthRequest` + `ValidateOIDCAuthCallback` (web + console first-login). Defer `CreateOIDCAuthRequestForMFA` to a follow-up.
- Reuse existing storage, claims-to-roles, login-rules, and session-creation helpers — do not reinvent them.

### Phase 3 — Proxy web routes (M)
- Add `oidcLoginWeb` / `oidcCallback` / `oidcLoginConsole` + registrations in `lib/web/apiserver.go`, mirroring GitHub.
- Verify the existing web UI button now drives a full browser login.

### Phase 4 — Test & validate
- **Unit:** mirror `github_test.go` for the new service (auth-request creation, callback validation, claim→role mapping, signature/nonce failure paths).
- **Integration:** end-to-end browser + `tsh login --auth=<oidc-connector>` against the Phase-0 Keycloak.
- **Regression:** confirm GitHub login and local login are untouched; confirm a build *without* the change still reports OIDC as enterprise-only (i.e., the change is cleanly isolated).

---

## 5. Keycloak-specific notes
- Keycloak is a standard, discovery-compliant OIDC provider — no special-casing needed beyond a correct `issuer_url` (`https://<host>/realms/<realm>`).
- Connector config: set `client_id`/`client_secret` to the Keycloak confidential client, `redirect_url` to `https://<proxy>/v1/webapi/oidc/callback`, `scope` to include `openid profile email groups` (add a Keycloak **group/role mapper** so the chosen claim is emitted), and `claims_to_roles` to map that claim to Teleport roles.
- Example scaffolding already exists at `examples/resources/oidc-connector.yaml`.

## 6. Risks & open questions
1. **Wiring seam (Phase 1):** exact location of the OSS `SetOIDCService` call is not yet pinned — first task of implementation. *(Low risk; the seam exists by design.)*
2. **Login success/failure audit events:** connector-lifecycle events are confirmed present; verify that SSO **login** success/failure events fire for the OIDC path (likely shared SSO events, but confirm in Phase 2).
3. **Upstream rebase drift:** keep the diff small and isolated (one new file + ~3 localized edits) so the fork rebases cleanly against upstream Teleport.
4. **Library choice:** prefer `zitadel/oidc/v3` (direct dep, full RP client). `coreos/go-oidc/v3` is available as a fallback. Decide in Phase 0.
5. **Licensing/clean-room:** reimplement from public OIDC specs and the OSS GitHub connector only — do **not** reference or import proprietary `e/` source.

## 7. Effort shape
- Gap 2 (entitlement): trivial — 1 line.
- Gap 3 (web routes): small-moderate — ~150 LoC mirroring GitHub.
- Gap 1 (service): the real work — ~400-700 LoC + tests, mostly adaptation of existing patterns.
- **Net:** a focused, single-developer fork patch. The heavy scaffolding (types, proto, storage, UI, libs) is already done.

---

*Verified against the current `oidc`-branch working tree on 2026-05-24 via four independent code-exploration passes. All file:line anchors reflect the present checkout and should be re-confirmed if the branch advances substantially.*
