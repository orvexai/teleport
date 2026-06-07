# Build, Deployment, CI/CD & Operations

Scope: build orchestration, buildbox/cross-compile images, in-tree CLI tooling, code
generation, GitHub Actions CI/CD, release/versioning, deployment recipes
(Helm/Terraform/systemd/launchd), observability, and security scanning.

All paths are absolute from repo root `/home/daniel/repos/teleport`.

---

## 1. Top-level Makefile

`Makefile` (2121 lines) is the primary build orchestrator. It includes
`common.mk`, `version.mk`, `darwin-signing.mk`, and (when running in cross
buildbox) `build.assets/buildbox/cross-compile.mk`. `VERSION=19.0.0-prealpha.2`
is declared here (line 16). Default goal is `all` (line 9).

### Make-target inventory

| Target | Purpose |
|---|---|
| `all` (default) | Build all OSS binaries in dev mode (no webasset build, no notarization). Drives `$(BINARIES)`. |
| `binaries` | Build all binaries in `$(BINARIES)` (per-OS list: linux/darwin add `teleport tctl tsh tbot fdpass-teleport teleport-update`; windows only `tsh tctl`). |
| `full` | Production build: forces `WEBASSETS_SKIP_BUILD=0`, builds webassets, then `all`. |
| `full-ent` | Same as full but builds enterprise webassets and recurses into `e/Makefile`. |
| `$(BUILDDIR)/teleport` | Builds the teleport server. Tags: `grpcnotrace webassets_embed $(PAM_TAG) $(FIPS_TAG) $(BPF_TAG) $(SESSIONHELPER_EMBED_TAG) $(WEBASSETS_TAG) $(RDPCLIENT_TAG) $(PIV_BUILD_TAG)`. Depends on `ensure-webassets`, `rdpclient`, `session/reexec/embed/sessionhelper`. |
| `$(BUILDDIR)/tctl` | Builds tctl with libfido2/PIV/touchid tags. |
| `$(BUILDDIR)/tsh` | Builds tsh; depends on `rdpdecoder`. |
| `$(BUILDDIR)/tbot` | Machine ID agent; CGO_ENABLED=0 except on Windows. |
| `$(BUILDDIR)/teleport-update` | Auto-updater; always CGO_ENABLED=0. |
| `$(BUILDDIR)/fdpass-teleport` | Cargo-built Rust binary in `tool/fdpass-teleport`, copied into BUILDDIR. |
| `$(BUILDDIR)/sessionhelper` + `session/reexec/embed/sessionhelper` | Linux only; gzipped sessionhelper embedded inside teleport binary. |
| `tsh-app`, `tctl-app` | macOS .app bundle wrappers; signed + notarized by `$(NOTARIZE_TSH_APP)`/`$(NOTARIZE_TCTL_APP)` from `darwin-signing.mk`. |
| `rdpclient` | Cargo-builds the Rust `rdp-client` static lib in `lib/srv/desktop/rdp/rdpclient/`. Linked into `teleport`. |
| `rdpdecoder` | Cargo-builds Rust `rdp-decoder` linked into `tsh`. |
| `build-ironrdp-wasm` | wasm32 build of ironrdp; postprocessed via wasm-opt + wasm-bindgen; output to `web/packages/shared/libs/ironrdp/pkg/`. |
| `build-fido2` | Runs `build.assets/build-fido2-macos.sh build` for macOS libfido2 + cbor + openssl 3.0.19. |
| `bpf-bytecode` | `go generate ./lib/bpf/` (requires clang 14+, libbpf-dev). |
| `update-vmlinux-h` | Regenerates `bpf/vmlinux.h` from current kernel BTF. |
| `teleport-hot-reload` | Runs CompileDaemon for live-reload builds. |
| `clean`, `clean-build`, `clean-ui`, `clean-ent` | Wipe build dirs, node_modules, webassets, cargo cache. |
| `release` | Dispatches to `release-windows` / `release-darwin` / `release-unix` based on OS. |
| `release-unix`, `release-unix-preserving-webassets` | clean + full + build-archive + build-update-archive; recurses into `e/`. |
| `release-darwin` (non-universal) | unsigned -> notarize -> tsh-app/tctl-app -> signed archive. |
| `release-darwin` (universal) | Lipo-combines pre-built arm64+amd64 release tarballs into one. |
| `release-windows`, `release-windows-unsigned` | zip of tsh.exe+tctl.exe, signed via osslsigncode + DigiCert timestamp. |
| `release-connect` | Builds Teleport Connect (Electron) via pnpm `package-term`. macOS-only target. Linux uses `teleterm` target in `build.assets/Makefile`. |
| `release-amd64`, `release-386`, `release-arm`, `release-arm64` | Architecture aliases that re-invoke `release` with `ARCH=`. |
| `build-archive`, `build-update-archive` | Tar/zip the binaries + examples + install script + LICENSE + VERSION + CHANGELOG into `$(RELEASE_DIR)`. Reproducible flags applied if `REPRODUCIBLE=yes`. |
| `pkg` | macOS .pkg installer (teleport-bin + tsh + tctl combined via productbuild, signed/notarized). |
| `rpm`, `oss-deb`, `deb`, `rpm-unsigned` | Calls `build.assets/build-package.sh -p rpm/deb`. |
| `image` | Builds `$(DOCKER_IMAGE):$(VERSION)-$(ARCH)` from `build.assets/charts/Dockerfile` after `clean docker-binaries build-archive oss-deb`. |
| `test` | Top-level test entry: `test-helm test-sh test-api test-go test-rust test-operator test-terraform-provider`. |
| `test-go` | Runs `test-go-unit test-go-touch-id test-go-vnet-daemon test-go-tsh test-go-chaos`. Uses `gotestsum`, writes JUnit XML to `$(TEST_LOG_DIR)`. |
| `test-go-unit`, `test-go-root`, `test-go-flaky`, `test-go-tsh`, `test-go-chaos`, `test-go-touch-id`, `test-go-vnet-daemon`, `test-go-bench`, `test-go-bench-root` | Sub-targets for unit/root/flaky/bench tests. |
| `test-api`, `test-operator`, `test-terraform-provider`, `test-terraform-provider-mwi`, `test-kube-agent-updater`, `test-access-integrations`, `test-event-handler-integrations`, `test-integrations-lib`, `test-teleport-usage`, `test-rust`, `test-sh`, `test-helm` | Per-module test entry points. |
| `integration`, `integration-root`, `integration-kube` | Integration tests (regular / TestRoot-only / TestKube). Need TTY. |
| `e2e-aws`, `e2e-binaries` | e2e suite + parallel binary builder script. |
| `lint` | Runs `lint-api lint-go lint-kube-agent-updater lint-tools lint-protos lint-no-actions`. |
| `lint-go` | `golangci-lint run -c .golangci.yml` plus separate runs in integrations/terraform & event-handler. |
| `lint-api`, `lint-build-tooling`, `lint-backport`, `lint-kube-agent-updater` | Per-Go-module lint invocations. |
| `lint-sh` | shellcheck (excludes SC2086, SC1091). |
| `lint-helm` | `helm-janitor reference -check` then `helm-janitor lint`. |
| `lint-rust` | `cargo clippy --locked --all-targets -- -D warnings && cargo fmt --check`. |
| `lint-license` | runs `addlicense -check` against AGPL3 (root) and Apache2 (api/). |
| `lint-test-symbols` | Uses `goda tree` to ensure `testing`/`testify` are not in shipped binaries. |
| `lint-protos` (=`protos/lint`) | `buf lint` + `buf lint --config=buf-legacy.yaml api/proto`. |
| `lint-breaking` | `buf breaking . --against` for proto breaking checks. |
| `lint-no-actions` | runs `lint-sh lint-license` (linters without their own CI workflow). |
| `fix-imports`, `fix-imports/host` | `gci write` with standard / Teleport / integrations prefixes. |
| `fix-license` | `addlicense` (write mode). |
| `version`, `update-version`, `setver`, `validate-semver` (in `version.mk`) | Regenerate `gitref.go`, `api/version.go`, helm chart versions, and tsh `Info.plist` `CFBundleVersion`. |
| `update-tag` | `git tag $(GITTAG)` + `git tag api/$(GITTAG)`, also tags `e/` submodule, pushes. |
| `tag-build`, `tag-publish` | Dispatches `tag-build.yaml`/`tag-publish.yaml` in `gravitational/teleport.e` via `gh workflow run`. Uses `IS_CLOUD_SEMVER` / `IS_PROD_SEMVER` to select environment. |
| `docker`, `docker-binaries`, `docker-ui`, `enter`, `enter-root`, `enter/centos7`, `enter/centos7-fips`, `enter/grpcbox`, `enter/node`, `enter/arm` | Wrappers for `make -C build.assets …` that build/run buildbox containers. |
| `grpc` | `make -C build.assets grpc` (runs in grpcbox container). |
| `grpc/host` | `protos/all` then `build.assets/genproto.sh` (no docker). |
| `protos/all`, `protos/build`, `protos/format`, `protos/lint`, `protos/breaking`, `ensure-buf` | proto sub-targets. `ensure-buf` installs buf at the version printed by `build.assets/Makefile`. |
| `protos-up-to-date`, `protos-up-to-date/host`, `crds-up-to-date`, `terraform-resources-up-to-date`, `terraform-module-docs-up-to-date`, `icons-up-to-date`, `go-generate-up-to-date`, `derive-up-to-date`, `cli-docs-up-to-date`, `audit-event-reference-up-to-date`, `access-monitoring-reference-up-to-date`, `resource-docs-up-to-date`, `bpf-up-to-date` | "Generated files in sync" checks. Pattern: `must-start-clean/host` -> regenerate -> `git diff --quiet`. Used by CI. |
| `derive` | Runs `goderive` (`build.assets/tooling/cmd/goderive`) over `api/types`, `api/types/discoveryconfig`, `api/types/accesslist`, `api/types/userloginstate`. |
| `go-generate` | `go generate ./lib/...`. |
| `gen-docs` | Bundle: `gen-resource-docs`, `audit-event-reference`, terraform docs, CRD docs, helm chart-ref. |
| `gen-resource-docs` | Runs `build.assets/tooling/cmd/resource-ref-generator` with `config.yaml`. |
| `cli-docs`, `cli-docs-tsh`, `cli-docs-tbot`, `cli-docs-teleport`, `cli-docs-tctl` | Build each tool with `-tags docs` and pipe `help` to `docs/pages/reference/cli/*.mdx`. |
| `audit-event-reference` | `pnpm run -C ./web/packages/teleport event-reference`. |
| `access-monitoring-reference` | Runs `gen-athena-docs` -> `docs/pages/includes/access-monitoring-events.mdx`. |
| `install` | cp binaries to `$(BINDIR)` (default `/usr/local/bin`) + create `$(DATADIR)` (default `/usr/local/share/teleport`). |
| `backport PR=1234 TO=branch/1,branch/2` | `cd assets/backport && go run main.go -pr -to`. |
| `changelog` | `go run github.com/gravitational/shared-workflows/tools/changelog@latest`. |
| `create-github-release` | Builds release notes via `shared-workflows/tools/release-notes` and `gh release create`. |
| `docs-test`, `docs-test-whitespace`, `docs-fix-whitespace` | docs sanity checks/fixes (busybox docker). |
| `run-etcd` | Builds + runs etcd from `.github/services/Dockerfile.etcd`. |
| `print-version`, `print-go-version`, `print-rust-toolchain-version`, `print-wasm-bindgen-version`, `print-darwin-signing-vars` | Diagnostic prints. |
| `init-submodules-e` | `git submodule init e && git submodule update`. |
| `go-mod-tidy-all` | `find . -name go.mod -execdir go mod tidy`. |
| `dump-preset-roles` | Runs `build.assets/dump-preset-roles/main.go` then a focused web test. |
| `benchstat` | Runs `benchstat $(BENCH_FILES)`. |
| `test-env-leakage` | Sets `BUILD_SECRET=FAKE_SECRET`, builds, greps binaries to ensure no env leak. |
| `test-compat` | `build.assets/build-test-compat.sh` runs teleport binaries on ubuntu 14/16/18/20/22, centos:7 etc. |

