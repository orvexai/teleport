# Architecture — Teleterm (Teleport Connect)

> **Scope:** `web/packages/teleterm/` (TypeScript: renderer + main + preload) and `lib/teleterm/` (Go: `tshd` daemon). Together they form the cross-platform desktop client **Teleport Connect**.
>
> **Deep dive:** `findings/web-and-teleterm.md` §9-13 (~200 lines).
>
> **License:** Apache-2.0 (distinct from the AGPL-3.0 core).

---

## 1. What This Part Is

A multi-process **Electron desktop application** distributed as a signed installer per platform (DMG, NSIS, RPM, DEB, tarball). The product name is **Teleport Connect** (`@gravitational/teleterm` is the package name; `productName: "Teleport Connect"` in `package.json`).

The defining mental model: **the renderer never talks to a Teleport cluster directly**. Every network call goes through a local Go daemon (`tshd`) over gRPC. The renderer is, effectively, a UI-only client of a local privileged service.

---

## 2. Process Architecture

Four processes, three of them Electron, one of them a spawned Go binary:

```
┌──────────────────────────────────────────────────────────────────┐
│                          User's Mac/Windows/Linux                  │
│                                                                    │
│  ┌─────────────┐    IPC     ┌─────────────┐                       │
│  │ Electron    │  ───────►  │ Electron    │                       │
│  │ Renderer    │ (Electron) │ Main        │                       │
│  │ (React UI)  │  ◄───────  │ (Node.js)   │                       │
│  └──────┬──────┘            └──────┬──────┘                       │
│         │                          │                              │
│         │ gRPC (TCP, mTLS on Win)  │ gRPC                         │
│         ▼                          ▼                              │
│  ┌─────────────────────────────────────────┐                      │
│  │       tshd  (Go binary, lib/teleterm)   │                      │
│  │   apiserver/  daemon/  clusters/  vnet/ │                      │
│  └─────────────────────────────────────────┘                      │
│                          │                                         │
│                          │ mTLS to Teleport                        │
└──────────────────────────┼─────────────────────────────────────────┘
                           ▼
                  ┌──────────────────┐
                  │  Teleport Proxy  │
                  └──────────────────┘

  Plus: a "shared" process for PTY hosting (so node-pty doesn't block the renderer).
```

The four processes:

1. **Renderer** (`web/packages/teleterm/src/ui/`) — React 19 UI. No direct network access.
2. **Main** (`web/packages/teleterm/src/main/`) — Electron main process. Window management, OS integration, auto-update, spawning + supervising `tshd`.
3. **Shared / PTY host** (`web/packages/teleterm/src/sharedProcess/` or similar) — owns `node-pty` so PTY allocation doesn't block the renderer.
4. **`tshd`** — spawned Go process from `lib/teleterm/`. Owns cluster connections, certs, gRPC bridges. The renderer talks to `tshd` via gRPC over TCP (mTLS on Windows; loopback on macOS/Linux).

Reverse channel: `tshd` raises events back to the renderer via a `TshdEventsService` gRPC server **hosted inside the renderer** (gRPC inversion). This is how MFA prompts, headless approvals, and notifications surface in the UI.

---

## 3. The Go Daemon — `lib/teleterm/`

```
lib/teleterm/
├── teleterm.go                  Process entry (start, stop, signals)
├── daemon/                      Daemon-level lifecycle + state
├── apiserver/                   gRPC server entry
├── services/
│   ├── unifiedresources/        Unified-resources gRPC
│   ├── connectmycomputer/       "Connect My Computer" feature
│   ├── clusteridcache/
│   ├── …                        Per-feature gRPC service impls
├── clusters/                    Wraps lib/client for the daemon's needs
├── api/protogen/golang/         Generated Go bindings from proto/teleport/lib/teleterm/
├── vnet/                        VNet integration on the daemon side
└── README.md
```

