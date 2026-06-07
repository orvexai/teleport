# Architecture — Web UI

> **Scope:** `web/packages/teleport/`, `web/packages/design/`, `web/packages/shared/`, `web/packages/build/`. The browser application served by the Teleport Proxy at `https://<cluster>/web`.
>
> **Deep dive:** `findings/web-and-teleterm.md` §1-8 (~250 lines on the web UI portion).

---

## 1. What This Part Is

A single-page React 19 application served from inside the Teleport proxy binary via Go's `embed.FS` (`webassets_embed.go`). Browsers talk to it over HTTPS on port 3080 (default). It is the *primary administrative UI* for users, admins, and operators.

License: **Apache-2.0** (different from the AGPL-3.0 core).

---

## 2. Workspace Layout

The web workspace is a `pnpm` workspace defined by `pnpm-workspace.yaml`:

```
web/packages/
├── teleport/           The actual web app (depends on design, shared)
├── design/             Design system (themed components, icons, layout primitives)
├── shared/             Cross-package utilities, browser-side libraries
│   ├── components/     Many "feature-shared" components reused by web app + Teleterm
│   └── libs/
│       ├── ironrdp/    Rust → WASM RDP renderer (in the Cargo workspace)
│       └── tdp/        Teleport Desktop Protocol client (TypeScript)
├── build/              Babel/SWC presets, Vite + Jest config, plugins
└── teleterm/           Electron desktop app — see architecture-teleterm.md
```

Enterprise overlay: `e/web/*` (pnpm workspace member, not present here). The OSS web app stands alone.

---

## 3. Top-Level Composition

The router and provider tree are wired in `web/packages/teleport/src/Teleport.tsx`:

```tsx
<QueryClientProvider>                  // @tanstack/react-query
  <ThemeProvider>                      //   @gravitational/design — theme + GlobalStyle
    <UserContext>                      //     current user / SSO state
      <TeleportContextProvider>        //       cluster info, feature flags, services singleton
        <Router>                       //         react-router v7 (custom Switch/Route compat layer)
          <Routes from src/config.ts:ossRoutes />
        </Router>
      </TeleportContextProvider>
    </UserContext>
  </ThemeProvider>
</QueryClientProvider>
```

Routes live centrally in `web/packages/teleport/src/config.ts` (`ossRoutes`). The enterprise overlay extends them via `eRoutes`. A **feature registry** in `features.tsx` maps each top-level feature to its route, icon, and access-gate predicate (so a feature can hide itself based on cluster license / user role).

The compat router shim (`web/packages/teleport/src/components/Router/Router.tsx`) keeps the legacy `<Switch>` / `<Route>` API on top of react-router v7's new API.

---

## 4. State Management

Three coexisting patterns:

1. **Class-based singleton stores on `TeleportContext`.** The "main" services (auth, audit, cluster) are class instances hung on a long-lived context. Older code reads them via `useTeleport()`.
2. **React Context.** Cross-cutting concerns (user, theme, features) live in their own context providers.
3. **React Query.** New endpoints use `@tanstack/react-query 5` with `useQuery` / `useMutation` hooks. The `QueryClient` is configured at the root.

There is **no Redux**, **no MobX**, **no Zustand**. `immer` is used (sparingly) for mutating local component state.

---

## 5. Data Layer

**The web UI does NOT use real gRPC over the network.** Despite many `@protobuf-ts/runtime` imports, the actual data layer is **REST/JSON over HTTPS**, defined in `web/packages/teleport/src/services/api/api.ts`. Generated protobuf-ts types are imported purely as **response-shape typing** — the wire format on `lib/web/*` endpoints is JSON.

Pattern per feature:

```
web/packages/teleport/src/services/<feature>/
├── types.ts         (optional) hand-rolled types
├── makeXxx.ts       Converters from API JSON → domain types
├── <feature>.ts     Functions that wrap api.get/post/put/delete + react-query hooks
```

Authenticated requests go through `services/api/api.ts` which adds CSRF / cookie / bearer handling and unwraps `lib/httplib`-style errors.