Key build tags (assembled in Makefile lines 67-260):
`FIPS_TAG=fips`, `PAM_TAG=pam`, `BPF_TAG=bpf` (linux/amd64|arm64 only),
`RDPCLIENT_TAG=desktop_access_rdp`, `TSH_RDP_DECODER_TAG=rust_rdp_decoder`,
`LIBFIDO2_BUILD_TAG=libfido2[ libfido2static]`, `TOUCHID_TAG=touchid` (when `TOUCHID=yes`),
`VNETDAEMON_TAG=vnetdaemon`, `PIV_BUILD_TAG=piv`, `SESSIONHELPER_EMBED_TAG=sessionhelper_embed`
(linux), plus `grpcnotrace`, `webassets_embed`, `kustomize_disable_go_plugin_support`.

`common.mk` (67 lines) decides BPF/CGO, exposes `$(GOTESTSUM)`, `$(GCI)`,
`$(GODA)`, `$(BENCHSTAT)` from `build.assets/tools/` and `$(HELMJANITOR)` from
`build.assets/tooling/`.

`version.mk` (39 lines) writes `gitref.go` and `api/version.go`, calls
`helm-janitor update-version`, runs `update-plist-version` on every
`tsh.app/Contents/Info.plist`, and validates semver via
`build.assets/tooling/cmd/check`.

`darwin-signing.mk` (182 lines) defines TEAMID/DEVELOPER_ID and bundle-IDs
for `prod/build` (Gravitational Inc., team `QH8AA5B8UP`) and `stage/build`
(Ada Lin, team `K497G57PDJ`) environments. Defines `notarize_binaries_cmd`,
`notarize_app_bundle`, `notarize_pkg` which all use `codesign` + `xcrun
notarytool submit`.

---

## 2. Buildbox / cross-compilation Dockerfiles

All under `build.assets/`. Pinned tool versions live in
`build.assets/versions.mk`:
`GOLANG_VERSION=go1.25.10`, `GOLANGCI_LINT_VERSION=v2.10.1`,
`NODE_VERSION=24.13.0`, `WASM_OPT_VERSION=0.116.1`, `LIBPCSCLITE_VERSION=1.9.9-teleport`,
`DEVTOOLSET=devtoolset-12`, `BUF_VERSION=v1.66.0`, `GOGO_PROTO_TAG=v1.3.2`,
`PROTOC_VERSION=26.1`.

| Dockerfile | Purpose |
|---|---|
| `build.assets/Dockerfile` | Generic CI buildbox on `buildpack-deps:22.04`. Stages: `libfido2` (custom static build of libudev-zero 1.0.3, libcbor 0.11.0, openssl 3.0.19, fido2 1.15.0), and a final buildbox with Go, Rust, Node, golangci-lint, wasm-opt, wasm-bindgen, helm, protoc. Tagged `BUILDBOX` and pushed to ghcr.io. Used for tests/lint/protogen, not releases. |
| `build.assets/Dockerfile-arm` | ARM (32-bit `armhf`) cross-compile box; debian:11. Installs `gcc-arm-linux-gnueabihf` (armhf) and `gcc-aarch64-linux-gnu`. Used for `release-arm`. |
| `build.assets/Dockerfile-centos7` | CentOS 7 + devtoolset-12 cross-build box, two targets: `buildbox` and `buildbox-fips` (BoringCrypto). Old glibc 2.17 for max distro compat. Used for all official linux amd64/arm64 releases (cannot cross-compile arch; runs natively per-arch). Sources `BUILDBOX_CENTOS7_ASSETS` image. |
| `build.assets/Dockerfile-centos7-assets` | Companion image with prebuilt long-running assets (libpcsclite static, libbpf, etc.) for centos7 buildbox; rebuilt by scheduled workflow. |
| `build.assets/Dockerfile-bpf` | Minimal debian:12 + clang + llvm + libbpf-dev image for generating BPF bytecode via `go generate ./lib/bpf/`. Invoked by `make bpf-bytecode`. |
| `build.assets/Dockerfile-grpcbox` | `docker.io/golang:1.25.10` + npm + buf + protobuf-ts plugins + protoc + grpc-tools. Caches `npm exec` and `go run` outputs. Used by `make grpc` (top-level Makefile -> `make -C build.assets grpc -> $(GRPCBOX_RUN) make grpc/host`). |
| `build.assets/Dockerfile-node` | `node:${NODE_VERSION}-bullseye`-based webassets/Teleport Connect builder. Bullseye glibc 2.31 baseline. Has corepack pnpm enabled. Used by `make webassets`, `make ui`, `make teleterm`. |
| `build.assets/buildbox/Dockerfile-thirdparty` | Multi-stage build of crosstool-NG cross-compilers + libs for the "ng" buildbox. Stages: `crosstoolng`, `compilers`, `libs`. Pushed as `BUILDBOX_THIRDPARTY`. |
| `build.assets/buildbox/Dockerfile` | The "buildbox-ng" cross-compile buildbox layered on top of `BUILDBOX_THIRDPARTY`. Enables true cross-compile of `teleport` on a single host. |
| `build.assets/charts/Dockerfile` | DEPRECATED (>=v15 unused) - ubuntu:20.04 + dumb-init + ca-certificates + debug tools "heavy" image. |
| `build.assets/charts/Dockerfile-distroless` | Production OCI image based on `gcr.io/distroless/cc-debian12`. Multi-arch via deb fetched per `${TARGETARCH}` using `fetch-debs`. |
| `build.assets/charts/Dockerfile-distroless-fips` | Same as distroless but installs FIPS deb. |
| `build.assets/charts/Dockerfile-tbot-distroless` | tbot-only distroless image. |
| `build.assets/charts/Dockerfile-tbot-distroless-fips` | tbot FIPS distroless image. |

`build.assets/Makefile` orchestrates buildbox creation, `release-amd64/arm64`
(via centos7 buildbox), `release-arm` (via arm buildbox), `release-amd64-fips`
(via centos7-fips), `webassets` (via node), `bpf-bytecode` (via bpf), `grpc`
(via grpcbox), and `teleterm` (Connect via node).

`build.assets/arch.mk` normalizes `HOST_ARCH` (`uname -m`) -> `RUNTIME_ARCH`
(`x86_64`->`amd64`, `aarch64`/`arm64`->`arm64`).

