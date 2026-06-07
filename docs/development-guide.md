# Development Guide

Practical guide to building, running, testing, and contributing to Teleport. This document covers the **whole repo**; per-part deep dives live in `architecture-<part>.md`.

> Current version on this branch (`Makefile:VERSION`): `19.0.0-prealpha.2` — a dev/master version. Release branches use a stable semver.

---

## 1. Prerequisites

The *authoritative* dependency versions live in [`build.assets/versions.mk`](../build.assets/versions.mk). Always match those when developing locally — buildbox containers pin to them.

Top-level toolchain:

| Tool | Required | Notes |
| --- | --- | --- |
| **Go** | 1.25.10 (per `go.mod`) | Use `goenv` / system Go matching `build.assets/versions.mk:GOLANG_VERSION`. |
| **Rust** | 1.94.0 (per `rust-toolchain.toml`) | `rustup` will auto-install the pinned toolchain. Targets needed: `wasm32-unknown-unknown` (browser RDP), `x86_64-pc-windows-gnu` (cross-compile for Windows). |
| **Node.js** | `^24` (per root `package.json`) | Used for the web UI build. Pin from `versions.mk:NODE_VERSION`. |
| **pnpm** | 10.32.1 | Activate via Corepack: `corepack enable pnpm`. Locked through `packageManager` in `package.json`. |
| **libfido2** | Latest | For WebAuthn / Touch ID support in `tsh`. Build flag: `FIDO2=dynamic\|static\|off`. |
| **pkg-config** (or `pkgconf`) | Latest | Detects libfido2/etc. The Makefile auto-discovers either. |
| **helm** + `helm-unittest 0.2.11` | Latest helm | For `make test-helm`. |
| **bats** (`bats-core`) | matches `build.assets/Dockerfile:BATS_VERSION` | For shell-script tests (`make test-sh`). |
| **Docker** | Recent | Optional — `make -C build.assets build-binaries` runs a containerised build. |
| **Make** | GNU make ≥ 4.0 | Default. |
| **`buf`** | Latest | For `make grpc` (proto regeneration); managed via `buf.yaml`. |
| **`cbindgen`** | Pulled by Cargo automatically | For Rust → C header generation. |

For a **macOS** walk-through, see `BUILD_macos.md` (Homebrew-based). Increase ulimit (`ulimit -n 2560`) before running the full test suite.

---

## 2. First Build

### 2a. Dockerised (most reliable, slower)

```bash
make -C build.assets build-binaries
```

Builds Linux binaries matching the host architecture. **Cannot cross-compile** between architectures from this entry point. Use the cross-compiling buildbox for that — see `build.assets/buildbox/` and `build.assets/buildbox/cross-compile.mk`.

### 2b. Local (faster, requires the prereqs above)

```bash
# Compile everything (debug binaries) — output in ./build/
make all

# Compile production binaries (-w -s -trimpath, strips debug info, builds web assets)
make full

# Compile a single binary (the Makefile has per-binary phony targets):
make build/teleport
make build/tctl
make build/tsh           # tsh dynamically links libfido2 by default
make build/tbot
make build/teleport-update
make build/fdpass-teleport   # the Rust FD-passing helper
```

The `make full` target also builds the React web UI via `pnpm` and embeds it in the Go binary (`webassets_embed.go`). Skip with `WEBASSETS_SKIP_BUILD=1` to speed up Go-only iteration.

### 2c. Hot reload

```bash
go install github.com/githubnemo/CompileDaemon@latest
make teleport-hot-reload
```

Rebuilds and restarts `teleport start` on every Go file change.

### 2d. Code generation

After changing any `*.proto`:

```bash
make grpc        # regenerates go/ ts/ connect-go/ gogo bindings into gen/ and api/gen/
```

After changing operator CRDs:

```bash
make -C integrations/operator crd   # regenerates apis/resources/{v1,v2,v3,v5}/ + config/crd/bases/
```

After changing Terraform schema:

```bash
make -C integrations/terraform gen
```

After changing audit-event types (regenerates the `docs/pages/reference/audit-events.mdx` reference):

```bash
pnpm --filter=@gravitational/teleport event-reference
```

---

## 3. Build Variants

