# Execution Prompt — OIDC SSO for the Community Build (2026-05-24)

Paste the fenced block below into a fresh Claude Code session on the `oidc` feature branch.

- **Branch setup (do first):** `git switch -c oidc orvex` (cut the OIDC feature branch from the fork's integration branch `orvex`). All work lands on `oidc`.
- **Full architecture + resolved decisions:** [`_bmad-output/planning-artifacts/architecture.md`](architecture.md) — read the WHOLE file before starting. The decision list **D1–D11**, the **change-set map**, and the **architectural boundaries** are load-bearing.
- **Supporting context:** [`_bmad-output/planning-artifacts/oidc-community-implementation-plan.md`](oidc-community-implementation-plan.md) (current-state inventory + the three gaps) and [`docs/findings/backend-auth-security.md`](../../docs/findings/backend-auth-security.md) §6 (SSO connectors).
- **Reference implementation:** `lib/auth/github.go` (1,224 lines) and `lib/auth/github_test.go` (622 lines) — the OIDC service and web handlers mirror these.
- **Execution model:** a wave-based agent squad on `oidc`. **No worktrees.** This work is *concentrated* — the logic is one new file (`lib/auth/oidc_login.go`) plus three small edits — so Waves 0–3 are single-agent and sequential by necessity; genuine parallelism is only Wave 5 (review fixes). An orchestrator gate runs between every wave.
- **No codegen:** unlike most Teleport changes, this touches **no** `.proto` files — the OIDC connector + auth-request messages already exist. Do not run protoc / buf.
- **Pre-work HEAD:** the orchestrator captures `git rev-parse HEAD` as its first action.

---

```
Execute the OIDC-SSO-for-the-community-build implementation. The full architecture
and the resolved decisions (D1–D11) are in
_bmad-output/planning-artifacts/architecture.md — read the WHOLE file now, before
anything else. Supporting current-state detail is in
_bmad-output/planning-artifacts/oidc-community-implementation-plan.md. The
reference implementation you will mirror is lib/auth/github.go (+ github_test.go).

You are the ORCHESTRATOR. You spawn agents, enforce gates, and commit. You do not
edit production code yourself except to resolve a gate failure.

First action: run `git rev-parse HEAD` and record it as the pre-work HEAD.

═══════════════════════════════════════════════════════════════════════
GLOBAL RULES (apply to every wave)
═══════════════════════════════════════════════════════════════════════

SCOPE — what is IN and what is OUT
- IN: D1–D9 and D11 from architecture.md — the entitlement gate, the OIDC auth-flow
  service, the auth-server wiring, the proxy web routes, claims→roles, session/cert
  issuance, and login audit events. The run is finished only when Wave 6 confirms
  web + console OIDC login work end-to-end against Keycloak.
- DEFERRED (explicitly out of scope this run): D10 — OIDC-backed MFA
  (CreateOIDCAuthRequestForMFA / lib/auth/sso_mfa.go). Implement it as a clear
  "not yet supported" error and move on.
- NEVER touch: the oidc_idp CA, lib/oidc/token_validator.go (join-method OIDC), or
  SAML. Enabling SAML's entitlement is forbidden — only OIDC.

BRANCH & ISOLATION
- All work happens on `oidc`. NO worktrees. NO sub-branches.
- Every agent edits files directly in the shared working tree.
- Waves 0–3 are single-agent. In any parallel wave (Wave 5), agents own STRICTLY
  DISJOINT files; if an agent needs a file owned by another, it STOPS and reports.

GATE PROTOCOL (orchestrator runs this after each wave's agent(s) report done)
- There is NO codegen step (no .proto changes).
- Run, in order, scoped to the touched packages:
    1. gofmt -l lib/auth lib/web lib/modules lib/service   (must print nothing)
    2. go build ./lib/... ./tool/teleport ./tool/tctl
    3. go test ./lib/auth/... ./lib/modules/... ./lib/web/...
    4. make lint-go            (golangci-lint -c .golangci.yml)
- If the gate is RED: identify the culprit, dispatch a single fix agent against the
  offending files, re-run the gate. Repeat until GREEN.
- When GREEN: commit the wave's slice(s). One logical change = one commit.
- Do NOT start wave N+1 until wave N is committed and the gate is GREEN.

DISCIPLINE
- Every behavioural change ships as ONE commit containing the test (un-skipped from
  the Wave 0 net, or new), the change, and the test green. A fix is not done
  otherwise.
- If a change breaks a pre-existing test, fix it to match CORRECT behaviour — never
  to re-encode a bug. If unsure whether a test encodes intended behaviour, STOP and
  report.

CONVENTIONS
- Commit style: conventional, scoped, matching the fork's recent history —
  feat(auth:), feat(web:), feat(service:), feat(modules:), fix(review:), test(auth:).
- Errors: wrap with trace.Wrap(err). Preserve the OIDCService interface signatures
  in lib/auth/oidc.go:34 verbatim.
- Logging: package slog logger. NEVER log tokens, auth codes, client secrets, or
  full claim payloads.
- Thread context.Context everywhere.
- SECURITY (non-negotiable, architecture D2/D4): reuse sso.ValidateClientRedirect
  (lib/client/sso/redirector.go:583) exactly as github.go:159 does — do not
  reinvent. Verify the ID token (signature vs the provider JWKS, iss, aud==client_id,
  exp) AND the nonce AND the state on every callback before any user/session exists.
- CLEAN-ROOM: implement from public OIDC specs + the OSS GitHub connector only.
  Never reference or import proprietary e/ source.

═══════════════════════════════════════════════════════════════════════
WAVE 0 — Baseline + characterization test net   (1 agent, blocking)
═══════════════════════════════════════════════════════════════════════
First: go build ./lib/... AND go test ./lib/auth/... ./lib/modules/... ./lib/web/...
MUST be green. If not, STOP and report.

Then create the characterization net for the future OIDC login service, mirroring
the structure of lib/auth/github_test.go, in:
  lib/auth/oidc_login_test.go

Cases (every test COMPILES and is marked t.Skip("un-skip in Wave 2") so the gate
stays green; Wave 2 un-skips them):
  L1 — Auth request: createOIDCAuthRequest builds a valid authorization-code URL
       (issuer's authorize endpoint, client_id, scope incl. openid, state, nonce)
       and persists the request via the existing CreateOIDCAuthRequest storage.
  L2 — Callback happy path: a well-formed callback (valid state + code + ID token)
       creates/updates the user, applies claims→roles, and returns a populated
       authclient.OIDCAuthResponse with a session.
  L3 — claims→roles: connector ClaimsToRoles maps a Keycloak groups/roles claim to
       the expected Teleport roles (via services.TraitsToRoles).
  L4 — SECURITY: invalid/missing state token is rejected.
  L5 — SECURITY: nonce mismatch is rejected.
  L6 — SECURITY: bad ID-token signature (wrong/again unknown key) is rejected.
  L7 — SECURITY: a disallowed client redirect URL is rejected by
       sso.ValidateClientRedirect.

Gate. Commit: test(auth): characterization net for OIDC login service (L1-L7, skipped)

═══════════════════════════════════════════════════════════════════════
WAVE 1 — Open the OIDC entitlement gate   (1 agent)   [architecture D5]
═══════════════════════════════════════════════════════════════════════
Owns exclusively:
  lib/modules/modules.go
  (+ a small assertion test in lib/modules or a ServerWithRoles test in lib/auth)
Task:
  - In defaultModules.Features() (lib/modules/modules.go:389-405), add exactly:
        entitlements.OIDC: {Enabled: true, Limit: 0},
    Do NOT add entitlements.SAML or anything else.
  - Prove it: a test asserting modules.Features().GetEntitlement(entitlements.OIDC)
    .Enabled is true in the community build, and/or that ServerWithRoles
    CreateOIDCConnector no longer returns AccessDenied (the gate at
    auth_with_roles.go:4439 now passes).
Gate. Commit: feat(modules): enable OIDC entitlement in the community build

═══════════════════════════════════════════════════════════════════════
WAVE 2 — OIDC auth-flow service   (1 agent — NEW file, built alongside)
═══════════════════════════════════════════════════════════════════════         [architecture D1, D2, D3, D4, D8, D9]
Owns exclusively:
  lib/auth/oidc_login.go            (NEW — the OIDCService implementation)
  lib/auth/oidc_login_test.go       (un-skip L1-L7)
  go.mod / go.sum                   (promote github.com/coreos/go-oidc/v3 from
                                     indirect to direct — already in the module graph)
Task — implement the OIDCService interface (lib/auth/oidc.go:34), mirroring
github.go method-for-method:
  - newOIDCOAuth2Config(connector): build oauth2.Config from the connector
    (client id/secret, redirect, scopes incl. "openid"); resolve endpoints via
    go-oidc discovery (oidc.NewProvider(ctx, issuer) → provider.Endpoint()). Cache
    the provider/JWKS keyset — do NOT refetch per callback. [D1, D2 perf]
  - createOIDCAuthRequest: ValidateClientRedirect FIRST [D4]; generate state +
    nonce; add PKCE params when connector GetPKCEMode() is on [D3]; set
    req.RedirectURL = config.AuthCodeURL(state, nonce, …); persist via the existing
    CreateOIDCAuthRequest storage (mirror github.go:142).
  - validateOIDCAuthCallback: load the request by state; exchange the code; build a
    go-oidc Verifier(&oidc.Config{ClientID}) and verify the ID token (signature vs
    JWKS, iss, aud, exp); check nonce; extract claims. [D2] Mirror
    github.go ValidateGithubAuthRedirect (github.go:538).
  - map claims→roles via services.TraitsToRoles + connector GetClaimsToRoles
    (api/types/oidc.go:376). [D8]
  - makeOIDCAuthResponse: reuse Server CreateWebSessionFromReq / CreateSessionCerts
    exactly as makeGithubAuthResponse (github.go:661) does; return
    *authclient.OIDCAuthResponse. [D9]
  - CreateOIDCAuthRequestForMFA: return a clear "not yet supported" error. [D10 deferred]
  - Constructor: NewOIDCService(authServer *Server) OIDCService (or accept the
    narrow interfaces it needs).
  - This service is built ALONGSIDE the stub — it is NOT wired in yet.
  - Un-skip and pass L1-L7.
Gate. Commit: feat(auth): OIDC login service for the community build (Keycloak) (L1-L7)
(If the go.mod change is cleaner as its own commit: build(deps): promote coreos/go-oidc/v3 to a direct dependency)

═══════════════════════════════════════════════════════════════════════
WAVE 3 — Wire it in + proxy routes + audit   (1 agent, 3 sequential commits)
═══════════════════════════════════════════════════════════════════════         [architecture D6, D7, D11]
Owns exclusively:
  lib/service/service.go            (D6 wiring)
  lib/web/apiserver.go              (D7 routes + handlers)
  lib/auth/oidc.go                  (only if the stub delegation needs adjusting)
Tasks (commit each as its own slice):
  - D6: in initAuthService (lib/service/service.go:2208), immediately after
    `authServer, err := auth.Init(…)` (:2434) succeeds, call
    authServer.SetOIDCService(NewOIDCService(authServer)). (This is the seam
    enterprise fills via the plugin registry; we wire it directly.)
    Commit: feat(service): wire the OIDC login service into the community auth server
  - D7: in lib/web/apiserver.go, register three routes beside the GitHub ones
    (~:1040) and add the handlers, mirroring githubLoginWeb / githubCallback /
    githubLoginConsole:
        GET  /webapi/oidc/login/web      -> oidcLoginWeb
        GET  /webapi/oidc/callback       -> oidcCallback
        POST /webapi/oidc/login/console  -> oidcLoginConsole
    They call the existing proxy-client methods CreateOIDCAuthRequest /
    ValidateOIDCAuthCallback. The web UI already advertises OIDC connectors and
    renders the buttons — these routes make them work.
    Commit: feat(web): OIDC proxy login routes (web, console, callback)
  - D11: emit SSO login success/failure audit events in the validate path (reuse the
    shared SSO login event types). Connector-lifecycle events already emit.
    Commit: feat(auth): emit OIDC SSO login audit events
Gate after all three.

═══════════════════════════════════════════════════════════════════════
WAVE 4 — Security + correctness review   (orchestrator, single step)
═══════════════════════════════════════════════════════════════════════
On the full diff since the pre-work HEAD (git diff <pre-work-HEAD>...HEAD):
  - Run /security-review FIRST — this is an authentication / SSO surface; prioritise
    token validation, redirect validation, state/nonce/PKCE, and secret handling.
  - Then run /bmad-code-review for general correctness.
Collect ALL findings, grouped by severity (critical / high / medium / low), each
with file:line and a proposed fix.

═══════════════════════════════════════════════════════════════════════
WAVE 5 — Fix EVERY review finding   (parallel agents, disjoint files)
═══════════════════════════════════════════════════════════════════════
Fix EVERY finding — critical, high, MEDIUM, AND LOW. Nothing deferred or waved off
(except D10 MFA, which was out of scope from the start). Partition findings into
disjoint file-sets; one agent per partition; each adds a regression test where the
finding warrants one. Gate, then commit each slice: fix(review): <short description>.
If the Wave 5 changes are non-trivial, re-run /security-review and /bmad-code-review
on the Wave 5 diff and fix any new findings the same way.

═══════════════════════════════════════════════════════════════════════
WAVE 6 — Final verification + Keycloak e2e + report   (1 agent, then orchestrator)
═══════════════════════════════════════════════════════════════════════
Automated gate (full):
  - gofmt clean; go build ./lib/... ./tool/teleport ./tool/tctl
  - go test -race ./lib/auth/... ./lib/modules/... ./lib/web/...
  - make lint-go  — 0 issues.

Manual end-to-end (CANNOT be fully automated — the agent MUST flag this and provide
the checklist; do not claim success without it):
  1. Bring up Keycloak (Docker): create a realm + a confidential client; add a
     group/role mapper so a groups/roles claim is emitted.
  2. Build teleport+tctl; configure a cluster; `tctl create` an OIDC connector
     (issuer = https://<host>/realms/<realm>, redirect_url =
     https://<proxy>/v1/webapi/oidc/callback, scope incl. openid, claims_to_roles
     mapping the claim to a Teleport role). See examples/resources/oidc-connector.yaml.
  3. Web login via the OIDC button → Keycloak → back to Teleport with a session.
  4. `tsh login --auth=<oidc-connector>` (console flow) → certs issued.
  5. Confirm the mapped role is applied and OIDC login audit events fire.

Deliverable check: confirm D1-D9 and D11 are done; state explicitly that D10 (MFA)
is deferred. Final report: every commit grouped by wave; the /security-review and
/bmad-code-review dispositions; and an explicit statement that BOTH web and console
OIDC login succeed against Keycloak and that claims map to Teleport roles.

═══════════════════════════════════════════════════════════════════════
DO NOT, at any point: create worktrees or sub-branches; import or reference
proprietary e/ source; enable the SAML entitlement; touch the oidc_idp CA or
lib/oidc/token_validator.go; reinvent sso.ValidateClientRedirect; log tokens, codes,
secrets, or full claims; bump dependencies other than promoting coreos/go-oidc/v3 to
direct; skip a gate; defer a review finding of any severity; or bypass git hooks.
```

---

## Wave summary

| Wave | Agents | Delivers |
|---|---|---|
| 0 | 1 | baseline green; L1–L7 characterization net (skipped) |
| 1 | 1 | D5 — OIDC entitlement enabled in the community build (1 line) |
| 2 | 1 | D1–D4, D8, D9 — the OIDC auth-flow service (`oidc_login.go`, new; built alongside) |
| 3 | 1 | D6 wiring · D7 proxy routes · D11 login audit — end-to-end login works |
| 4 | orchestrator | `/security-review` + `/bmad-code-review` findings |
| 5 | parallel | every finding fixed — incl. medium + low |
| 6 | 1 | full green, manual Keycloak e2e, every deliverable verified, final report |

Sequential by necessity through Wave 3 — the logic is concentrated in one new file plus three small edits — then parallel only in Wave 5. All work lands on `oidc`. No worktrees, no codegen. Verification gate (`gofmt` + `go build` + targeted `go test` + `make lint-go`) between every wave; `/security-review` leads the review because this is an authentication surface. D10 (OIDC MFA) is deferred by design.