Helper scripts (`build.assets/`):
- `build-common.sh` - sourced; `find_or_fetch_tarball` etc.
- `build-package.sh` - rpm/deb/pkg builder (FPM-like).
- `build-pkg-app.sh` - macOS .pkg for tsh/tctl with `pkgbuild`+`productbuild`.
- `build-fido2-macos.sh` - cross-arch macOS libfido2 (with cbor + openssl 3.0.19) cached locally; called by `make build-fido2`.
- `build-test-compat.sh` - runs built binaries inside ubuntu 14.04/16.04/18.04/20.04/22.04 + centos:7 containers to assert glibc symbol compat.
- `build-webassets-if-changed.sh` - SHA-based rebuild of webassets target if `web/`, `webassets/{oss-sha,e-sha}` changed.
- `build-e2e-binaries.sh` - parallel `make teleport tctl` + go build webauthnmock-tagged tsh, logs to `build-logs/`.
- `genproto.sh` - drives gRPC generation; called by `make grpc/host`.
- `please-run.sh` - used by all `*-up-to-date` checks to tell users what command to run.
- `changelog.sh`, `dump-preset-roles/`, `install`, `keychain-setup.sh`, `unused-docs-assets.sh`.

---

## 3. In-tree build tooling

All Go binaries under `build.assets/tooling/cmd/<name>/`, built lazily by
top-level Makefile (or via `go run`). Output dir `build.assets/tooling/bin/`.

| Binary | Purpose |
|---|---|
| `apiversion` | Parses semver from `os.Args[1]`, prints a `package api` template into `api/version.go`. Used by `version.mk:setver`. |
| `accessgraph-rewrite-generated` | Rewrites generated Go code for AccessGraph (post-processing protoc output). |
| `benchfind` | Locates Go packages with `Benchmark*` tests honoring build tags; consumed by `make test-go-bench*`. |
| `buf-plugin-linters` | Custom `buf` lint plugin enforcing pagination conventions (`check.Main(paginationSpec)`). Bound by buf config. |
| `check` | Semver / git-tag validator; used by `validate-semver` and `update-tag` (`-check valid -tag $(GITTAG)`). |
| `difftest` | Walks `git diff` against a base branch and emits a `SUBJECT=` list of changed test funcs for `make test ADDFLAGS='-count N'`. Backs `.github/actions/difftest`. |
| `gen-athena-docs` | Generates `docs/pages/includes/access-monitoring-events.mdx` from `eventschema`. |
| `gobuildverify` | Reads `debug/buildinfo` from a binary and asserts expressions on module versions / build settings. Used in CI to verify shipped binary metadata. |
| `goderive` | Wraps `derive.Plugin` set with `teleportequal` and `deepcopy` plugins; generates `Equal()`/`DeepCopy()` functions for selected api types. Backs `make derive`. |
| `govulncheck-report` | Wraps `govulncheck`, splits args, formats output for CI. |
| `helm-janitor` | The Helm CI swiss-army knife: `lint`, `test`, `reference`, `update-version`. Bound to `$(HELMJANITOR)` in `common.mk` and called from `make test-helm`, `make lint-helm`, `examples/chart/Makefile`. |
| `protoc-gen-eventschema` | protoc plugin that emits event-schema reference for audit events. Wired into buf gen config. |
| `query-latest` | Queries GitHub for latest Teleport release tag (used by post-release / changelog). |
| `render-helm-ref` | Reads a Helm chart and emits a markdown reference (`-chart`, `-output`). Invoked indirectly via `helm-janitor reference`. |
| `render-tests` | Filter: consumes `go test -json` on stdin, renders / aggregates / reports flaky tests for `make test-go-flaky`. |
| `rerun` | Runs a command N times with timeout; powers flaky test detection (`-n $(FLAKY_RUNS) -t $(FLAKY_TIMEOUT)`). |
| `resource-ref-generator` | Reads `config.yaml` and emits Markdown reference for tctl resources (drives `gen-resource-docs`). |
| `update-plist-version` | Rewrites `CFBundleVersion` / `CFBundleShortVersionString` in macOS `tsh.app/Contents/Info.plist`. |

External tools pinned via `build.assets/tools/<name>/go.mod` (one go module per
tool, used with `go -C … tool -n`): `gci`, `gotestsum`, `goda`, `benchstat`.

`build.assets/charts/`: production OCI Dockerfiles + `fetch-debs` + `smoke_tests`.

`build.assets/macos/`: `install` script, per-tool `tsh/`, `tshdev/`, `tctl/`,
`tctldev/` skeletons each containing `tsh.app/Contents/{Info.plist,
embedded.provisionprofile, Library, PkgInfo, Resources}`, entitlements files, and
`scripts/{tsh,tctl}/postinstall`. Drive `tsh-app`/`tctl-app` make targets.

`build.assets/download-hashes/osslsigncode.sha256`: sole pinned tool hash, used
by windows signing path.

---

## 4. Code generation pipeline

### `make grpc` (the canonical entry)

1. Top-level `make grpc` -> `make -C build.assets grpc`.
2. `build.assets/Makefile:grpc` builds the `grpcbox` image
   (`Dockerfile-grpcbox`) then runs `$(GRPCBOX_RUN) make grpc/host`.
3. `grpc/host` (top-level) runs `protos/all` (build/lint/format with `buf`)
   then `build.assets/genproto.sh`.
4. `genproto.sh` invokes `buf generate` with the four config files:
   - `buf-go.gen.yaml` -> Go server stubs into `gen/proto/go/` and `api/gen/proto/go/`.
   - `buf-gogo.gen.yaml` -> gogoproto Go stubs (legacy).
   - `buf-ts.gen.yaml` -> TypeScript stubs (`@protobuf-ts/plugin`) into `gen/proto/ts/`.
   - `buf-connect-go.gen.yaml` -> Connect-Go RPC stubs.
   - `buf-legacy.yaml` -> legacy linter config for `api/proto`.
5. `buf-plugin-linters` enforces pagination rules during `buf lint`.
6. `buf.lock` pins the protobuf module registry; `buf.yaml` is the top-level config.

CI uses `protos-up-to-date` (regenerate + `git diff --quiet`) to ensure
checked-in stubs match `.proto` sources.

### Other generated artifacts

- **CRDs (Kubernetes operator):** `make crds-up-to-date` runs
  `make -C integrations/operator crd-manifests` and `crd-docs`. The operator
  Makefile (line 103, 122) invokes `crdgen` (in `integrations/operator/crdgen/`)
  and `protoc-gen-crd-docs`. Output: CRD manifests + docs MDX.
- **Helm chart reference:** `examples/chart/Makefile` -> `helm-janitor reference`
  which uses `build.assets/tooling/cmd/render-helm-ref`. Output checked under
  `docs/pages/reference/helm-reference/`.
- **Terraform provider reference:** `make terraform-resources-up-to-date` ->
  `make -C integrations/terraform docs`.
- **Terraform module docs:** `make terraform-module-docs-up-to-date` ->
  `make -C integrations/terraform-modules docs`.
- **Resource (tctl) reference:** `make gen-resource-docs` -> `resource-ref-generator`
  with `build.assets/tooling/cmd/resource-ref-generator/config.yaml`.
- **CLI reference:** `make cli-docs` builds each tool with `-tags docs`, pipes
  `help` to `docs/pages/reference/cli/<tool>.mdx`.
- **Derived Go funcs:** `make derive` -> `goderive` regenerates Equal/DeepCopy
  for selected api types.
- **`go generate`:** `make go-generate` runs `go generate ./lib/...` (mostly BPF
  bytecode + stringers + mocks).
- **BPF bytecode:** `make bpf-bytecode` (host) or `make -C build.assets
  bpf-bytecode` (docker) via clang.
- **Icons:** `pnpm process-icons` (web/packages/design/src/Icon/script/script.js).
- **Audit event reference:** `pnpm run -C ./web/packages/teleport event-reference`.
- **Access Monitoring reference:** `make access-monitoring-reference` runs
  `gen-athena-docs`.

`gen/proto/{go,ts}/`, `api/gen/proto/go/`, `lib/bpf/bytecode/*` are checked
in and verified by the `*-up-to-date` CI gates.

---

## 5. CI workflows (`.github/workflows/`)

51 workflows. Bypass workflows ending in `*-bypass.yaml` / `*-merge-queue.yaml`
exist so required checks can succeed under path-filtered skip or merge-queue
contexts (each pairs to a primary workflow with the same `name:`).

### Build workflows