**Terminals + Desktop sessions** use a different transport: `AuthenticatedWebSocket` carrying length-prefixed protobuf-encoded frames (`web/packages/teleport/lib/term/protobuf.ts`). The terminal uses `@xterm/xterm` v6 with the Fit / Image / WebGL / Search / WebLinks addons (`lib/term/terminal.ts`). The desktop session decodes RDP frames in the browser via the WASM `ironrdp` library (`shared/libs/ironrdp/`).

---

## 6. Feature Inventory (`web/packages/teleport/src/`)

Each is a top-level folder containing route(s), page components, and service hooks:

| Folder | Purpose |
| --- | --- |
| `Authn/`, `Welcome/`, `Login/`, `Logout/` | Login flows: local, SSO, passwordless, MFA, headless |
| `Apps/` | Application Access — browse + launch HTTP apps |
| `Servers/` | SSH nodes — list + tabular details |
| `Databases/` | Database Access — list + connect |
| `Kubernetes/` | Kubernetes clusters — list + exec terminal |
| `Desktops/` | Windows Desktop Access — list + open RDP session |
| `Console/` | Live terminal (SSH + Kube exec + DB query + Desktop session) |
| `Sessions/` (active + recordings) | Active sessions, recording playback |
| `Audit/` | Audit log viewer with filters |
| `AccessRequests/` | Access request creation, review, approval |
| `Discover/` | Resource discovery / enrollment wizard |
| `Roles/` | Role editor (YAML + form) |
| `Users/` | User management |
| `Cluster/` | Cluster-level config (auth preference, networking config, …) |
| `JoinTokens/` | Provision token management |
| `Notifications/` | Notification center (cluster events for the user) |
| `Locks/` | Active locks (`KindLock`) |
| `TrustedClusters/` | Inbound / outbound trust |
| `Integrations/` | AWS OIDC, Azure OIDC, GitHub Actions integration setup |
| `Mwi/` | Machine & Workload Identity wizard |
| `Account/` | Personal account settings, MFA devices, hardware keys |
| `Support/` | Cluster info + diagnostics for support |
| `Headless/` | Headless SSH approval flow |
| `MCP/` | MCP server management |
| `Welcome/` | First-run + invite flows |
| `services/` | The API client layer (see §5) |
| `components/` | Web-app-specific shared components |
| `lib/` | Web-app-specific libraries (term/, …) |

The route → feature mapping is in `config.ts:ossRoutes`. The sidebar's order is in `features.tsx`.

---

## 7. Design System — `web/packages/design/`

Heavily themed component library used by the web app + Teleterm + any future Electron-side admin views. Component categories (one folder per primitive):

```
Alert  AnimatedProgressBar  Box  Button  ButtonIcon  ButtonLink  ButtonSelect
ButtonWithMenu  Card  CardError  CardIcon  CardSuccess  CardTile  Checkbox
CollapsibleInfoSection  DataTable  Dialog  DialogConfirmation  FieldRadio
Flex  Icon  Image  Indicator  Input  Label  LabelInput  LabelState  Link
Mark  Menu  Modal  MultiRowBox  Pill  Popover  RadioButton  RadioGroup
ResourceIcon  ShimmerBox  SlideTabs  Status  StatusIcon  StepSlider
SVGIcon  SyncStamp  Tabs  Tag  Text  TextArea  Theme  ThemeProvider
Toggle  Tooltip  TopNav
```

Plus:

- `theme/` — light + dark themes, design tokens.
- `system/` — `styled-system`-based responsive props (margin, padding, color, …).
- `assets/` — fonts, images.
- `datetime/`, `utils/` — formatting helpers.

Styling primitives: **`styled-components` v6** + `styled-system` + `@emotion/is-prop-valid`. Almost every component has a corresponding Storybook story.

---

## 8. Shared Library — `web/packages/shared/`

A *grab-bag* of components and utilities reused across web app + Teleterm + e2e tests. Components include:

```
AccessRequests / AdvancedSearchToggle / AnimatedTerminal / AwsLaunchButton
ButtonFileUpload / ButtonSso / ButtonTextWithAddIcon / Controls / CopyButton
DemoTerminal / DesktopSession / Editor / EmptyState / ErrorSuspenseWrapper
FieldCheckbox / FieldInput / FieldMultiInput / FieldSelect / FieldTextArea
FileTransfer / Highlight / LatencyDiagnostic / ListFilters / Markdown
MenuAction / MenuLogin / MenuLoginWithActionMenu / MissingPermissionsTooltip
```

