# Contribution Guide

> **Quick orientation.** This is a *navigator* to the contribution process. The substantive content lives in three places: [`/CONTRIBUTING.md`](../CONTRIBUTING.md) (official policy), [`/AGENTS.md`](../AGENTS.md) (review priorities), and [`development-guide.md`](./development-guide.md) (everything technical: build, test, lint, code conventions).

---

## 1. Where the Information Lives

| Question | Source |
| --- | --- |
| What's the patch-acceptance process? | [`/CONTRIBUTING.md`](../CONTRIBUTING.md) |
| What's the policy for adding dependencies? | [`/CONTRIBUTING.md`](../CONTRIBUTING.md) §Adding dependencies |
| What's the Helm chart contribution flow? | [`/examples/chart/CONTRIBUTING.md`](../examples/chart/CONTRIBUTING.md) |
| Operator-specific conventions? | [`/integrations/operator/CONTRIBUTING.md`](../integrations/operator/CONTRIBUTING.md) |
| Terraform MWI provider conventions? | [`/integrations/terraform-mwi/CONTRIBUTING.md`](../integrations/terraform-mwi/CONTRIBUTING.md) |
| What do reviewers (human + AI) care about? | [`/AGENTS.md`](../AGENTS.md) |
| How do I build, test, lint, hot-reload? | [`development-guide.md`](./development-guide.md) |
| Code style: errors, time, RBAC, logging, resource types? | [`development-guide.md` §7](./development-guide.md) |
| How do I run / iterate on a specific part? | [`development-guide.md` §9](./development-guide.md) |

---

## 2. Patch Acceptance Process (paraphrased from `CONTRIBUTING.md`)