| Workflow | Triggers | Purpose |
|---|---|---|
| `build-api.yaml` | PR/merge-queue on `api/**`, `go.{mod,sum}` | Builds the `api/` Go module standalone. |
| `build-centos7-assets.yaml` | manual + `cron '0 13 * * 0'` (weekly Sun 1pm UTC) | Rebuilds `BUILDBOX_CENTOS7_ASSETS` image and pushes to ghcr.io. Long-running, scheduled. |
| `build-ci-buildbox-images.yaml` | push to master/branch on `build.assets/Dockerfile*`, `build.assets/{Makefile,images.mk,versions.mk}` | Builds & pushes `BUILDBOX`, `BUILDBOX_ARM`, `BUILDBOX_CENTOS7`, `BUILDBOX_NODE` to ghcr.io. |
| `build-ci-service-images.yaml` | push to master on `.github/services/Dockerfile.*`, `fixtures/etcdcerts/*` | Builds & pushes `ci-etcd` image. |
| `build-macos.yaml` | merge-queue on Go/Rust/build files | Native macOS build on `macos-15-xlarge`. |
| `build-macos-bypass.yaml` | path-skipped PRs | Bypass for above. |
| `build-windows.yaml` | merge-queue on Go/rdp-decoder/Cargo files | Native Windows build of tsh on `windows-2022-16core`. |
| `build-windows-bypass.yaml` | path-skipped PRs | Bypass for above. |
| `build-usage-image.yaml` | release `published` event | Builds usage script image, pushes to ECR Public. |

### Test workflows

| Workflow | Triggers | Purpose |
|---|---|---|
| `unit-tests-code.yaml` | push master/branch, PR on `**.go`, `go.{mod,sum}`, `Makefile`, `build.assets/{Makefile,Dockerfile*}`, merge_group | Required `make test-go-unit/tbot/tsh/touchid/chaos/vnet-daemon`. Uploads test-metrics via custom action. |
| `unit-tests-code-bypass.yaml` | path-skipped PR | Required check bypass. |
| `unit-tests-ui.yaml` | PR on `web/**`, gen/proto/{js,ts}, package.json, pnpm-lock, Cargo, tsconfig, jest.config | UI Jest tests. |
| `unit-tests-ui-bypass.yaml` | path-skipped PR | Bypass. |
| `unit-tests-helm.yaml` | PR / merge_group | Helm `quintush/helm-unittest` snapshots. |
| `unit-tests-integrations.yaml` | PR / merge_group | tests in `integrations/lib`, plugin packages. |
| `unit-tests-rust.yaml` | PR / merge_group | `cargo test` for Rust workspace. |
| `integration-tests-non-root.yaml` | PR / merge_group | Required non-root integration tests. |
| `integration-tests-root.yaml` | PR / merge_group | Required root integration tests (`TestRoot*`). |
| `integration-tests-win.yaml` | PR / merge_group | Windows integration tests. |
| `kube-integration-tests-non-root.yaml` | PR / merge_group | Requires alpine 3.20.3; runs `TestKube*`. |
| `aws-e2e-tests-non-root.yaml` | PR / merge_group | Real AWS E2E (RDS/Redshift/EKS) via IAM role assume. |
| `e2e-tests.yaml` | PR/merge_group/push master+branch | OSS+Enterprise E2E orchestrator; calls `e2e-tests-base.yaml`. |
| `e2e-tests-base.yaml` | `workflow_call` | Parameterized E2E base for OSS/Enterprise. |
| `e2e-pr-comment.yaml` | `workflow_call` | Posts PR comment with E2E results. |
| `flaky-tests.yaml` | PR on `**.go` | `rerun -n 3 -t 1h`; uses `difftest` action to scope to changed funcs. Runs on `ubuntu-22.04-16core`. |
| `flaky-tests-bypass.yaml`, `flaky-tests-merge-queue.yaml` | path skip / merge_group | Required bypass. |
| `benchmark-code-nonroot.yaml`, `benchmark-code-root.yaml` | merge_group | `make test-go-bench` / `test-go-bench-root`. |
| `benchmark-code-smoke-nonroot.yaml`, `benchmark-code-smoke-root.yaml` | PR | Single-run smoke benchmarks. |
| `bloat.yaml` | push master/branch on `**.go`, `**.rs`, `go.{mod,sum}`, `Cargo.*`, `Makefile`, `*.mk` | Binary size regression detector. Runs on `ubuntu-22.04-4core`. |
| `os-compatibility-test.yaml` | PR / merge_group | Runs `make test-compat` (binaries on multiple distro containers). |
| `doc-tests.yaml` | PR / merge_group | Docs lint (paths-filter on `docs/**`). |

### Lint workflows

| Workflow | Triggers | Purpose |
|---|---|---|
| `lint.yaml` | PR / merge_group | Big aggregate lint: golangci-lint (api, teleport, assets/backport, build.assets/tooling, integrations/terraform, integrations/event-handler), license, spell, oxlint, oxfmt, type-check (tsgo), Storybook smoke, icons, audit event reference, BPF generated files, CLI reference docs, derived funcs, go-generate freshness, test symbols. |
| `terraform-lint.yaml` | PR / merge_group on `**.tf`, `**.hcl`, manual | Uses `gravitational/shared-workflows/.github/workflows/terraform-lint.yaml`. |

### Release / repo automation

| Workflow | Triggers | Purpose |
|---|---|---|
| `post-release.yaml` | `release` event `released` (excludes prereleases) + manual | After a GitHub release: determines latest flag, kicks downstream artifacts. |
| `update-ami-ids.yaml` | manual + `workflow_call` | Updates AWS AMI IDs in `examples/aws/terraform/AMIS.md` after a release. |
| `update-docs-webhook.yaml` | push master/branch on `docs/**` or `CHANGELOG.md` | Triggers Amplify deploy webhook for docs site. |
| `docs-amplify.yaml` | PR on `docs/**` + manual | Posts Amplify preview URL via AWS OIDC + Amplify API. |
| `changelog.yaml` | PR events (edited/labeled/etc.) | Verifies PR description has a `Changelog:` entry or `no-changelog` label. |
| `changelog-merge-queue.yaml` | merge_group | Bypass for the above. |
| `manual-test-plan.yaml` | PR labeled/edited | Ensures manual test plan checklist is filled. |
| `backport.yaml` | PR closed | Auto-creates backport PRs via shared-workflows backport tool. |

### Repo hygiene / governance

| Workflow | Triggers | Purpose |
|---|---|---|
| `assign.yaml` | `pull_request_target` opened/ready | Auto-assigns reviewers via Workflow Bot. |
| `label.yaml` | `pull_request_target` opened/ready | Auto-labels PR. |
| `check.yaml` | `pull_request_review`/`pull_request_target` | Workflow Bot enforces reviewer rules; invalidates reviews. |
| `check-merge-queue.yaml` | merge_group | Bypass. |
| `cla-assistant.yaml` | issue_comment, pull_request_target, merge_group | CLA signature check. |
| `dismiss.yaml` | `cron '0,30 * * * *'` (every 30 min) | Dismisses stale workflow runs on PRs (workaround for fork PR limitations). |
| `renovate.yaml` | `cron '0 7 * * 3'` (Wed 7am UTC) + manual | Self-hosted Renovate run for Go toolchain (see `.github/renovate.json`). |

Required-for-merge (from `lint.yaml`, build/test bypass pairs naming convention,
and CODEOWNERS gating `.github/workflows/`): all unit/integration/lint workflows
+ macOS/Windows build merge_group runs. Scheduled: `build-centos7-assets`
(weekly), `dismiss` (30-minute), `renovate` (weekly).

---

## 6. Custom GitHub actions / services

### Composite actions (`.github/actions/`)

| Action | Purpose |
|---|---|
| `difftest/action.yml` | Builds `difftest` binary, runs `difftest diff` against `origin/${GITHUB_BASE_REF}`, then `difftest test` to extract changed test names; invokes `make ${target}` with `SUBJECT='…'` and `-count`. Used by flaky-tests. |
| `prepare-workspace/action.yml` | Marks workspace as git safe.directory, exports `go-build` / `go-mod` cache paths and Go version. Used at top of most Go workflows. |
| `upload-test-metrics/action.yml` | Assumes AWS role, runs `gravitational/shared-workflows/tools/ci-normalize` to push JUnit XML in `$GITHUB_WORKSPACE/test-logs/*.xml` to S3 (`tp-278576220453-ci-test-artifacts-storage`). |

### Service definitions (`.github/services/`)

| File | Purpose |
|---|---|
| `Dockerfile.etcd` | `quay.io/coreos/etcd:v${ETCD_VERSION}` with fixture certs from `fixtures/etcdcerts/*.pem`. Built/pushed by `build-ci-service-images.yaml`. Consumed by tests needing etcd (also runnable locally via `make run-etcd`). |

---

## 7. Linting & formatting

### Go (`.golangci.yml`, 605 lines)

`go: '1.25'`, `timeout: 15m`. `default: none`, then explicitly enabled:
`bodyclose`, `depguard`, `errorlint`, `forbidigo`, `forcetypeassert`, `govet`,
`ineffassign`, `misspell`, `nolintlint`, `revive`, `sloglint`, `staticcheck`,
`testifylint`, `unconvert`, `unused`, `usetesting`.

Notable `depguard` rules:
- `cgo` denies CGO-requiring packages (`lib/bpf`, `lib/backend/lite`,
  `lib/cgroup`, `lib/config`, `lib/devicetrust/*`, `session/pam`, `session/uacc`,
  `lib/vnet/daemon`, etc.) from tbot, lib/client, integrations.
- `client-tools` denies `lib/auth`, `lib/cloud`, `lib/srv`, `lib/web` from
  tbot/tctl/tsh/lib/client to keep tools small.
- `logging` denies `sirupsen/logrus`, `golang.org/x/exp/slog`, `aws-sdk-go` v1 in
  favour of `log/slog` / `aws-sdk-go-v2`.