**Registered gRPC services** (`apiserver/`):
- `TerminalService` — open SSH, kube, DB, desktop sessions; manage clusters/gateways/databases/kube clusters/access requests.
- `VnetService` — start/stop VNet; status; routes.
- `AutoUpdateService` — coordinate Teleterm + tools updates with the cluster's update policy (drives `electron-updater`).

Auth & connection facades in `clusters/` wrap `lib/client.TeleportClient` so the daemon owns multi-cluster state (active cluster, profile selection, per-cluster certs).

**Protos** defining the daemon API live at `proto/teleport/lib/teleterm/`:
- `v1/` — main service definitions (`TerminalService`, `VnetService`).
- `auto_update/` — AutoUpdate service.
- `vnet/` — VNet service.

---

## 4. The Electron App — `web/packages/teleterm/`

```
web/packages/teleterm/
├── package.json                 productName="Teleport Connect", main: build/app/main/index.js
├── electron-vite.config.mts     electron-vite (renderer + main + preload + shared)
├── electron-builder-config.js   Per-platform installer config
├── csp.ts                       Content Security Policy
├── dev-app-update.yml           Dev auto-update config
├── build_resources/             Icons, signing assets
├── eslint.config.mjs            (kept for storybook plugin compat)
├── index.html                   Renderer entry HTML
├── babel.config.js
└── src/
    ├── main/                    Electron main process
    │   ├── index.ts             Entry: app.whenReady, BrowserWindow, tshd spawn
    │   ├── menus/               Application menus
    │   ├── tshd/                Spawn + supervise + restart tshd
    │   ├── windowsManager.ts    Multi-window state
    │   ├── ipc/                 Main ↔ renderer IPC channels
    │   └── …
    ├── ui/                      Renderer (React)
    │   ├── Document/            Tabs (terminal, kube, db, gateway, …)
    │   ├── ClusterConnect/      Cluster login flows
    │   ├── DocumentTerminal/    SSH terminal tab (xterm via the shared PTY process)
    │   ├── AccessRequests/      Per-request UI (mirrors web AccessRequests)
    │   ├── Notifications/       In-app notification center
    │   ├── VnetSliderStep/      VNet wizard
    │   ├── TshContext/          ImmutableStore-based state (immer)
    │   └── …                    feature folders
    ├── services/
    │   ├── tshd/                Renderer-side gRPC client to TerminalService
    │   ├── tshdEvents/          The TshdEventsService server (renderer-hosted)
    │   ├── appUpdater/          electron-updater driver
    │   │   └── clientToolsUpdateProvider.ts   Custom provider; queries AutoUpdateService
    │   ├── pty/                 PTY orchestration via the shared process
    │   ├── tabHost/             Tab lifecycle
    │   ├── …
    ├── sharedProcess/           PTY host process
    └── preload/                 Electron preload bridge (renderer ↔ main contextBridge)
```

State management in the renderer is **`ImmutableStore`** — a hand-rolled wrapper around `immer` (`web/packages/teleterm/src/services/store/`). Each major area has its own store; React subscribes via hooks.

---

## 5. Wire Protocols

