---
stepsCompleted: [1, 2, 3, 4, 5, 6, 7, 8]
inputDocuments:
  - _bmad-output/planning-artifacts/oidc-community-implementation-plan.md
  - docs/findings/backend-auth-security.md
  - docs/architecture-backend.md (available)
  - docs/architecture-web.md (available)
  - docs/technology-stack.md (available)
workflowType: 'architecture'
project_name: 'teleport'
user_name: 'Daniel'
date: '2026-05-24'
subject: 'OIDC SSO for the OSS (Community) build — Keycloak'
lastStep: 8
status: 'complete'
completedAt: '2026-05-24'
---

# Architecture Decision Document — OIDC SSO for OSS Teleport (Keycloak)

_This document builds collaboratively through step-by-step discovery. Sections are appended as we work through each architectural decision together._

## Project Context Analysis

### Requirements Overview

**Functional Requirements (derived from implementation plan + auth findings):**
- FR1 — Operators can CRUD an OIDC connector (Keycloak) via `tctl`/web. *(Already implemented in OSS; blocked only by the entitlement gate.)*
- FR2 — Users can log in via the **web UI** using an OIDC connector (browser → Keycloak → callback → Teleport web session).
- FR3 — Users can log in via **`tsh login --auth=<oidc-connector>`** (console/CLI redirector flow).
- FR4 — OIDC claims (Keycloak groups/roles) map to Teleport roles via the connector's `claims_to_roles`, reusing `services.TraitsToRoles`.
- FR5 — The callback **verifies the ID-token signature + nonce + state** against Keycloak's discovery/JWKS endpoints.
- FR6 — Connector-lifecycle and SSO login success/failure **audit events** fire (lifecycle events already wired).
- FR7 *(deferred)* — OIDC-backed **MFA** (`CreateOIDCAuthRequestForMFA`, the `sso_mfa.go` path).