- `template` denies `text/template`/`html/template` (DCE-hostile) in favour of
  `DataDog/datadog-agent/pkg/template/{text,html}`.
- `main` denies `io/ioutil`, `math/rand` (v1), `golang/protobuf`,
  `hashicorp/go-uuid`, `pborman/uuid`, `uber.org/atomic`, `golang.org/x/exp/slices`,
  `hashicorp/go-version`, `golang.org/x/mod/semver`, `microsoftgraph/msgraph-sdk-go`,
  `cloudflare/cfssl`, `golang.org/x/net/context`, `gogo/protobuf/jsonpb`.
- `oidc` mandates `zitadel/oidc/v3`.
- `testify`/`testing`/`go-cmp` rules restrict test-only packages from prod code.
- `api_constants_defaults` enforces strict imports for the `api/{constants,defaults}` packages.
- `session_submodule` isolates `session/` from the rest of the repo.

### TypeScript (`.oxlintrc.jsonc` + `.oxfmtrc.json`)

`oxlint` (Rust-based ESLint) with plugins `typescript`, `react`, `jest`,
`import`, and JS plugins `eslint-plugin-unused-imports`,
`eslint-plugin-storybook`, `eslint-plugin-testing-library`,
`eslint-plugin-jest-dom`. `categories.correctness=off`. Run via
`pnpm oxlint` (`package.json` script).
`oxfmt` (Rust-based Prettier): 80 cols, semi, single quote, 2-space tabs,
trailing comma `es5`, arrow `avoid`. Sort imports with custom groups
(`build`, `shared/**`, `design/**`, `gen-proto-ts/**`). Run via `pnpm format`.
`eslint.config.mjs` re-exports `web/packages/build/eslint.config.mjs` (still used
by editor integrations).

### Rust (`rust-toolchain.toml`)

Channel `1.94.0`. Targets: `wasm32-unknown-unknown`, `x86_64-pc-windows-gnu`.
Lint via `cargo clippy --locked --all-targets -- -D warnings` and `cargo fmt
--check` (Makefile `lint-rust`).

### Proto

`buf lint` against `buf.yaml` and `buf-legacy.yaml` (for `api/proto`).
Custom plugin `buf-plugin-linters` enforces pagination conventions.
`buf breaking . --against '.git#branch=$(BASE)'` for breaking-change detection
(`lint-breaking`).

### Shell

`shellcheck` (excluding SC2086, SC1091; SC2129 also excluded for AWS AMI
scripts) against all `*.sh` and `assets/aws/files/bin/*`.

### License

`go run github.com/google/addlicense@v1.0.0` enforces AGPL3 license header on
non-`api/` files and Apache2 on `api/*` (`build.assets/LICENSE.header`).

### Terraform (`.tflint.hcl`)

Six lines: `ignore_module = { "./agent-installation" = true, "./azure" = true }`.
Workflow `terraform-lint.yaml` invokes `gravitational/shared-workflows`
`terraform-lint.yaml`. Files: `**.tf`, `**.tf.json`, `**.hcl`.

### Spell / formatting / misc

`misspell` (in golangci-lint enable list); `cspell.json` lives under `docs/`.
`svgo.config.mjs` configures the SVG optimizer (preset-default with
`cleanupIds.minify=false`).

---

## 8. Versioning & releases

### Version of source

- `Makefile:VERSION=19.0.0-prealpha.2` is the canonical version variable.
- `version.go` exposes `Version = api.Version` and `SemVer()` / `MinClientSemVer()`. Also `Gitref` (populated by build).
- `api/version.go` is auto-generated by `build.assets/tooling/cmd/apiversion` (called by `version.mk:setver`).
- `gitref.go` is auto-generated from `git describe --long --tags` by `version.mk:setver`.
- `version.mk` also propagates the version to Helm charts (`helm-janitor update-version`) and tsh `Info.plist` (`update-plist-version`).
- `examples/chart/*/Chart.yaml` keep `.version: &version "19.0.0-prealpha.2"` synced.

### Release pipeline (no goreleaser)

There is **no** goreleaser config in the repo. Releases use:
1. `make update-version` (manual: bumps VERSION + propagates).
2. `make update-tag` -> `git tag v$(VERSION)` + `api/$(VERSION)` + same in `e/` submodule, push to `origin`.
3. `make tag-build` -> `gh workflow run tag-build.yaml --repo gravitational/teleport.e --ref v$(VERSION)`. Environment selected by `IS_CLOUD_SEMVER` / `IS_PROD_SEMVER`.
4. After tag-build finishes, `make tag-publish` -> runs `tag-publish.yaml`.
5. Linux/macOS release artifacts: `make release` -> `release-{unix,darwin,windows}` -> `make full` + `build-archive` + `build-update-archive`; recursed into `e/Makefile`. Output: `$(BUILDDIR)/artifacts/teleport-v$(VERSION)-$(OS)-$(ARCH)-bin.tar.gz` (or `.zip` on win, `.pkg`/`.dmg` on macOS).
6. Linux distro packages: `make rpm`, `make deb`, `make oss-deb` -> `build.assets/build-package.sh`.
7. macOS .pkg installer: `make pkg` -> `build-pkg-app.sh` (tsh.pkg, tctl.pkg) + `build-package.sh` (teleport-bin.pkg) + `productbuild`; signed/notarized by `darwin-signing.mk:notarize_pkg`.
8. Windows .exe: signed with `osslsigncode` + DigiCert timestamp in the `release-windows` target.
9. `create-github-release` runs `gh release create v$(VERSION)` with notes pulled from `CHANGELOG.md` via `shared-workflows/tools/release-notes`.
10. `post-release.yaml` workflow runs after the GitHub `release` event, determines latest flag, kicks downstream consumers.

### macOS code-signing (`darwin-signing.mk` + `entitlements/`)

- `prod/build` env: `TEAMID=QH8AA5B8UP` (Gravitational Inc.), bundle id `com.gravitational.teleport`.
- `stage/build` env: `TEAMID=K497G57PDJ` (Ada Lin), bundle id `com.goteleport.dev`.
- `notarize_binaries_cmd`: `codesign --options runtime --timestamp` then `xcrun notarytool submit --wait`.
- `notarize_app_bundle`: identical with `--options kill,hard,runtime` + entitlements.
- `notarize_pkg`: `productsign` then `notarytool submit` then `xcrun stapler staple`.
- `entitlements/entitlements.go` enumerates `EntitlementKind` constants (paired 1:1 with cloud/product features). Per-target entitlements XML files live at `build.assets/macos/{tsh,tshdev,tctl,tctldev}/<name>.entitlements`.
- `build.assets/macos/<tool>/tsh.app/` skeletons contain `Contents/{Info.plist, embedded.provisionprofile, Library, PkgInfo, Resources}`; `scripts/<tool>/postinstall` runs at .pkg install time.

### Autoupdate integration

- `tool/teleport-update/main.go` is the small CGO-less updater binary (`make $(BUILDDIR)/teleport-update`). It uses `lib/autoupdate`.
- `lib/autoupdate/` package layout: `agent/`, `lookup/`, `report/`, `rollout/`, `tools/`, plus `package_url.go`, `env_vars.go`. Drives self-update against advertised version channels.
- `lib/release/release.go` is a small HTTP client to `get.gravitational.com` for release metadata (`humanize` deps, JSON, TLS).
- Release tarball produced by `build-update-archive` is named `teleport-update-v$(VERSION)-$(OS)-$(ARCH)-bin.tar.gz` (linux-amd64 also gets a `-centos7` alias copy for the website).

### CHANGELOG (`CHANGELOG.md`)

~10,423 lines, 403 `##`-level sections. Structure: top-level `# Changelog`,
then `## <Version> (date)` entries with optional `### Breaking changes`,
`### New features`, etc. The PR template (`.github/PULL_REQUEST_TEMPLATE.md`)
requires a `Changelog:` line, validated by `changelog.yaml`. The repo's
`make changelog` calls `gravitational/shared-workflows/tools/changelog`.

### Root `assets/`

| Path | Purpose |
|---|---|
| `assets/aws/` | Packer (`single-ami.pkr.hcl`) and supporting `cmd/`, `files/` (bash scripts under `bin/` are linted) to build Teleport AMIs. Has its own `go.mod` + `Makefile`. |
| `assets/backport/` | The `backport` CLI (`make backport PR= TO=`). Own `go.mod`. |
| `assets/img/hero-teleport-platform.png` | README image. |
| `assets/install-scripts/` | `install.sh`, `install-connect.sh`, `install-selinux.sh`, `license-check.sh`, plus a `shim/` directory. `install-selinux.sh` is copied into the linux release tarball by `build-archive`. |
| `assets/loadtest/` | Loadtest harnesses (ansible-like, azure, cluster, control-plane, databases, etcd, helm). |

---

## 9. Deployment recipes

### Helm charts (`examples/chart/`)

All charts pinned to `appVersion=19.0.0-prealpha.2` via `.version: &version`
YAML anchor.