| Variant | Trigger | Notes |
| --- | --- | --- |
| **Debug** | `TELEPORT_DEBUG=true make build/teleport` | Disables optimization (`-gcflags=all="-N -l"`). |
| **FIPS** | `FIPS=1 make full` | Tags with `fips`, switches Rust `rdpclient` to BoringSSL (`fips` feature). Requires `BORING_BSSL_FIPS_SYSROOT` when cross-compiling. |
| **Enterprise** | `make full-ent` | Requires the `e/` submodule. Builds with enterprise webassets (`webassets_embed_ent.go`). |
| **macOS app bundles** | `make tsh-app`, `make tctl-app` | Codesigns into `.app` bundles using entitlements from `build.assets/macos/`. |
| **Hot reload** | `make teleport-hot-reload` | Requires `CompileDaemon` on `$PATH`. |
| **Cross-compile** | `BUILDBOX_MODE=cross make ARCH=arm64 …` | Uses the cross-compiling buildbox. Loads `build.assets/buildbox/cross-compile.mk`. |
| **Without BPF** | Automatic on non-`linux/{amd64,arm64}` | `common.mk:with_bpf` decides. |
| **Without webassets** | `WEBASSETS_SKIP_BUILD=1 make all` | Skip the long `pnpm build-ui` step. |
| **FIDO2 linking** | `FIDO2=dynamic\|static\|off make build/tsh` | Static linking is what release artifacts use. |

The release dance happens via the `release*` targets:

```
release-amd64 / -arm / -arm64 / -386            # Linux per-arch
release-darwin / -darwin-arm64 / -darwin-amd64  # macOS (signed)
release-windows / release-windows-unsigned      # Windows
release-connect                                 # Teleport Connect (Electron) packaging
```

Each `release-*` runs `clean → full → build-archive → build-update-archive`. macOS targets additionally codesign + notarise via `darwin-signing.mk`.

---

## 4. Running Locally

```bash
sudo mkdir -p -m0700 /var/lib/teleport
sudo chown $USER /var/lib/teleport
./build/teleport configure | sudo tee /etc/teleport.yaml > /dev/null
./build/teleport start --config=/etc/teleport.yaml
```

For a one-process dev cluster *without* installing anything, use the in-tree helper:

```go
// from a Go test:
import "github.com/gravitational/teleport/tool/teleport/testenv"
// see testenv.MakeTestServer(...) for an in-process auth+proxy+node combo
```

`testenv` is also how integration tests spin a real cluster.

---

## 5. Testing

The umbrella `make test` runs **everything**:

```
make test
├── make test-helm                # helm-unittest on examples/chart/**
├── make test-sh                  # bats tests
├── make test-api                 # tests inside api/ module
├── make test-go                  # tests inside root module
├── make test-rust                # cargo test for the rust workspace
├── make test-operator            # integrations/operator tests
└── make test-terraform-provider  # integrations/terraform tests
```

Useful sub-targets:

| Target | Purpose |
| --- | --- |
| `make test-go` | Root-module Go tests (use `gotestsum` from `build.assets/tools/`). |
| `make test-api` | API-module tests. |
| `make test-bpf` | eBPF integration test (Linux + root only). |
| `make test-helm` | `helm-unittest 0.2.11` against `examples/chart/**`. |
| `make test-rust` | `cargo test --workspace`. |
| `make test-operator` | Operator unit + integration tests (needs envtest binaries). |
| `make test-terraform-provider` | Provider tests against a test Teleport server. |

Web UI:

```bash
pnpm test                   # jest
pnpm tdd                    # jest --watch
pnpm test-coverage          # jest --coverage + a script to print a coverage link
pnpm test-update-snapshot   # snapshot refresh
pnpm storybook              # storybook dev server
pnpm storybook-smoke-test   # CI smoke check
pnpm test-storybook         # full storybook tests via @storybook/test-runner
```

End-to-end (Playwright):

```bash
pnpm --filter e2e test       # or: cd e2e && pnpm playwright test
```

Selective Go tests (the `difftest` tool):

```bash
go -C build.assets/tooling tool -n difftest    # path to the binary
# `difftest` runs only the tests touched by a git diff; see build.assets/tooling/cmd/difftest/README.md
```

---

## 6. Linting & Formatting

| Layer | Tooling | Command |
| --- | --- | --- |
| Go | `golangci-lint` driven by `.golangci.yml` | `make lint-go` (see Makefile) |
| Go imports | `gci` (from `build.assets/tools/gci/`) | run via lint target |
| Proto | `buf lint` + custom plugin `build.assets/tooling/cmd/buf-plugin-linters/` | `make lint-proto` |
| TypeScript | `oxlint` (NOT eslint) + `oxfmt` (NOT prettier) | `pnpm lint`, `pnpm lint-fix`, `pnpm format`, `pnpm format-check` |
| TS type check | `tsgo --build` (native TS compiler) with legacy fallback `tsc --build` | `pnpm type-check`, `pnpm type-check-legacy` |
| Rust | `cargo fmt` + `cargo clippy` | `cargo fmt --check`, `cargo clippy --workspace --all-targets` |
| Helm charts | `helm lint` + `chart-testing` | `make lint-helm` |
| Terraform | `tflint` (config in `.tflint.hcl`) | manual |
| Docs (prose) | `vale` (config in `docs/.vale.ini`, styles in `docs/vale-styles/`) | manual / CI |
| Spellcheck | `cspell` (config in `docs/cspell.json`) | `pnpm exec cspell …` |
| Secrets / vulns | `trivy` (ignore list `.trivyignore`) | CI |