**Non-Functional Requirements:**
- NFR-Security *(dominant)* — Reuse `ValidateClientRedirect` (`lib/client/sso/redirector.go:583`, explicitly security-critical); enforce state + nonce + PKCE; verify ID-token signatures; no open redirects; safe `client_secret` handling. Match Teleport's existing SSO threat model exactly.
- NFR-Maintainability/Rebase — Keep the change minimal and isolated (one new auth file + ~3 localized edits) so the fork rebases cleanly against upstream.
- NFR-Consistency — Reuse the existing session/cert issuance, RBAC, and login-rule machinery so OIDC behaves identically to GitHub from the rest of Teleport's perspective.
- NFR-Licensing — AGPL clean-room reimplementation; no reference to proprietary `e/` source.
- NFR-Performance — Cache OIDC discovery + JWKS (don't refetch per callback).

**Scale & Complexity:**
- Primary domain: **backend Go (auth service)** + thin proxy/web layer; frontend is effectively done (SSO buttons render for `oidc` already).
- Complexity level: **medium** — bounded breadth, but security-critical correctness.
- Architectural components touched: **~4** — (1) OIDC auth-flow service impl, (2) modules entitlement, (3) proxy web handlers, (4) auth-server init wiring — plus tests.

### Technical Constraints & Dependencies
- Fixed contract: must implement the existing `OIDCService` interface (3 methods, `lib/auth/oidc.go:34`) and return the existing `authclient.OIDCAuthResponse`.
- Must reuse existing types/storage: `OIDCConnectorV3`, `OIDCAuthRequest`, `CreateOIDCAuthRequest`/`GetOIDCAuthRequest`.
- Available libraries (decision deferred to design step): `github.com/zitadel/oidc/v3` (direct), `golang.org/x/oauth2` (direct), `github.com/coreos/go-oidc/v3` (indirect).
- Reference implementation to mirror: `lib/auth/github.go`.
- IdP: Keycloak — a standard discovery-compliant OIDC provider (no special-casing expected).

### Cross-Cutting Concerns Identified
- **Security** (SSO redirect validation, token validation, secret storage) — spans auth + proxy.
- **Entitlement/licensing gate** — enabling `entitlements.OIDC` is read in many places; scope strictly to OIDC (do **not** also enable SAML, which is unimplemented).
- **Traits → roles / login rules** — shared SSO machinery reused, not rebuilt.
- **Audit & observability** — login + connector events.
- **Rebase/maintainability** — isolation of the diff spans every touched file.
- **Scope boundary** — *not* to be confused with adjacent OIDC subsystems: the `oidc_idp` CA (Teleport-as-IdP for app access) and `lib/oidc/token_validator.go` (join-method OIDC like GitHub Actions) are **out of scope**.

## Starter Template Evaluation

### Primary Technology Domain
Backend service feature inside an existing large Go monorepo (Teleport), plus a thin Go proxy/web handler layer. The TypeScript web UI needs no new work.

### Starter Options Considered
N/A — this is a **brownfield** change. There is no greenfield starter decision; the de-facto "starter" is the Teleport monorepo itself, with its established `make` build, module layout, and pinned dependency set.

### Selected "Starter": the existing Teleport repository
**Rationale:** all language, build, test, lint, and dependency choices are already fixed by the monorepo. Adding scaffolding would violate the rebase-isolation NFR. "Initialization" simply means working within `lib/auth`, `lib/web`, `lib/modules`, and `lib/service`.

**Dependency versions (governed by `go.mod`, NOT to be bumped by this work):**
- `github.com/zitadel/oidc/v3 v3.45.0` (direct)
- `golang.org/x/oauth2 v0.36.0` (direct)
- `github.com/coreos/go-oidc/v3 v3.17.0` (currently indirect)

> Deviation note: the workflow normally web-searches for "latest" versions. That targets greenfield projects. Here, versions are dictated by the Teleport module graph; bumping an OIDC library in a monorepo this size is a separate, higher-risk change explicitly out of scope. We use what is already vendored.

## Core Architectural Decisions

### Decision Priority Analysis
- **Critical (block implementation):** library/token-validation (D1–D2), entitlement gate (D5), auth-service wiring seam (D6), proxy route integration (D7).
- **Important (shape architecture):** redirect-validation reuse (D4), claims→roles reuse (D8), session/cert reuse (D9), audit (D11).
- **Deferred (post-v1):** OIDC-backed MFA (D10), identifier-first / user-matchers, Entra-ID group provider.

### Authentication & Security (core)

**D1 — OIDC client library.** Use **`golang.org/x/oauth2`** for the authorization-code flow (mirrors `lib/auth/github.go`'s `newGithubOAuth2Config` → `AuthCodeURL` → `Exchange`) **plus `github.com/coreos/go-oidc/v3`** for OIDC discovery + ID-token verification.
- *Rationale:* maximizes consistency with existing code — `github.go` already uses `oauth2`, and `lib/oidc/token_validator.go` already uses `go-oidc` for token verification. Promotes `coreos/go-oidc/v3` from indirect to direct (already in the module graph — no new download).
- *Alternative:* `github.com/zitadel/oidc/v3` relying-party client (already direct) bundles discovery+exchange+verification in one higher-level abstraction. Viable; rejected for v1 only because it introduces an RP abstraction not used on the login path today. **⚑ Judgment call flagged for Daniel:** if you'd rather standardize on zitadel/oidc, say so and I'll swap D1.

**D2 — Token & request validation.** On callback: validate `state` against the stored `OIDCAuthRequest`; exchange the code; **verify the ID token** via go-oidc `Verifier` (signature vs discovery JWKS, `iss`, `aud`==client_id, `exp`); validate `nonce`. Cache `oidc.Provider`/JWKS (go-oidc's remote keyset caches by default) — never refetch per callback.

**D3 — PKCE.** Honor the connector's existing `GetPKCEMode()`; send PKCE params when enabled.

**D4 — Redirect validation (security-critical).** Reuse `sso.ValidateClientRedirect` (`lib/client/sso/redirector.go:583`) exactly as `github.go:159` does. Do **not** reinvent. Non-negotiable.

**D5 — Entitlement gate.** Add **only** `entitlements.OIDC: {Enabled: true, Limit: 0}` to `defaultModules.Features()` (`lib/modules/modules.go:389-405`). Do **not** enable SAML. Unblocks all four sites (`auth_with_roles.go:4398/4419/4439/4501`).

**D6 — Auth-service wiring seam.** *Verified:* `SetOIDCService` (`lib/auth/auth.go:1579`) is **never called in OSS** — enterprise calls it via the plugin registry's `RegisterAuthServices` (invoked at `lib/auth/middleware.go:257`). **Decision: direct wiring** — call `authServer.SetOIDCService(oidc.NewOIDCService(authServer, …))` in `lib/service/service.go::initAuthService` (`:2208`), right after `authServer, err := auth.Init(…)` (`:2434`).
- *Alternative:* implement an OSS `plugin.Plugin` and register it (upstream-faithful) — rejected as heavier, less rebase-friendly for a fork.

**D7 — Proxy web routes.** **Decision: register 3 handlers directly** in `lib/web/apiserver.go` beside the GitHub routes (≈`:1040`): `GET /webapi/oidc/login/web`, `GET /webapi/oidc/callback`, `POST /webapi/oidc/login/console` → `oidcLoginWeb`/`oidcCallback`/`oidcLoginConsole`, mirroring the GitHub trio. They call the existing proxy-client methods `CreateOIDCAuthRequest`/`ValidateOIDCAuthCallback`. Same rationale as D6.

**D8 — Claims → roles.** Reuse `services.TraitsToRoles` (`lib/services/traits.go:42`) + the connector's `GetClaimsToRoles` (`api/types/oidc.go:376`). No new mapping logic; Login Rules apply via the existing evaluator.

**D9 — Session / cert issuance.** Reuse `Server` helpers exactly as `makeGithubAuthResponse` (`github.go:661`) does (`CreateWebSessionFromReq`, `CreateSessionCerts`). The `authclient.OIDCAuthResponse` return type already exists.

**D10 — MFA (deferred).** Implement `CreateOIDCAuthRequestForMFA` later (the `lib/auth/sso_mfa.go` path). For v1 it may return a clear "not yet supported" error; first-login web + console is the v1 goal.

**D11 — Audit.** Connector-lifecycle events already emit (`oidc.go:48-101`). Emit SSO login success/failure via the existing shared SSO login event types in the new validate path.

### Data Architecture
No schema changes. Reuse `OIDCConnector` storage and `OIDCAuthRequest` (state-token-keyed, TTL) via `CreateOIDCAuthRequest`/`GetOIDCAuthRequest` (`lib/services/local/users.go:1682/1702`).

### API & Communication Patterns
No new proto/gRPC. Connector CRUD + `CreateOIDCAuthRequest`/`GetOIDCAuthRequest` RPCs already exist; proxy→auth uses existing `authclient` methods. The only new wire surface is the 3 proxy HTTP routes (D7).

### Frontend Architecture
No work. The web UI already renders OIDC SSO buttons (`getSsoIcon.tsx`, `SsoButtons.tsx`) and the proxy already advertises OIDC connectors to the FE.

### Infrastructure & Deployment
Ships inside the existing `teleport` binary; the fork's orvex build-on-push CI already builds images. External dependency: a Keycloak realm. No infra changes in this repo; connector configured via `tctl`/YAML (`examples/resources/oidc-connector.yaml`).

### Decision Impact Analysis
- **Implementation sequence:** D5 (gate) + D6 (wiring) → service (D1/D2/D3/D4/D8/D9) → D7 (routes) → D11 (audit) → D10 (MFA, later).
- **Cross-component dependencies:** D6 needs the service (D1–D2); D7 needs D5+D6 (routes are useless until the service answers); D2 needs D1.

## Implementation Patterns & Consistency Rules

### Critical conflict points
Single-developer/agent work in a strict-convention monorepo — the risk is divergence from Teleport conventions, not multi-agent disagreement.

### Naming & file placement
- New auth code in **new files** beside the reference: `lib/auth/oidc_login.go` (impl) + `lib/auth/oidc_login_test.go`, mirroring `github.go`/`github_test.go`. Keep edits to existing `lib/auth/oidc.go` (CRUD + stub) minimal.
- Mirror GitHub method names: `newOIDCOAuth2Config`, `createOIDCAuthRequest`, `validateOIDCAuthCallback`, `makeOIDCAuthResponse`.
- Web handlers: `oidcLoginWeb` / `oidcCallback` / `oidcLoginConsole` (exact GitHub parallel).

### Structure / reuse
- **Reuse, never reinvent:** `sso.ValidateClientRedirect`, `services.TraitsToRoles`, session/cert helpers, existing storage. New code = OIDC protocol glue only.
- Constructor: `oidc.NewOIDCService(authServer *auth.Server, …) auth.OIDCService` (or accept the narrow interfaces it needs).

### Format / process
- Errors: wrap with `trace.Wrap(err)` (Teleport-wide); preserve the `OIDCService` interface signatures verbatim.
- Logging: use the package `slog` logger; **never log tokens, codes, client secrets, or full claims**.
- Always thread `context.Context`.
- Emit audit events on the same boundaries GitHub does.

### Enforcement / guardrails (all changes MUST)
- **Clean-room:** implement from public OIDC specs + the OSS GitHub connector only — never reference proprietary `e/` source.
- **Rebase isolation:** confine logic to new files; the only edits to existing files are (1) `modules.go` entitlement, (2) `service.go` wiring, (3) `apiserver.go` routes, (4) the `oidc.go` stub delegation if needed. Keep each small.
- **Scope guard:** do not touch the `oidc_idp` CA or `lib/oidc/token_validator.go`.

### Keycloak configuration contract (anti-pattern guard)
- `issuer_url = https://<host>/realms/<realm>` (no trailing slash); `redirect_url = https://<proxy>/v1/webapi/oidc/callback`; `scope` includes `openid` (+ `profile email groups`). Add a Keycloak **group/role mapper** so the chosen claim is emitted; map it via `claims_to_roles`.

## Project Structure & Boundaries

### Complete change-set map
```
teleport/
├── lib/
│   ├── auth/
│   │   ├── oidc.go                 # EDIT (minimal): keep CRUD; stub delegates to injected service
│   │   ├── oidc_login.go           # NEW: OIDCService impl (D1,D2,D3,D4,D8,D9) — mirrors github.go
│   │   ├── oidc_login_test.go      # NEW: unit tests — mirrors github_test.go
│   │   ├── github.go               # REFERENCE ONLY (do not change)
│   │   └── auth.go                 # REFERENCE: SetOIDCService :1579 (called from service.go per D6)
│   ├── modules/modules.go          # EDIT (1 line): add entitlements.OIDC to defaultModules.Features() :389
│   ├── service/service.go          # EDIT (small): SetOIDCService in initAuthService :2208 (after auth.Init :2434)
│   ├── web/apiserver.go            # EDIT: 3 oidc routes (~:1040) + 3 handler funcs (mirror github*)
│   ├── client/sso/redirector.go    # REUSE: ValidateClientRedirect :583 (untouched)
│   └── services/
│       ├── traits.go               # REUSE: TraitsToRoles :42 (untouched)
│       └── local/users.go          # REUSE: Create/GetOIDCAuthRequest :1682/:1702 (untouched)
├── api/types/oidc.go               # REUSE: OIDCConnector type (untouched)
├── examples/resources/oidc-connector.yaml   # REFERENCE for Keycloak connector config
└── go.mod                          # EDIT (minor): promote coreos/go-oidc/v3 to direct (already in graph)
```

### Architectural boundaries
- **Auth boundary:** the `OIDCService` interface (`lib/auth/oidc.go`) is the contract; the impl sits behind it, reached only via the auth `Server`. RBAC + entitlement enforced in `ServerWithRoles` *before* the service runs.
- **Proxy↔Auth boundary:** proxy handlers never talk to Keycloak's token endpoint for cert issuance — they call `authclient.CreateOIDCAuthRequest`/`ValidateOIDCAuthCallback`; the auth server owns IdP communication and signing.
- **Trust boundary:** every redirect URL passes `ValidateClientRedirect`; every ID token is signature/claim verified before any user/session is created.
- **External boundary:** Keycloak over HTTPS (discovery + JWKS + token endpoints) only.

### Data flow (web login)
`browser → proxy /webapi/oidc/login/web → authclient.CreateOIDCAuthRequest → auth(OIDCService).createOIDCAuthRequest [store request, build AuthCodeURL] → redirect to Keycloak → Keycloak → proxy /webapi/oidc/callback → authclient.ValidateOIDCAuthCallback → auth(OIDCService).validateOIDCAuthCallback [verify state/code/ID-token/nonce → claims→roles → create user/session/cert] → set web session → redirect to app`

## Architecture Validation Results

### Coherence Validation ✅
- **Decision compatibility:** D1/D2 (oauth2 + go-oidc) match existing in-repo usage; D6/D7 (direct wiring) are internally consistent; no contradictions.
- **Pattern consistency:** naming/file/error/audit patterns mirror the verified GitHub reference.
- **Structure alignment:** the change-set map supports every decision; new logic is isolated to new files.

### Requirements Coverage Validation ✅
- **FR1** CRUD → D5 unblocks (storage exists). **FR2/FR3** web/console login → D6+D7+service. **FR4** claims→roles → D8. **FR5** token validation → D2. **FR6** audit → D11. **FR7** MFA → D10 (explicitly deferred).
- **NFRs:** Security → D2/D3/D4 + clean-room; Maintainability/Rebase → isolation rules + change-set map; Consistency → D8/D9 reuse; Licensing → clean-room guard; Performance → D2 JWKS caching.

### Implementation Readiness Validation ✅
Decisions documented with rationale, alternatives, and exact file:line anchors. Patterns + structure are concrete. One flagged judgment call (D1) and one deferred item (D10) are explicit.

### Gap Analysis
- **Critical:** none.
- **Important:** confirm the precise `NewOIDCService` constructor signature/deps during implementation; confirm the exact shared SSO login audit event type names.
- **Nice-to-have:** containerized-Keycloak integration test harness; identifier-first login support.

### Architecture Completeness Checklist
**Requirements Analysis**
- [x] Project context thoroughly analyzed
- [x] Scale and complexity assessed
- [x] Technical constraints identified
- [x] Cross-cutting concerns mapped

**Architectural Decisions**
- [x] Critical decisions documented with versions
- [x] Technology stack fully specified
- [x] Integration patterns defined
- [x] Performance considerations addressed

**Implementation Patterns**
- [x] Naming conventions established
- [x] Structure patterns defined
- [x] Communication patterns specified
- [x] Process patterns documented

**Project Structure**
- [x] Complete directory structure (change-set map) defined
- [x] Component boundaries established
- [x] Integration points mapped
- [x] Requirements to structure mapping complete

### Architecture Readiness Assessment
- **Overall Status:** READY FOR IMPLEMENTATION
- **Confidence Level:** High — every decision is anchored to verified current code; the change is small, isolated, and patterned on a working OSS reference.
- **Key Strengths:** ~90% of plumbing pre-exists; a battle-tested template (`github.go`); strong reuse of security-critical code; rebase-friendly isolation.
- **Areas for Future Enhancement:** OIDC-backed MFA (D10); containerized Keycloak integration tests; identifier-first / user-matcher support.

### Implementation Handoff
- **AI Agent Guidelines:** follow D1–D11 exactly; reuse (don't reinvent) the named helpers; confine edits to the change-set map; clean-room only.
- **First Implementation Priority:** Phase 0 spike — stand up a local Keycloak realm/client and prove `go-oidc` discovery + ID-token verification before touching Teleport (de-risks D1/D2).