| Chart | Purpose |
|---|---|
| `teleport-cluster/` | Primary self-hosted cluster chart (proxy + auth + optional operator dep). Has `values.yaml`, `values.schema.json`, `tests/` (helm-unittest), `templates/`, depends on `teleport-operator` chart. |
| `teleport-kube-agent/` | Kubernetes agent for SSH / app / db / kube access. Depends on `teleport-kube-updater`. Includes `aws-and-manual-db.yaml` example. |
| `teleport-kube-updater/` | (sub-chart, dependency of teleport-kube-agent) kube-agent rolling updater. |
| `teleport-operator/` | (sub-chart, dependency of teleport-cluster) Kubernetes operator deployment. |
| `teleport-relay/` | Standalone relay deployment. |
| `tbot/` | Machine ID agent (tbot) for K8s workload identity. |
| `tbot-spiffe-daemon-set/` | tbot as a DaemonSet issuing SPIFFE SVIDs to other workloads. |
| `event-handler/` | `teleport-plugin-event-handler` (audit event fanout). Depends on `tbot` chart (conditional). |
| `access/slack/` | `teleport-plugin-slack` Access Request plugin. |
| `access/jira/` | Jira access plugin. |
| `access/pagerduty/` | PagerDuty access plugin. |
| `access/msteams/` | Microsoft Teams access plugin. |
| `access/mattermost/` | Mattermost access plugin. |
| `access/discord/` | Discord access plugin. |
| `access/datadog/` | Datadog access plugin. |
| `access/email/` | Email access plugin. |

Each chart has `Chart.yaml`, `values.yaml`, `templates/`, `tests/` (unittest
snapshots), and most have `values.schema.json` + `README.md`.

`examples/chart/Makefile` exposes `render-chart-ref`, `check-chart-ref`,
`test`, `lint`, each backed by the `helm-janitor` binary.
`examples/chart/CONTRIBUTING.md` mandates `.lint/` example values + `tests/`
unit tests for any new value.

### Terraform recipes (`examples/aws/terraform/`)

| Module | Purpose |
|---|---|
| `starter-cluster/` | Single-instance Teleport cluster on EC2: ACM cert, IAM, LB, SG, DynamoDB, Route53, S3 (audit/sessions), SSM. |
| `ha-autoscale-cluster/` | Full HA: separate auth/proxy/node ASGs, IAM roles per role, separate networks (`*_network.tf`), DynamoDB locks + state, Route53, bastion. User-data templates `.tpl`. Includes `connect.sh`, `ansible/` sub-folder. |
| `ecs-agent/` | Tiny module to run a teleport agent as an ECS task. |
| `AMIS.md` | Lookup table of AMI IDs, updated by `update-ami-ids.yaml` workflow. |
| `README.md` | High-level layout doc. |

`examples/terraform/` (top-level) contains terraform provider examples;
`examples/terraform-starter/` is the entry point for cloud customers.

### systemd / launchd / upstart

`examples/systemd/`:
- `teleport.service` - canonical unit. `production/{auth,node,proxy}/` -
  role-specific overlays. `fips/teleport.service` for FIPS builds.
  `before-remove`, `post-install`, `post-upgrade` are package hooks (used by
  rpm/deb).
- `machine-id/machine-id.service` - tbot unit.
- `vnet/teleport-vnet.service` + `dbus/`, `polkit/` policy snippets.
- `plugins/teleport-{slack,jira,pagerduty,mattermost,msteams,email,discord}.service`.

`examples/launchd/com.goteleport.teleport.plist` - macOS LaunchDaemon plist
(starts `/usr/local/bin/teleport start`).

`examples/upstart/` - legacy upstart files (Ubuntu 14.04 era).

### Other examples

| Path | Purpose |
|---|---|
| `examples/k8s-auth/` | `get-kubeconfig.sh`, `merge-kubeconfigs.sh` for bringing kube clusters under Teleport. |
| `examples/desktop-registration/` | Tiny Go program for AD desktop registration. Own go.mod. |
| `examples/etcd/` | (empty - placeholder). |
| `examples/grafana/teleport-dashboard.json` | Prebuilt Grafana dashboard. |
| `examples/teleport-usage/` | Usage report generator. |
| `examples/dynamoathenamigration/` | DynamoDB -> Athena audit migration. |
| `examples/athena/` | Athena audit log sample queries. |
| `examples/bench/` | Bench data fixtures. |
| `examples/jwt/`, `examples/go-client/`, `examples/access-plugin-minimal/`, `examples/api-sync-roles/`, `examples/service-discovery-api-client/`, `examples/identity-activity-center/`, `examples/workload-clusters/`, `examples/mcp-servers/`, `examples/resources/` | Reference apps / clients. |

---

## 10. Observability surface

### Prometheus metrics (`metrics.go` - root)

Constants only (the strings); collectors registered in
`lib/observability/metrics/`. All metrics live under namespace
`teleport` (`MetricNamespace`).

Auth/SSH/proxy:
`auth_generate_requests_total`, `auth_generate_requests_throttled_total`,
`auth_generate_requests` (in-flight), `auth_generate_seconds` (histogram),
`server_interactive_sessions_total`, `proxy_ssh_sessions_total`,
`remote_clusters`, `trusted_clusters`, `cluster_name_not_found_total`,
`failed_login_attempts_total`, `connect_to_node_attempts_total`,
`failed_connect_to_node_attempts_total`, `user_max_concurrent_sessions_hit_total`,
`proxy_connection_limit_exceeded_total`, `user_login_total`,
`heartbeat_connections_received_total`, `certificate_mismatch_total`,
`heartbeats_missed_total`, `watcher_events`, `watcher_event_sizes`,
`proxy_missing_ssh_tunnels`, `migrations`, `incomplete_session_uploads_total`,
`total_instances`, `enrolled_in_upgrades`, `upgrader_counts`,
`access_requests_created`, `user_certificates_generated`.

Process / Go runtime (from default collectors): `process_cpu_seconds_total`,
`process_max_fds`, `process_open_fds`, `process_resident_memory_bytes`,
`process_start_time_seconds`, `go_threads`, `go_goroutines`, `go_info`,
`go_memstats_alloc_bytes`, `go_memstats_heap_alloc_bytes`,
`go_memstats_heap_objects`.

Backend: `backend_watchers_total`, `backend_watcher_queues_total`,
`backend_requests`, `backend_read_seconds`, `backend_write_seconds`,
`backend_batch_write_seconds`, `backend_batch_read_seconds`,
`backend_write_requests_total`, `backend_writes_total`,
`backend_write_requests_failed_total`,
`backend_write_requests_failed_precondition_total`,
`backend_atomic_write_requests_total`,
`backend_atomic_write_requests_failed_total`,
`backend_atomic_write_condition_failed_total`,
`backend_atomic_write_seconds`, `backend_atomic_write_size`,
`backend_atomic_write_contention`, `backend_batch_write_requests_total`,
`backend_batch_write_requests_failed_total`, `backend_read_requests_total`,
`backend_reads_total`, `backend_read_requests_failed_total`,
`backend_batch_read_requests_total`, `backend_batch_read_requests_failed_total`.

BPF / process: `bpf_lost_command_events`, `bpf_lost_disk_events`,
`bpf_lost_network_events`, `bpf_lost_restricted_events`, `process_state`,
`build_info`, `cache_events`, `cache_stale_events`, `registered_servers`,
`bot_instances`, `registered_servers_by_install_methods`,
`reverse_tunnels_connected`, `hosted_plugin_status`, `services`,
`resources_health_status`, `connected_resources`.

Usage events: `usage_events_submitted_total`, `usage_batches_total`,
`usage_events_requeued_total`, `usage_batch_submission_duration_seconds`,
`usage_batch_submitted_total`, `usage_batch_failed_total`,
`usage_events_dropped_total`.

Athena audit log: `audit_parquetlog_batch_processing_seconds`,
`audit_parquetlog_s3_flush_seconds`, `audit_parquetlog_delete_events_seconds`,
`audit_parquetlog_batch_size`, `audit_parquetlog_batch_count`,
`audit_parquetlog_last_processed_timestamp`,
`audit_parquetlog_age_oldest_processed_message`,
`audit_parquetlog_errors_from_collect_count`.

Tag keys defined here: `cluster`, `migration`, `roles`, `resources`,
`private_key_policy`, `upgrader`, `status`, `range`, `req`, `true`, `false`,
`resource`, `version`, `os`, `gitref`, `goversion`, `cache_component`, `type`,
`server`, `client`, `install_methods`, `service_name`, `automatic_updates`.

Health: `healthy`, `unhealthy`, `unknown` constants; resource type
constants `db`, `kubernetes`.

### Metric / collector helpers (`lib/observability/metrics/`)

- `registry.go` - `Registry` wrapping `prometheus.Registry`.
- `prometheus.go` - `RegisterPrometheusCollectors` (idempotent wrapper handling
  `AlreadyRegisteredError`), `RegisterCollectors(registry, …)`. The default
  build-info collector is created here under `Namespace=teleport`,
  `Name=build_info`.
- `gatherers.go` - assembles gatherer set.
- Sub-packages: `aws/` (AWS API call metrics), `dynamo/` (DynamoDB ops),
  `grpc/` (server/client interceptors), `s3/`.

### Tracing (`lib/observability/tracing/`)

- `tracing.go` - OTLP setup. Constants:
  `VersionKey=teleport.version`, `ProcessIDKey=teleport.process.id`,
  `HostnameKey=teleport.host.name`, `HostIDKey=teleport.host.uuid`,
  `DefaultExporterDialTimeout=5s`.
