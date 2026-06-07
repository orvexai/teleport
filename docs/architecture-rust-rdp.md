# Architecture — Rust RDP Workspace

> **Scope:** `lib/srv/desktop/rdp/rdpclient/`, `lib/srv/desktop/rdp/decoder/`, `web/packages/shared/libs/ironrdp/`. The three-crate Cargo workspace that gives Teleport RDP (Remote Desktop Protocol) support for both the Go desktop service and the browser.
>
> **Deep dive:** `findings/integrations-rust-bpf-tools.md` §7-8 (~200 lines on the Rust workspace).

---

## 1. What This Part Is

A small Rust workspace (3 crates, ~21 source files) wrapping the [**IronRDP**](https://github.com/Devolutions/IronRDP) library — Devolutions' open-source RDP implementation — pinned to a specific git revision (`a0a3e750c9e4ee9c73b957fbcb26dbc59e57d07d`).

It exists in **two consumer shapes** simultaneously:

1. **Native** — a `staticlib` linked into the Go `teleport` binary via CGo. Used by `lib/srv/desktop/` (the `windows_desktop_service`) to terminate RDP from upstream Windows hosts.
2. **Browser** — a `cdylib` compiled to WASM (`wasm32-unknown-unknown` target) bundled into the React UI. Used by the browser-side desktop player to decode RDP graphics so frames don't have to be transcoded to a different format server-side.

The decoder is shared between (1) and (2) as a small helper crate.

**Plus**: a fourth, *separate* (non-workspace) Rust binary at `tool/fdpass-teleport/` for file-descriptor passing — covered in `architecture-backend.md` §3p.

---

## 2. Workspace Layout

`/Cargo.toml` (root):

```toml
[workspace]
resolver = "2"
members = [
    "lib/srv/desktop/rdp/rdpclient",
    "lib/srv/desktop/rdp/decoder",
    "web/packages/shared/libs/ironrdp",
]

[workspace.package]
edition = "2021"
license = "AGPL-3.0-only"
publish = false

[workspace.dependencies]
ironrdp-cliprdr        = { git = "…IronRDP", rev = "a0a3e750c…" }
ironrdp-connector      = { git = "…", rev = "a0a3e750c…" }
ironrdp-core           = { git = "…", rev = "a0a3e750c…" }
ironrdp-displaycontrol = { git = "…", rev = "a0a3e750c…" }
ironrdp-dvc            = { git = "…", rev = "a0a3e750c…" }
ironrdp-graphics       = { git = "…", rev = "a0a3e750c…" }
ironrdp-pdu            = { git = "…", rev = "a0a3e750c…" }
ironrdp-rdpdr          = { git = "…", rev = "a0a3e750c…" }
ironrdp-rdpsnd         = { git = "…", rev = "a0a3e750c…" }
ironrdp-session        = { git = "…", rev = "a0a3e750c…" }
ironrdp-svc            = { git = "…", rev = "a0a3e750c…" }
ironrdp-tls            = { git = "…", rev = "a0a3e750c…", features = ["rustls"] }
ironrdp-tokio          = { git = "…", rev = "a0a3e750c…" }

[profile.dev]     debug = 1   lto = "off"
[profile.release] debug = 1   codegen-units = 1   lto = "thin"
```

Pinning is by **git rev**, not crates.io, because IronRDP is moving fast and Teleport carries its own patched fork (`gravitational/boring` for BoringSSL FIPS, etc.).

---

## 3. Crate 1 — `rdp-client` (`lib/srv/desktop/rdp/rdpclient/`)

```toml
[lib]
crate-type = ["staticlib"]   # for CGo linking

[dependencies]
# all the ironrdp-* crates from workspace
bitflags = "2"
byteorder, bytes
env_logger, log
iso7816, iso7816-tlv      # smartcard ASN.1
picky, picky-asn1-*, rsa  # certificate handling
rand, sspi                # SSPI = Kerberos / NTLM (network_client feature)
tokio, url, utf16string, uuid
rdp-decoder = { path = "../decoder" }
rustls = { default-features = false, features = ["aws-lc-rs"] }
reqwest, picky

# FIPS-conditional:
boring        = { git = "gravitational/boring", rev = "99897…", optional = true }
tokio-boring  = { git = "gravitational/boring", rev = "99897…", optional = true }

[build-dependencies]
cbindgen, tempfile   # generates the C header consumed by Go

[features]
fips = ["tokio-boring/fips", "boring/fips"]
```

**What this crate does:** it's the actual native RDP client. The Go `windows_desktop_service` opens an RDP connection to an upstream Windows host **through this crate** (rather than through a Go-native RDP implementation). The crate:

- Negotiates RDP (`ironrdp-connector` + `ironrdp-pdu`).
- Supports redirection of:
  - **Smartcards** (`iso7816` + `iso7816-tlv`) — for Windows logon via Teleport-issued smartcards (see `lib/winpki/`).
  - **Clipboard** (`ironrdp-cliprdr`).
  - **Audio** (`ironrdp-rdpsnd`).
  - **Drive** (`ironrdp-rdpdr`).
  - **Dynamic Virtual Channels** (`ironrdp-dvc`).
  - **Display control** (`ironrdp-displaycontrol`).
- Speaks Kerberos / NTLM via **`sspi 0.16`** (Rust's SSPI implementation), with `network_client` for Windows-server-style network logons.
- Uses TLS via **`rustls 0.23`** with `aws-lc-rs` (the AWS-LC-RS provider) **OR**, under the `fips` feature, **BoringSSL** from the gravitational fork of `boring` / `tokio-boring`.
- Re-uses **`rdp-decoder`** (crate 2) for RDP graphics frame decoding when the session is recorded.

**FFI surface (to Go):** `cbindgen 0.29` runs at build time (`build.rs`) and emits a C header from the Rust source. The header exposes **~21 C functions** to Go: connect, send input, get/set state, register channel handlers, run the I/O loop, free resources, plus a recording-frame export hook for the decoder.

**Go side:** `lib/srv/desktop/rdp/rdpclient/client.go` (and `*_cgo.go` files) wraps these via CGo. The Go build-tag matrix is:
- `desktop_access_rdp` — compile the CGo bridge (sets up linker flags).
- `+fips` — switch to the BoringSSL Rust feature; also affects Go build flags.
- `!desktop_access_rdp` — compile a *no-op* stub (`lib/srv/desktop/rdp/rdpclient/client_noop.go`) so the Go binary still builds on archs where the Rust staticlib doesn't ship.

Per-platform linker flags (in `client_cgo.go`) point at `librdp_client.a` and pull in the right system libraries.

---

## 4. Crate 2 — `rdp-decoder` (`lib/srv/desktop/rdp/decoder/`)

```toml
[lib]
crate-type = ["staticlib", "lib"]   # both for CGo AND for the other Rust crates

[dependencies]
ironrdp-core, ironrdp-graphics, ironrdp-pdu, ironrdp-session
```

A minimalist shared decoder. Its purpose is **session-recording rendering**: when an RDP session is recorded, the bitmap frames need to be turned back into images at playback time. This decoder is small enough to be shared between the native client (which records) and any tooling that wants to render recorded RDP frames.

Built with **both** `staticlib` (for CGo) and `lib` (for use by other Rust crates) — that's why `rdpclient` can `path = "../decoder"` it.

---

## 5. Crate 3 — `ironrdp` (`web/packages/shared/libs/ironrdp/`)

```toml
[lib]
crate-type = ["cdylib"]   # WASM

[dependencies]
ironrdp-core, ironrdp-graphics, ironrdp-pdu, ironrdp-session
console_error_panic_hook
getrandom1 = { package = "getrandom", version = "0.2", features = ["js"] }
getrandom2 = { package = "getrandom", version = "0.3", features = ["wasm_js"] }
js-sys, web-sys (features = ["ImageData"])
log
time = { features = ["wasm-bindgen"] }
tracing, tracing-subscriber, tracing-web
uuid = { features = ["js"] }
wasm-bindgen = "0.2"
```

This is the **browser-side** RDP renderer. It is *not* a full RDP client — it doesn't handle network I/O or auth. It receives **already-decrypted RDP PDUs** as bytes over a WebSocket (from `lib/srv/desktop`, framed as TDP) and turns them into `ImageData` ready for HTML canvas rendering.

Two `getrandom` versions are imported simultaneously (`getrandom1` for v0.2 / `getrandom2` for v0.3) — necessary because the dependency tree contains both releases and they each need the `js` / `wasm_js` feature to work in the browser.

**Build flow:**

```bash
make build-ironrdp-wasm
# → cd web/packages/shared/libs/ironrdp
# → cargo build --target wasm32-unknown-unknown --release
# → wasm-opt (optimizer)
# → wasm-bindgen (JS bindings + .d.ts types)
# Result: web/packages/shared/libs/ironrdp/dist/ (consumed by Vite)
```

Invoked from the pnpm workspace via `pnpm --filter=@gravitational/shared build-wasm` (which is just `make -C ../../../ build-ironrdp-wasm` after cleaning prior artifacts via `web/scripts/clean-up-ironrdp-artifacts.mjs`).

Vite bundles the resulting `.wasm` + JS shim via `vite-plugin-wasm`.

---

## 6. Build Targets

| Target | What it produces |
| --- | --- |
| `make rdpclient` | `lib/srv/desktop/rdp/rdpclient/target/release/librdp_client.a` (staticlib) + the C header. |
| `make rdpdecoder` | `lib/srv/desktop/rdp/decoder/target/release/librdp_decoder.a`. |
| `make build-ironrdp-wasm` | `web/packages/shared/libs/ironrdp/dist/` (WASM + JS shim). |
| `make ensure-wasm-deps` | Installs `wasm-bindgen-cli` + `wasm-opt` if missing. |
| `make rustup-toolchain-warning` | Sanity check the pinned 1.94.0 toolchain is selected. |

Cross-platform: the staticlib is built per OS/arch. The Makefile gates `rdpclient` to platforms where it's supported (excluded on arm/386 by default; FIPS-on-arm64 has extra plumbing).

---

## 7. Integration Seams

(See `integration-architecture.md`.)

| Seam | This part ↔ | Channel |
| --- | --- | --- |
| S5 | `rdp-client` (Rust staticlib) ↔ `lib/srv/desktop/rdp/rdpclient/*.go` (Go) | CGo + cbindgen-generated C header |
| S6 | `ironrdp` (Rust cdylib → WASM) ↔ `web/packages/shared/libs/ironrdp/*.ts` (browser JS) | `wasm-bindgen` |

---

## 8. Pointer Index

| Concept | File |
| --- | --- |
| Workspace root | `Cargo.toml` (root of repo) |
| Pinned IronRDP rev | `Cargo.toml` workspace.dependencies (rev `a0a3e750c…`) |
| Native client crate | `lib/srv/desktop/rdp/rdpclient/Cargo.toml`, `src/` |
| Native client FFI build script | `lib/srv/desktop/rdp/rdpclient/build.rs` (cbindgen) |
| Go-side CGo wrapper | `lib/srv/desktop/rdp/rdpclient/client.go` + `*_cgo.go` |
| Go-side no-op stub | `lib/srv/desktop/rdp/rdpclient/client_noop.go` |
| Decoder crate | `lib/srv/desktop/rdp/decoder/Cargo.toml`, `src/lib.rs` |
| WASM crate | `web/packages/shared/libs/ironrdp/Cargo.toml`, `src/lib.rs` |
| WASM-side TS wrapper | `web/packages/shared/libs/ironrdp/*.ts` |
| WASM build target | root `Makefile::build-ironrdp-wasm` |
| WASM clean script | `web/scripts/clean-up-ironrdp-artifacts.mjs` |
| BoringSSL fork | `gravitational/boring` rev `99897308a…` (used under `fips` feature) |
| TLS provider (non-FIPS) | rustls + aws-lc-rs |

For everything else, refer to `findings/integrations-rust-bpf-tools.md` §7-8.