Important: `oxlint`/`oxfmt` are required — `eslint`/`prettier` are *not* the formatters here despite eslint deps existing for Storybook plugin compatibility.

---

## 7. Code Style & Conventions

(Distilled from `AGENTS.md`, `CONTRIBUTING.md`, and tribal knowledge that's visible in the codebase.)

### 7a. Error handling (Go)

- **Always wrap errors with `gravitational/trace`**: `return nil, trace.Wrap(err)` or `trace.BadParameter("…")`. Never return bare `errors.New`/`fmt.Errorf`. The wrapper maps to HTTP/gRPC codes and carries stack traces.
- `trace.NotFound`, `trace.AccessDenied`, `trace.BadParameter`, `trace.AlreadyExists`, `trace.LimitExceeded`, etc., have specific semantic meaning that the proxy / web layer reads.

### 7b. Time

- Inject clocks: `lib/service` builds an `*service.TeleportProcess` that carries a `clockwork.Clock`. Pass it everywhere. In tests, use `clockwork.NewFakeClock()` to advance time manually.

### 7c. Logging

- Use `log/slog` (Go stdlib) via helpers in `lib/utils/log/`. There are OS-specific writers in `lib/utils/log/eventlog/` (Windows Event Log) and `lib/utils/log/oslog/` (macOS unified logging).
- Don't log secrets; **`AGENTS.md` flags secret leakage as a critical review category**.

### 7d. RBAC

- Authorization checks live in `lib/authz/`. Don't reimplement role evaluation locally; call `AccessChecker.CheckAccess(...)`.

### 7e. Resource types

- All cluster-state types live under `api/types/` and are protobuf-defined under `api/proto/teleport/<resource>/`.
- New resources should use the v1-style per-resource gRPC services (see `lib/auth/<resource>/`) — the monolithic legacy `AuthService` is being decomposed.

### 7f. Reviews (`AGENTS.md`)

For AI reviewers / code reviewers, focus on:

- Authentication/authorization bypasses
- Secret leakage, unsafe logging, credential exposure
- Unsafe defaults in security-sensitive areas
- Injection (SQL, command, template, path traversal, SSRF)
- Insecure crypto / key handling
- Privilege escalation / sandbox escapes
- Data corruption, durability failures, irreversible loss
- Concurrency hazards causing outages / races
- Reliability regressions: crash loops, panics, deadlocks, unbounded retries

*Ignore* style nits, micro-perf, readability nits unless tied to a significant failure.

### 7g. Documentation

When working on a product area, *consult `docs/pages/`* (the user-facing docs) for the intended UX — implementation should match.

---

## 8. Contributing Workflow

From `CONTRIBUTING.md`:

1. Comment on (or open) a GitHub issue describing the proposed change.
2. Wait for maintainer agreement before writing significant code.
3. Fork → write code → open PR.
4. Maintainers review; **historically the team creates a "buddy PR" that incorporates contributor changes** rather than merging directly. (See `CONTRIBUTING.md`.)

Dependency additions need maintainer pre-approval **and** an approved license check (see `CONTRIBUTING.md:Adding dependencies`).

**Discussions** (GitHub Discussions) come before issues for open-ended questions or design ideas.

**Design changes** of any significant scope go through the **RFD process** (`rfd/`):
1. Copy `rfd/0000-rfds.md` as a template.
2. Allocate the next RFD number.
3. Submit the RFD as a PR; iterate in the PR review.
4. RFD state transitions: `draft → implemented` (or `deprecated`).

---

## 9. Working on Specific Parts

### Backend (`lib/`, `api/`, `tool/`, `proto/`)

```bash
# Build just the daemon, no web assets, for fast Go iteration:
WEBASSETS_SKIP_BUILD=1 make build/teleport

# Re-run a single Go package:
go test ./lib/auth/... -run TestSomething -v
```

### Web UI (`web/`)

```bash
pnpm install                       # first time
pnpm --filter=@gravitational/teleport start    # dev server with HMR
# … or use the umbrella:
pnpm start-teleport                # equivalent to above
```

This serves the React UI against a *running* Teleport proxy (you'll need a local cluster). The script `web/scripts/run-storybook.sh` runs Storybook for isolated component dev.

### Teleterm (Electron — `web/packages/teleterm/`)

```bash
pnpm install
pnpm start-term                    # electron-vite dev (renderer hot-reload, main auto-restart)
pnpm build-term                    # build the Electron app
pnpm package-term                  # build + package (electron-builder → installer)
```

The Teleterm Go daemon lives at `lib/teleterm/` — build it with `make build/teleport` (it's part of the main binary in dev) or it's bundled with the Electron app at packaging time.

### Rust RDP (`lib/srv/desktop/rdp/`)

```bash
make rdpclient            # builds rdp-client and rdp-decoder as staticlibs
make rdpdecoder           # decoder only
make build-ironrdp-wasm   # builds the WASM crate for the browser
```

Native rdpclient links into the Go binary via CGo; `cbindgen` generates the C header at build time.

### Integrations

```bash
# Operator
make -C integrations/operator build
make -C integrations/operator test
make -C integrations/operator crd     # regenerate CRDs

# Terraform provider
make -C integrations/terraform build
make -C integrations/terraform docs   # regenerates DOCS.md from schema
make -C integrations/terraform test

# Access plugins (one example)
make -C integrations/access/slack build
```

### eBPF (`bpf/`)

eBPF programs only build inside the buildbox (`Dockerfile-bpf`). To regenerate the bytecode:

```bash
make bpf-bytecode
```

To check it's up to date:

```bash
make bpf-up-to-date
```

`update-vmlinux-h` refreshes the kernel header.

---

## 10. Running Tests Against a Local Cluster

For features that need a real cluster (e.g. integrations, web UI):

```bash
./build/teleport start --config=/path/to/teleport.yaml
# in another terminal:
./build/tctl tokens add --type=node
# in another terminal:
./build/teleport start --token=<token> --auth-server=auth.example.com:3025 --config=/dev/null --roles=node
```

For ephemeral test clusters in tests, use `tool/teleport/testenv` (Go) or `integration/helpers/` (also Go).

---

## 11. Helpful In-Tree Tools

| Tool | Path | What it does |
| --- | --- | --- |
| `gotestsum` | `build.assets/tools/gotestsum/` | Pretty test output for `make test-go`. |
| `gci` | `build.assets/tools/gci/` | Import-grouping (called from the Go lint target). |
| `goda` | `build.assets/tools/goda/` | Package dependency analyzer. |
| `benchstat` | `build.assets/tools/benchstat/` | Benchmark comparisons. |
| `helm-janitor` | `build.assets/tooling/helm-janitor/` | Helm chart version-syncing (used by `make helm-version`). |
| `apiversion` | `build.assets/tooling/cmd/apiversion/` | Generates `api/version.go` from `Makefile:VERSION`. |
| `update-plist-version` | `build.assets/tooling/cmd/update-plist-version/` | Bumps version in macOS `tsh.app` plists. |
| `benchfind` | `build.assets/tooling/cmd/benchfind/` | Discovers benchmarks for `make bench`. |
| `buf-plugin-linters` | `build.assets/tooling/cmd/buf-plugin-linters/` | Custom buf lint rules for Teleport-specific style. |
| `difftest` | `build.assets/tooling/cmd/difftest/` | Selective Go test runner by git diff. |
| `render-helm-ref` | `build.assets/tooling/cmd/render-helm-ref/` | Generates Helm chart docs from `values.yaml`. |
| `resource-ref-generator` | `build.assets/tooling/cmd/resource-ref-generator/` | Generates docs for Teleport resources from Go struct tags. |
| `gobuildverify` | `build.assets/tooling/cmd/gobuildverify/` | Verifies Go binary build provenance (see `spec.md`). |
| `dump-preset-roles` | `build.assets/dump-preset-roles/` | Regenerates `gen/preset-roles.json`. |

---

## 12. Common Tasks

```bash
# Format Go code (auto)
go fmt ./...

# Run the Go fuzzers
make fuzz        # (where defined; see fuzz/)

# Check FIDO2 / libfido2 setup
make build-fido2 print-fido2-pkg-path

# Inspect what BPF support has been compiled in
make diag-bpf-vars

# Build inside the cross-compile buildbox (Linux arm64 example)
make -C build.assets enter            # opens a shell in the buildbox
ARCH=arm64 BUILDBOX_MODE=cross make full

# Regenerate the audit-events reference doc (docs/pages/reference/audit-events.mdx)
pnpm --filter=@gravitational/teleport event-reference

# Clean everything
make clean
```

---

## 13. Where to Look When Stuck

1. **`AGENTS.md`** — what reviewers care about.
2. **`rfd/`** — `grep` by keyword for the design conversation.
3. **`docs/pages/`** — the user-facing docs match the intended UX.
4. **`build.assets/versions.mk`** — exact pinned tool versions.
5. **`build.assets/README.md`** — buildbox details.
6. **`examples/chart/CONTRIBUTING.md`** — Helm chart conventions.
7. **`integrations/operator/CONTRIBUTING.md`** — operator-specific conventions.
8. **`docs/architecture-<part>.md`** (generated by this workflow) — per-part deep dives.
9. **The `e/` submodule** — many advanced features are enterprise-only. If a feature exists in the docs but not in this checkout, it's likely in `e/`.