- `client.go` - OTLP client builder.
- `exporter.go`, `collector.go` - exporter / span processor wiring.
- `internal/` - shared helpers.

### otelhttp (`lib/observability/otelhttp/default_client.go`)

Default-instrumented HTTP client preset.

### Public tracing types (`api/observability/tracing/`)

- `tracing.go`, `client.go`, `option.go`, plus sub-packages `http/` and `ssh/`
  for instrumenting downstream HTTP and SSH transports.

### Health endpoints / diagnostics (`lib/healthcheck/` + `lib/service/diagnostic.go`)

`lib/healthcheck/` (worker, manager, target, net, order_by, config) drives
agent-side health checking for resources (DB/Kube targets).

`lib/service/diagnostic.go` registers the **diagnostic HTTP mux** with these
handlers (gated by `diagnosticHandlerConfig` flags):

- `GET /metrics`   - Prometheus scrape (always enabled via `--diag-addr`).
- `GET /healthz`   - Liveness, returns JSON `{"status":"ok"}`.
- `GET /readyz`    - Calls `process.HandleReadiness`.
- `GET /debug/pprof/*` - via `debug.RegisterProfilingHandlers` (profiling flag).
- `GET /debug/log-level` etc. - via `debug.RegisterLogLevelHandlers`.

CLI flag: `--diag-addr=<host:port>` on `teleport start`, `teleport app start`,
`teleport db start` (registered in `tool/teleport/common/teleport.go` at lines
173, 240, 284). The flag is documented as "Start diagnostic prometheus and
healthz endpoint."

There is no `--observability` flag; observability is configured via:
1. `--diag-addr` for metrics + health + pprof.
2. `tracing_service` block in `teleport.yaml` (consumed by
   `lib/observability/tracing`).
3. `lib/service/connect.go` wires `grpcmetrics` interceptors using the
   metrics registry.

---

## 11. Security scanning

- **CodeQL:** No CodeQL workflow is checked in to this OSS repo (scanned
  out-of-band, likely via the enterprise repo).
- **Trivy:** `.trivyignore` (22 lines) suppresses common Dockerfile/K8s
  findings that can't be inlined: `AVD-DS-0002`, `AVD-KSV-0109`, `AVD-KSV-0110`,
  `DS001`, `DS013`, `DS026`, `KSV001`, `KSV003`, `KSV009`, `KSV011`, `KSV012`,
  `KSV013`, `KSV014`, `KSV015`, `KSV016`, `KSV018`, `KSV020`, `KSV021`,
  `KSV030`, `KSV047`, `KSV106`. No Trivy workflow in `.github/workflows/`
  (scanning is external).