| Channel | Transport | Codec |
| --- | --- | --- |
| Renderer ↔ Main | Electron's built-in `ipcRenderer` / `ipcMain` (`web/packages/teleterm/src/preload/`) | JSON |
| Renderer ↔ `tshd` | gRPC over TCP loopback (Linux/macOS); gRPC over TCP with **mTLS** on Windows | `@protobuf-ts/grpc-transport` + `@grpc/grpc-js` |
| Main ↔ `tshd` | gRPC (same as renderer's channel) | Same |
| `tshd` → Renderer (events) | `TshdEventsService` — gRPC server hosted in the renderer; tshd is the client | Same |
| `tshd` → Teleport cluster | mTLS + reverse-tunnel (standard Teleport client flow via `lib/client`) | gRPC + SSH |

On Windows, the loopback channel uses mTLS because Windows has weaker socket isolation guarantees than Unix-domain sockets. On Linux/macOS, plain TCP loopback is used (and OS sandboxing handles isolation).

---

## 6. Packaging & Distribution

**Build configuration:** `electron-vite.config.mts` orchestrates four builds (renderer, main, preload, shared). **`electron-builder-config.js`** handles packaging:

| Platform | Output |
| --- | --- |
| macOS | DMG (universal: arm64 + amd64), code-signed + notarized |
| Windows | NSIS installer, code-signed via PowerShell signing |
| Linux | RPM + DEB + tar.gz |

Signing assets in `build_resources/`. Notarization on macOS uses Apple Developer credentials (per-build environment).

**Updates** via `electron-updater 6.x`, with a *custom provider* (`services/appUpdater/clientToolsUpdateProvider.ts`) that queries the cluster's `AutoUpdateService` (in `tshd`) for the right version + channel — i.e. **Teleterm updates are policy-driven by the connected Teleport cluster's managed-updates configuration**, not by a Gravitational-controlled update server alone.

```bash
pnpm start-term                  # electron-vite dev (renderer HMR, main auto-restart)
pnpm build-term                  # production build of all 4 processes
pnpm package-term                # build + electron-builder package (platform-specific installer)
pnpm release-connect             # full release pipeline (called from CI)
```

The `tshd` Go binary is built via the root `Makefile` (`make build/teleport` produces both `teleport` and the daemon helper; for packaging, see `release-connect` target in the root `Makefile`).

---

## 7. Integration Seams

(See `integration-architecture.md`.)

| Seam | This part ↔ | Channel |
| --- | --- | --- |
| S3 | `web/packages/teleterm/src/services/tshd/` ↔ `lib/teleterm/apiserver/` | gRPC over TCP loopback / mTLS on Windows |
| S4 | `lib/teleterm/` ↔ Teleport cluster | mTLS + reverse-tunnel via `lib/client` |
| S6 (indirect) | Renderer browser-style RDP uses the same `ironrdp` WASM as the web UI | wasm-bindgen |

---

## 8. Pointer Index

| Concept | File |
| --- | --- |
| Electron main entry | `web/packages/teleterm/src/main/index.ts` |
| tshd supervisor | `web/packages/teleterm/src/main/tshd/` |
| Window manager | `web/packages/teleterm/src/main/windowsManager.ts` |
| Renderer root | `web/packages/teleterm/src/ui/index.tsx` (or App.tsx) |
| Document model (tabs) | `web/packages/teleterm/src/ui/Document/` |
| Renderer-side tshd client | `web/packages/teleterm/src/services/tshd/` |
| Reverse-channel TshdEventsService | `web/packages/teleterm/src/services/tshdEvents/` |
| Renderer state stores | `web/packages/teleterm/src/services/store/` (immer-backed ImmutableStore) |
| Update provider | `web/packages/teleterm/src/services/appUpdater/clientToolsUpdateProvider.ts` |
| Electron-vite config | `web/packages/teleterm/electron-vite.config.mts` |
| Electron-builder config | `web/packages/teleterm/electron-builder-config.js` |
| CSP | `web/packages/teleterm/csp.ts` |
| Daemon entry | `lib/teleterm/teleterm.go` |
| Daemon gRPC server | `lib/teleterm/apiserver/` |
| Daemon cluster facades | `lib/teleterm/clusters/` |
| Daemon services | `lib/teleterm/services/<svc>/` |
| Daemon protos | `proto/teleport/lib/teleterm/v1/*.proto` |
| Auto-update protos | `proto/teleport/lib/teleterm/auto_update/` |
| VNet integration (daemon) | `lib/teleterm/vnet/` |
| VNet protos | `proto/teleport/lib/teleterm/vnet/` |

For everything else, refer to `findings/web-and-teleterm.md` (especially §9-13).