Plus libraries in `shared/libs/`:

- **`ironrdp/`** — Rust crate compiled to WASM (in the Cargo workspace at the repo root). Built via `make build-ironrdp-wasm`; bundled by Vite via `vite-plugin-wasm`. Exposes a JS API to decode and render RDP frame streams (`init`, `decode_pdu`, …) for the browser-side desktop session.
- **`tdp/`** — TypeScript implementation of the Teleport Desktop Protocol (the wire framing used between the browser and `lib/srv/desktop`). Handles input forwarding (keyboard + mouse + clipboard) and resize negotiation.

---

## 9. Build & Test

| Goal | Command |
| --- | --- |
| Install | `pnpm install` |
| Dev (with HMR against a running cluster) | `pnpm start-teleport` |
| Build production assets | `pnpm build-ui-oss` (or `pnpm build-ui` for OSS+E) |
| Build the WASM RDP lib | `pnpm build-wasm` (which calls `make -C ../../../ build-ironrdp-wasm`) |
| Lint | `pnpm lint` (which is `oxlint` + `format-check`) |
| Format | `pnpm format` (oxfmt) |
| Type-check | `pnpm type-check` (tsgo / native TS compiler) |
| Unit tests | `pnpm test` (jest 30 + testing-library 16 + msw 2) |
| Watch mode | `pnpm tdd` |
| Storybook | `pnpm storybook` (port 9002) |
| Storybook tests | `pnpm test-storybook` (test-runner) |

Tooling notes:

- **Not eslint/prettier.** Linter is `oxlint` (`.oxlintrc.jsonc`); formatter is `oxfmt` (`.oxfmtrc.json`). Some eslint deps are present but only for Storybook plugin compatibility.
- **Native TS compiler.** Type-check uses `tsgo --build` from `@typescript/native-preview 7.0.0-dev` (the Go-based TS compiler). Legacy fallback: `pnpm type-check-legacy` uses `tsc`.
- **SWC for transforms.** `@vitejs/plugin-react-swc` + `@swc/plugin-styled-components`. Babel is present for Jest only.
- **Testing.** Jest 30 + `@testing-library/react` 16 + `@testing-library/jest-dom` + `msw` for API mocking. Snapshots are used sparingly.
- **Network mocking in stories.** `msw-storybook-addon` lets stories mock API responses without a real cluster.

---

## 10. Integration Seams

(See `integration-architecture.md`.)

| Seam | This part ↔ | Channel |
| --- | --- | --- |
| S1 | `web/packages/teleport/src/services/*.ts` ↔ `lib/web/apiserver.go` | HTTPS REST + WebSockets |
| S6 | `web/packages/shared/libs/ironrdp/*.ts` ↔ Rust `ironrdp` crate compiled to WASM | `wasm-bindgen` |
| S17 | `Desktops/`, `Console/` (browser) ↔ `lib/srv/desktop/` | WebSocket carrying TDP frames |

---

## 11. Pointer Index

| Concept | File |
| --- | --- |
| App root | `web/packages/teleport/src/Teleport.tsx` |
| Route table (OSS) | `web/packages/teleport/src/config.ts::ossRoutes` |
| Feature registry | `web/packages/teleport/src/features.tsx` |
| Router compat shim | `web/packages/teleport/src/components/Router/Router.tsx` |
| API client | `web/packages/teleport/src/services/api/api.ts` |
| WebSocket framing | `web/packages/teleport/lib/term/protobuf.ts` |
| xterm wrapper | `web/packages/teleport/lib/term/terminal.ts` |
| Design tokens | `web/packages/design/src/theme/` |
| Design components | `web/packages/design/src/<Component>/` |
| Shared components | `web/packages/shared/components/<Component>/` |
| Browser-side RDP (WASM) | `web/packages/shared/libs/ironrdp/` |
| Browser-side TDP | `web/packages/shared/libs/tdp/` |
| Vite config | `web/packages/build/vite/` |
| Jest config | `web/packages/build/jest/` |
| Storybook config | `web/.storybook/` |

For everything else, refer to `findings/web-and-teleterm.md`.