- **Dependabot (`.github/dependabot.yml`):** Active for `gomod` (root, api/,
  assets/aws/, assets/backport/, build.assets/tooling/, integrations/{terraform,
  terraform-mwi, event-handler}/, integrations/kube-agent-updater, examples/**,
  build.assets/tools/{gci, gotestsum, goda, benchstat}); `cargo` (root,
  lib/srv/desktop/rdp/rdpclient, tool/fdpass-teleport, web/.../ironrdp); `npm`
  (root, separate groups for `electron*`, `node-gyp`, `node-pty`, and
  ui/ui-tooling); `github-actions` (workflows + actions dirs). All with
  `cooldown.default-days: 2`. Each adds `no-changelog` label automatically.
  Many forked/replaced deps excluded (kingpin, go-mysql, go-libfido2,
  go-mssqldb, redis/v9, predicate, vt10x).
- **Renovate (`.github/renovate.json`):** Only handles Go-toolchain version
  bumps; runs against `master`, `branch/v18`, `branch/v17`. Custom regex
  managers patch `go.mod` `go`/`toolchain` directives,
  `build.assets/versions.mk:GOLANG_VERSION`, `docs/config.json:golang`,
  `build.assets/Dockerfile-grpcbox` `FROM docker.io/golang:` line. Patch updates
  enabled and grouped under `groupName: "Go version"`; minor/major disabled.
  Triggered by `renovate.yaml` workflow (Wed 7am UTC).
- **govulncheck:** wrapped by `build.assets/tooling/cmd/govulncheck-report`
  (called manually / out-of-band; not wired into the merge gates here).
- **shellcheck:** Run by `make lint-sh` (no dedicated workflow; covered by
  `lint.yaml` aggregate).
- **`addlicense`:** Enforces AGPL3 (root) / Apache2 (`api/`) headers in
  `lint-license`.

---

## 12. AI-pointer index (concept -> file)

- **Add a new make target** -> `/home/daniel/repos/teleport/Makefile`
- **Override version** -> `/home/daniel/repos/teleport/Makefile` (line 16, `VERSION=`)
- **Version source for `api/`** -> generated `/home/daniel/repos/teleport/api/version.go`; updater `/home/daniel/repos/teleport/build.assets/tooling/cmd/apiversion/main.go`
- **`Gitref` populated** -> `/home/daniel/repos/teleport/gitref.go` (regenerated by `/home/daniel/repos/teleport/version.mk`)
- **Build the `rdp-client` Rust static lib** -> `Makefile:527 rdpclient` -> `cargo build -p rdp-client` in `/home/daniel/repos/teleport/lib/srv/desktop/rdp/rdpclient/`
- **Build `rdp-decoder`** -> `Makefile:538 rdpdecoder`; crate at `/home/daniel/repos/teleport/lib/srv/desktop/rdp/decoder/`
- **Build ironrdp WASM** -> `Makefile:559 build-ironrdp-wasm`; crate at `/home/daniel/repos/teleport/web/packages/shared/libs/ironrdp/`
- **Cross-compile linux from macOS** -> `make -C build.assets build`; or `make docker-binaries`
- **Enter buildbox shell** -> `make enter`, `make enter/centos7`, `make enter/centos7-fips`, `make enter/arm`, `make enter/node`, `make enter/grpcbox`
- **Regenerate gRPC stubs** -> `make grpc` -> `/home/daniel/repos/teleport/build.assets/Dockerfile-grpcbox` -> `/home/daniel/repos/teleport/build.assets/genproto.sh`
- **Regenerate Kubernetes CRDs** -> `make -C integrations/operator crd-manifests`; generator `/home/daniel/repos/teleport/integrations/operator/crdgen/`
- **Regenerate Helm chart reference docs** -> `/home/daniel/repos/teleport/examples/chart/Makefile` -> `helm-janitor reference` -> `/home/daniel/repos/teleport/build.assets/tooling/cmd/render-helm-ref/`
- **Regenerate tctl resource reference** -> `make gen-resource-docs` -> `/home/daniel/repos/teleport/build.assets/tooling/cmd/resource-ref-generator/`
- **Regenerate CLI help docs** -> `make cli-docs` -> outputs in `/home/daniel/repos/teleport/docs/pages/reference/cli/`
- **Generate event-schema audit reference** -> `/home/daniel/repos/teleport/build.assets/tooling/cmd/protoc-gen-eventschema/` + `make access-monitoring-reference`
- **Macros for macOS code-sign & notarize** -> `/home/daniel/repos/teleport/darwin-signing.mk`
- **macOS entitlement enum** -> `/home/daniel/repos/teleport/entitlements/entitlements.go`
- **macOS .app skeleton + provisioning profile** -> `/home/daniel/repos/teleport/build.assets/macos/{tsh,tshdev,tctl,tctldev}/`
- **macOS .pkg builder** -> `/home/daniel/repos/teleport/build.assets/build-pkg-app.sh` + `Makefile:1786 pkg`
- **rpm/deb builder** -> `/home/daniel/repos/teleport/build.assets/build-package.sh`; templates in `/home/daniel/repos/teleport/build.assets/rpm/`, `rpm-sign/`
- **Linux install script (in tarball)** -> `/home/daniel/repos/teleport/build.assets/install`
- **macOS install script (in tarball)** -> `/home/daniel/repos/teleport/build.assets/macos/install`
- **SELinux install script** -> `/home/daniel/repos/teleport/assets/install-scripts/install-selinux.sh`
- **Top-level install script (curl|sh)** -> `/home/daniel/repos/teleport/assets/install-scripts/install.sh`
- **Build a Docker OCI image** -> `make image` -> uses `/home/daniel/repos/teleport/build.assets/charts/Dockerfile-distroless`
- **CentOS 7 buildbox glibc baseline** -> `/home/daniel/repos/teleport/build.assets/Dockerfile-centos7`
- **ARM cross-build** -> `/home/daniel/repos/teleport/build.assets/Dockerfile-arm`
- **BPF bytecode regen** -> `/home/daniel/repos/teleport/build.assets/Dockerfile-bpf` + `make bpf-bytecode`
- **Webassets builder** -> `/home/daniel/repos/teleport/build.assets/Dockerfile-node` + `make webassets`
- **gRPC code-gen builder** -> `/home/daniel/repos/teleport/build.assets/Dockerfile-grpcbox`
- **Pinned tool versions** -> `/home/daniel/repos/teleport/build.assets/versions.mk`
- **Pinned Rust toolchain** -> `/home/daniel/repos/teleport/rust-toolchain.toml` (channel `1.94.0`)
- **Pinned Go modules** -> `/home/daniel/repos/teleport/build.assets/tools/{gci,gotestsum,goda,benchstat}/go.mod`
- **Run a single Go test repeatedly until it fails** -> `make test-go-flaky` (uses `/home/daniel/repos/teleport/build.assets/tooling/cmd/rerun/`)
- **Diff-targeted tests** -> `/home/daniel/repos/teleport/build.assets/tooling/cmd/difftest/` (and `.github/actions/difftest/`)
- **Filter `go test -json` output** -> `/home/daniel/repos/teleport/build.assets/tooling/cmd/render-tests/`
- **Locate go-bench packages** -> `/home/daniel/repos/teleport/build.assets/tooling/cmd/benchfind/`
- **GitHub workflow: required Go test gate** -> `/home/daniel/repos/teleport/.github/workflows/unit-tests-code.yaml` (bypass: `unit-tests-code-bypass.yaml`)
- **GitHub workflow: required big-lint** -> `/home/daniel/repos/teleport/.github/workflows/lint.yaml`
- **GitHub workflow: macOS build gate** -> `/home/daniel/repos/teleport/.github/workflows/build-macos.yaml` (bypass: `build-macos-bypass.yaml`)
- **GitHub workflow: Windows tsh build** -> `/home/daniel/repos/teleport/.github/workflows/build-windows.yaml`
- **GitHub workflow: CI buildbox image rebuild** -> `/home/daniel/repos/teleport/.github/workflows/build-ci-buildbox-images.yaml`
- **GitHub workflow: CentOS 7 assets weekly** -> `/home/daniel/repos/teleport/.github/workflows/build-centos7-assets.yaml` (cron Sun 13:00 UTC)
- **GitHub workflow: changelog gate** -> `/home/daniel/repos/teleport/.github/workflows/changelog.yaml`
- **GitHub workflow: backport bot** -> `/home/daniel/repos/teleport/.github/workflows/backport.yaml`
- **GitHub workflow: post-release** -> `/home/daniel/repos/teleport/.github/workflows/post-release.yaml`
- **GitHub workflow: AMI ID update** -> `/home/daniel/repos/teleport/.github/workflows/update-ami-ids.yaml`
- **GitHub workflow: docs Amplify preview** -> `/home/daniel/repos/teleport/.github/workflows/docs-amplify.yaml`
- **GitHub workflow: docs webhook on push** -> `/home/daniel/repos/teleport/.github/workflows/update-docs-webhook.yaml`
- **GitHub workflow: Go-toolchain Renovate** -> `/home/daniel/repos/teleport/.github/workflows/renovate.yaml`
- **CODEOWNERS** -> `/home/daniel/repos/teleport/.github/CODEOWNERS` (protects workflows, go.mod, Cargo.toml; per-module reviewer sets)
- **Dependabot grouping rules** -> `/home/daniel/repos/teleport/.github/dependabot.yml`
- **Renovate custom Go-version matchers** -> `/home/daniel/repos/teleport/.github/renovate.json`
- **etcd CI service** -> `/home/daniel/repos/teleport/.github/services/Dockerfile.etcd` + `make run-etcd`
- **Composite action: diff-scoped tests** -> `/home/daniel/repos/teleport/.github/actions/difftest/action.yml`
- **Composite action: prepare Go cache** -> `/home/daniel/repos/teleport/.github/actions/prepare-workspace/action.yml`
- **Composite action: push JUnit to S3** -> `/home/daniel/repos/teleport/.github/actions/upload-test-metrics/action.yml`
- **golangci-lint config** -> `/home/daniel/repos/teleport/.golangci.yml`
- **oxlint config** -> `/home/daniel/repos/teleport/.oxlintrc.jsonc`
- **oxfmt config** -> `/home/daniel/repos/teleport/.oxfmtrc.json`
- **SVGO config** -> `/home/daniel/repos/teleport/svgo.config.mjs`
- **eslint config (editor compat)** -> `/home/daniel/repos/teleport/eslint.config.mjs`
- **Jest config** -> `/home/daniel/repos/teleport/jest.config.js`
- **Babel config** -> `/home/daniel/repos/teleport/babel.config.js`
- **pnpm workspace** -> `/home/daniel/repos/teleport/pnpm-workspace.yaml` (+ `/home/daniel/repos/teleport/.pnpmfile.cjs` patches missing `e/` workspace)
- **tsconfig roots** -> `/home/daniel/repos/teleport/tsconfig.{base,,node}.json`
- **Trivy ignores** -> `/home/daniel/repos/teleport/.trivyignore`
- **tflint config** -> `/home/daniel/repos/teleport/.tflint.hcl`
- **buf proto config** -> `/home/daniel/repos/teleport/buf.yaml`, `/home/daniel/repos/teleport/buf-{go,gogo,connect-go,ts}.gen.yaml`, `/home/daniel/repos/teleport/buf-legacy.yaml`, `/home/daniel/repos/teleport/buf.lock`
- **Custom buf lint plugin** -> `/home/daniel/repos/teleport/build.assets/tooling/cmd/buf-plugin-linters/`
- **Helm chart linting/testing** -> `/home/daniel/repos/teleport/examples/chart/Makefile` (`helm-janitor lint/test/reference`)
- **Helm chart inventory** -> `/home/daniel/repos/teleport/examples/chart/`
- **Helm contributing guide** -> `/home/daniel/repos/teleport/examples/chart/CONTRIBUTING.md`
- **Terraform HA reference** -> `/home/daniel/repos/teleport/examples/aws/terraform/ha-autoscale-cluster/`
- **Terraform starter** -> `/home/daniel/repos/teleport/examples/aws/terraform/starter-cluster/`
- **Terraform ECS agent** -> `/home/daniel/repos/teleport/examples/aws/terraform/ecs-agent/`
- **systemd unit (canonical)** -> `/home/daniel/repos/teleport/examples/systemd/teleport.service`
- **systemd units (role overlays)** -> `/home/daniel/repos/teleport/examples/systemd/production/{auth,proxy,node}/`
- **systemd FIPS unit** -> `/home/daniel/repos/teleport/examples/systemd/fips/teleport.service`
- **systemd Machine ID unit** -> `/home/daniel/repos/teleport/examples/systemd/machine-id/machine-id.service`
- **systemd VNet unit** -> `/home/daniel/repos/teleport/examples/systemd/vnet/teleport-vnet.service`
- **systemd Access plugin units** -> `/home/daniel/repos/teleport/examples/systemd/plugins/teleport-{slack,jira,pagerduty,msteams,mattermost,email,discord}.service`
- **macOS launchd plist** -> `/home/daniel/repos/teleport/examples/launchd/com.goteleport.teleport.plist`
- **Grafana dashboard** -> `/home/daniel/repos/teleport/examples/grafana/teleport-dashboard.json`
- **Diagnostic / health endpoints registration** -> `/home/daniel/repos/teleport/lib/service/diagnostic.go`
- **`--diag-addr` flag** -> `/home/daniel/repos/teleport/tool/teleport/common/teleport.go:173` (and `:240`, `:284`)
- **Prometheus metric names** -> `/home/daniel/repos/teleport/metrics.go`
- **Prometheus registry helpers** -> `/home/daniel/repos/teleport/lib/observability/metrics/{registry.go,prometheus.go,gatherers.go}`
- **AWS API call metrics collector** -> `/home/daniel/repos/teleport/lib/observability/metrics/aws/aws.go`
- **gRPC server/client metrics** -> `/home/daniel/repos/teleport/lib/observability/metrics/grpc/`
- **OTLP tracing setup** -> `/home/daniel/repos/teleport/lib/observability/tracing/tracing.go`
- **Default OTel HTTP client** -> `/home/daniel/repos/teleport/lib/observability/otelhttp/default_client.go`
- **Public tracing API** -> `/home/daniel/repos/teleport/api/observability/tracing/`
- **Resource health checker (agent)** -> `/home/daniel/repos/teleport/lib/healthcheck/`
- **Auto-update agent** -> `/home/daniel/repos/teleport/lib/autoupdate/agent/`
- **Auto-update binary** -> `/home/daniel/repos/teleport/tool/teleport-update/main.go`
- **Release metadata HTTP client** -> `/home/daniel/repos/teleport/lib/release/release.go`
- **CHANGELOG** -> `/home/daniel/repos/teleport/CHANGELOG.md` (10423 lines, 403 sections; `## <ver> (date)` style)
- **PR template** -> `/home/daniel/repos/teleport/.github/PULL_REQUEST_TEMPLATE.md` (mandates `Changelog:` line + manual test plan)
- **Issue templates** -> `/home/daniel/repos/teleport/.github/ISSUE_TEMPLATE/{bug_report,documentation,feature_request,flaky_test,test-plan-{docs,identity-security},testplan,webtestplan,config.yml}.md`
- **Submodule for enterprise (`e/`)** -> `/home/daniel/repos/teleport/.gitmodules` (`git@github.com:gravitational/teleport.e.git`)
- **gitattributes for linguist/generated marking** -> `/home/daniel/repos/teleport/.gitattributes`
- **`.npmrc`** -> `/home/daniel/repos/teleport/.npmrc` (`update-notifier=false`, hoists eslint)