1. **Discussions before issues.** For open-ended questions or design exploration, start a [GitHub Discussion](https://github.com/gravitational/teleport/discussions). Issues are for confirmed bugs and well-defined feature requests.

2. **Comment on a `good-starter-issue`** (or open a new issue) describing the proposed change. Wait for maintainer agreement.

3. **For larger work, write an RFD.** Significant design changes go through the **RFD process** (`/rfd/`). Steps:
   - Copy `/rfd/0000-rfds.md` as a template.
   - Allocate the next RFD number.
   - Submit as a PR; iterate in review.
   - States: `draft → implemented` (or `deprecated`).
   - The header has `authors`, `state`, `required_approvers`, then sections: `What`, `Why`, `Details`.

4. **Fork → write code → open PR.**

5. **Maintainer review.** Historically, the team **creates a "buddy PR"** that incorporates the contributor changes rather than merging the contributor's PR directly. This preserves authorship while letting Teleport engineers handle release coordination.

---

## 3. Reviewer Priorities (from `AGENTS.md`)

Both human and AI reviewers (including any `/review` and `/security-review` skills) focus on:

**What to look for:**
- Authentication / authorization bypasses
- Secret leakage, unsafe logging, credential exposure
- Unsafe defaults in security-sensitive areas
- Injection risks (SQL, command, template, path traversal, SSRF)
- Insecure crypto usage or key handling
- Privilege escalation, sandbox escapes
- Data corruption, durability failures, irreversible loss
- Concurrency hazards causing outages or races
- Reliability regressions: crash loops, panics, deadlocks, unbounded retries

**What to ignore:**
- Style nits, micro-perf, readability — unless tied to a significant failure

**Documentation expectation:**
- When working on a product area, *consult `docs/pages/`* (the user-facing docs) to understand the intended UX. Implementation should match.

---

## 4. Dependency Policy

Any new dependency requires:

- **Pre-approval from core Teleport contributors.** Don't add a dep then ask.
- **An approved license** (AGPL-compatible for the core, Apache-2.0-compatible for the web UI and Teleterm).
- **Go modules** for Go deps (no `go-get`-style fetches, no vendored URLs).

Practical tips:

- For Go, check if `lib/utils/` already has a helper. There's a *lot* there.
- For TS, check if `web/packages/shared/` or `web/packages/design/` already has the component.
- For Rust, prefer pinning by **git rev** when the upstream moves fast (this is what IronRDP and `boring` do).

---

## 5. Commit & PR Style

(Not encoded in `CONTRIBUTING.md`, but observable from the recent log.)

- Imperative mood subject ("Add X", "Fix Y") with the area in parentheses when relevant: `crdgen: address scope-related bugs`, `fix: Propagate DeviceExtensions in remote user identity mapping (#67016)`.
- PR numbers in the subject when squash-merged.
- Body explains the *why* and links to issues / RFDs.
- For ops changes, link the RFD if any.

---

## 6. Test Expectations

| Change type | Expected tests |
| --- | --- |
| New Go function | Add `*_test.go` in the same package; cover the happy path + at least one error path |
| New gRPC service / RPC | Add tests under `lib/auth/<resource>v1/<resource>v1/` + integration test in `integration/` |
| New REST endpoint | Add tests in `lib/web/*_test.go`; include auth boundary tests (positive + negative) |
| New CLI subcommand | Test in `tool/<binary>/common/` |
| Web UI feature | `jest` unit tests + Storybook story; e2e in `e2e/tests/` for critical flows |
| Helm chart change | `helm-unittest` cases in `examples/chart/*/tests/` |
| Operator change | `make -C integrations/operator test` (uses envtest) |
| Terraform provider change | `make -C integrations/terraform test` (needs a test cluster) |
| Rust RDP change | `cargo test --workspace`; if FIPS-affecting, also test with `--features=fips` |
| Proto change | `make grpc` (regenerate), then verify tests still compile; update buf lint compliance |

---

## 7. Documentation Updates

Different doc sources have different ownership:

| Doc | Where to edit | When to regenerate |
| --- | --- | --- |
| User-facing site | `/docs/pages/*.mdx` | Live (Docusaurus) |
| Audit events reference | Edit TS types in `web/packages/teleport/src/services/audit/` | `pnpm --filter=@gravitational/teleport event-reference` regenerates `docs/pages/reference/audit-events.mdx` |
| Helm chart reference | Edit `examples/chart/<chart>/values.yaml` | `build.assets/tooling/cmd/render-helm-ref/` regenerates per-chart `README.md` |
| Resource reference | Edit struct tags on `api/types/*.go` | `build.assets/tooling/cmd/resource-ref-generator/` regenerates `docs/pages/reference/...` |
| Preset roles JSON | Edit the source in `lib/services/presets.go` | `build.assets/dump-preset-roles/` regenerates `gen/preset-roles.json` |
| In-repo READMEs | Per-package READMEs are hand-edited | — |

**Do not edit generated output directly** — it'll be overwritten on the next build.

---

## 8. AI-Reviewer / Skill Use

This repo ships *in-repo agent skills* under `skills/`:

- `teleport-acl-review` — periodic-review helper for access lists.

It also has `.claude/skills/` (BMAD installed skills used for this workflow). If you're adding an agent-assisted workflow:

1. Put it in `skills/` (Teleport-specific) rather than `.claude/skills/` (BMAD-managed).
2. Follow the structure in `skills/README.md`.
3. Document the invocation pattern and required tools.

---

## 9. Cross-References

- Build / test / iterate: [`development-guide.md`](./development-guide.md).
- Architecture by part: [`architecture-backend.md`](./architecture-backend.md), [`architecture-integrations.md`](./architecture-integrations.md), [`architecture-web.md`](./architecture-web.md), [`architecture-teleterm.md`](./architecture-teleterm.md), [`architecture-rust-rdp.md`](./architecture-rust-rdp.md).
- RFDs: `/rfd/` (230 design docs, search by filename keyword).
- Code conventions deep dive: [`development-guide.md` §7](./development-guide.md).
- Reviewer priorities (verbatim): [`/AGENTS.md`](../AGENTS.md).
