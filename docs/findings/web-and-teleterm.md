# Web UI and Teleport Connect (Teleterm) — AI Architecture Notes

Scope: the React web app served by the Teleport proxy at `https://<cluster>/web` (package `@gravitational/teleport`) and the Electron desktop app *Teleport Connect* (package `@gravitational/teleterm` + Go daemon `lib/teleterm`).

Conventions
- All paths absolute from repo root `/home/daniel/repos/teleport`.
- "OSS" features live in the public tree; an `e/` overlay exists at `/home/daniel/repos/teleport/e/web/teleport/src/` (closed-source enterprise tree) — Vite/Jest path-alias `e-teleport/*` (see `web/packages/build/jest/config.js`).
- Workspace is a `pnpm` monorepo. The four shipped packages live under `web/packages/`: `teleport` (web app), `teleterm` (Electron app), `design` (design system) and `shared` (cross-package code). `build` is the tooling package.

---

## 1. Web UI — page/feature inventory

Top-level entry & shell:
- `web/packages/teleport/src/index.ts` — barrel re-export (`Context`, `ContextProvider`, `useTeleport`).
- `web/packages/teleport/src/boot.tsx` — DOM root entry. Reads server-injected `window['GRV_CONFIG']`, instantiates `TeleportContext`, mounts `<Teleport ctx={...} />` into `#app`. Optional OpenTelemetry boot via `localStorage` flag.
- `web/packages/teleport/src/Teleport.tsx` — top-level React tree (`QueryClientProvider` → `ThemeProvider` → `Router` → `Switch`); declares OSS public + private route groups and exports `getSharedPublicRoutes()`, `getSharedPrivateRoutes()` for the enterprise overlay to extend.
- `web/packages/teleport/src/Main/Main.tsx` — post-auth app shell. Owns `<Navigation>`, `<TopBar>`, `<BannerList>`, `<FeaturesContextProvider>`, alerts and feature-filtered route table.
- `web/packages/teleport/src/features.tsx` (980 LOC) — registry of `TeleportFeature` classes; each binds a `route` (path → component), a `navigationItem`, a `category` and an `hasAccess(flags)` predicate. `getOSSFeatures()` enumerates the OSS ones.
- `web/packages/teleport/src/teleportContext.tsx` — `TeleportContext` class instance constructed once in `boot.tsx`. Aggregates stateful "service" instances (audit, nodes, clusters, sessions, kube, …) plus `storeUser` / `storeNav` stores plus `getFeatureFlags()` → ACL-derived booleans.
- `web/packages/teleport/src/TeleportContextProvider.tsx` — React context wrapper for the above; consumed via `useTeleport()` (`web/packages/teleport/src/useTeleport.ts`).

Per-feature top-level folders under `web/packages/teleport/src/`. One bullet = one feature folder (purpose + entry file):

- `AccessRequests/LockedAccessRequests/LockedAccessRequests.tsx` — locked CTA when AccessRequests entitlement is missing (OSS placeholder; real review/checkout UI lives in `e-teleport`).
- `Account/` — user account settings (password, MFA devices, security, preferences). Entry `Account/Account.tsx`.
- `AppLauncher/` — `AppLauncher` route handler that exchanges proxy URL params for an app session cookie and redirects to a published HTTP app. Entry `AppLauncher/AppLauncher.tsx`.
- `Apps/` — App-server related flows. `Apps/AddApp/AddApp.tsx`, `Apps/MCPAppConnectDialog.tsx`, `Apps/TcpAppConnectDialog.tsx`. (App listing itself comes from `UnifiedResources`.)
- `Audit/Audit.tsx` (+ `useAuditEvents.ts`) — audit-event browser w/ infinite scroll & `EventList`/`EventDialog`.
- `AuthConnectors/` — list & edit SAML/OIDC/Github connectors. Entry `AuthConnectors/AuthConnectors.tsx`.
- `BotInstances/BotInstances.tsx` — list active bot instances.
- `Bots/` — list/add/inspect Machine ID bots. Sub-entries `Bots/Bots.tsx`, `Bots/Add/`, `Bots/Details/BotDetails.tsx`.
- `BrowserMFA/BrowserMFA.tsx` — page that satisfies a browser-initiated MFA challenge (used by tsh).
- `Clusters/Clusters.tsx` — list of registered clusters (root + leaves); plus `Clusters/ManageCluster/` for trust details.
- `Console/Console.tsx` — in-browser SSH/kube/db terminal multiplexer (see §6).
- `Databases/` — DB add wizard pieces.
- `Desktops/` — desktop listing UI (UnifiedResources actually lists; this folder mostly menus).
- `DesktopSession/DesktopSession.tsx` — in-browser RDP session using ironrdp WASM + TDP (`shared/components/DesktopSession`, `shared/libs/tdp`).
- `DeviceTrust/` — locked device-trust placeholder for OSS.
- `Discover/Discover.tsx` (+ `useDiscover.tsx`, `flow.tsx`, `resourceViewConfigs.ts`) — multi-step wizard that "discovers" / enrolls resources (Server, Database, Kubernetes, AWS console, ConnectMyComputer).
- `GitServers/` — list Git proxies.
- `HeadlessRequest/HeadlessRequest.tsx` — page rendered when a user opens a headless-auth approval link from tsh.
- `Instances/Instances.tsx` — list of Teleport instances (services that registered with the cluster).
- `Integrations/` (`Integrations/Integrations.tsx`, `Integrations/Enroll/`, `Integrations/status/`) — manage cluster integrations (AWS OIDC, AWS IAM Anywhere, GitHub, Okta, plugins, EAS).
- `JoinTokens/JoinTokens.tsx` — list/create node join tokens.
- `LocksV2/Locks.tsx`, `LocksV2/NewLock/` — identity & resource locks.
- `Login/Login.tsx` (+ `useLogin.ts`, `LoginSuccess.tsx`, `LoginFailed.tsx`, `LoginTerminalRedirect.tsx`, `LoginClose.tsx`, `Motd/`) — unauthenticated login flow with local/SSO/passwordless variants.
- `ManagedUpdates/ManagedUpdates/ManagedUpdates.tsx` — display managed update config & agent rollout state.
- `Nodes/` — fixtures only; node listing is part of `UnifiedResources`.
- `Notifications/Notifications.tsx`, `Notifications/Notification.tsx`, `notificationContentFactory.tsx` — in-app notification feed served from `/notifications`.
- `Recordings/` — session-recording listing (legacy entry; new entry is `SessionRecordings/`).
- `Roles/Roles.tsx` (+ `RoleEditor/`, `useRoles.ts`) — role list, YAML editor and a `RoleEditor/StandardEditor/` form-based editor backed by `use-immer`/`useImmerReducer`.
- `SamlApplications/` — list SAML apps registered with the SAML IdP.
- `SessionRecordings/list/ListSessionRecordingsRoute.tsx`, `SessionRecordings/view/ViewSessionRecordingRoute.tsx` — list & playback of session recordings (`/player/...`). Tty/desktop playback wired via `lib/player/`.
- `Sessions/Sessions.tsx` (+ `useSessions.ts`) — active SSH sessions list.
- `SingleLogoutFailed/SingleLogoutFailed.tsx` — terminal page for SAML SLO failure.
- `Support/Support.tsx` — version, license & download links.
- `TrustedClusters/` — trusted cluster management UI.
- `UnifiedResources/UnifiedResources.tsx` — the main resource list (cards or list) that fans out across servers/apps/dbs/desktops/kubes. Uses `shared/components/UnifiedResources/UnifiedResources.tsx` as the generic engine.
- `Users/Users.tsx` (+ `useUsers.ts`, `state.ts`, `UserAddEdit/`, `UserDelete/`, `UserDetails/`, `UserList/`, `UserReset/`, `UserTokenLink/`) — user management. Mutations use `useMutation` from React Query.
- `Welcome/Welcome.tsx` (+ `NewCredentials/`, `useToken.ts`, `CardWelcome.tsx`) — invite/reset-password onboarding (`/web/invite/:tokenId`, `/web/reset/:tokenId`).
- `WorkloadIdentity/WorkloadIdentities.tsx` — list workload-identity rules (SPIFFE).

Other support code (not features):
- `assets/`, `lib/` (auth-WebSocket, term/tty, tdp adapter, useMfa, player, util), `mocks/`, `services/`, `stores/`, `test/`, `Authn/` (does not exist; auth UI lives under `Login/` & `components/Authenticated`, `components/AuthnDialog`, `components/Onboard`).

---

## 2. Web UI — routing

- Library: `react-router` v7 (data-router mode). The package is pulled at workspace root (see imports of `createBrowserRouter`, `Outlet`, `useParams` in `web/packages/teleport/src/components/Router/Router.tsx`).
- Convention: a custom thin compatibility layer at `web/packages/teleport/src/components/Router/Router.tsx` re-implements pre-v6 idioms (`<Switch>`, `<Route path exact element title>`, `<Redirect>`, `<Prompt>`) on top of `createBrowserRouter` + `<Routes>`. It strips parent prefixes from absolute child paths (`stripTrailingSplat`, `RouteBaseProvider`) so feature components can declare full paths like `cfg.routes.audit` directly.
- The data router is instantiated once in `Router({children})` (single root pattern with `path: '*'` mounting `NavigationInitializer` → user-supplied children).
- All route patterns are centralized in `web/packages/teleport/src/config.ts`. `export const ossRoutes = {...}` at lines 111–211 is the authoritative list. The enterprise overlay extends `cfg.routes` and registers `nonExactRoutes` for "consumed by nested switches" routes.
- Top-level switch in `Teleport.tsx`: public routes (login, login-failed, login-success, invite, reset, saml-slo-failed, terminal-redirect, login-close) → then `<Authenticated>` gate → `<UserContextProvider>` → `<TeleportContextProvider>` → either `AppLauncher` (special direct-app launch) or `privateOSSRoutes()` (player, console, desktop, headlessSSO, browserMFA, and finally `<Main features=getOSSFeatures()>`).
- Inside `Main`, each feature's `route.path` is registered through `<Route>` children inside `<Switch>` again — declarative composition via the `TeleportFeature` interface in `web/packages/teleport/src/types.ts`.
- `Console`, `DesktopSession`, `SessionRecordings/view/...` are mounted outside `Main` (they own the full viewport).

---

## 3. Web UI — data fetching pattern

- `@tanstack/react-query` v5 (single `QueryClient` instantiated in `web/packages/teleport/src/Teleport.tsx` lines 51–63 with defaults `{ networkMode: 'always', refetchOnWindowFocus: false, retry: false }`). Teleterm uses its own `QueryClient` in `web/packages/teleterm/src/ui/App.tsx`.
- Two coexisting patterns:
  1. **Classic class-based "service" pattern.** `TeleportContext` (`teleportContext.tsx`) constructs singleton services (`NodeService`, `AuditService`, `ClustersService`, `ResourceService`, …) that wrap `web/packages/teleport/src/services/api/api.ts` (a small `fetch` wrapper that injects bearer/MFA headers and parses `ApiError`). Components fetch via `useAttempt`/`useAsync` hooks (`shared/hooks/useAttemptNext`, `shared/hooks/useAsync`). Example: `web/packages/teleport/src/Sessions/useSessions.ts`.
  2. **Modern React Query hooks** for newer surfaces. Examples:
     - `web/packages/teleport/src/Audit/useAuditEvents.ts` → `useInfiniteQuery` with `queryKey: ['audit_events', clusterId, fromParam, …]` and `keepPreviousData`.
     - `web/packages/teleport/src/Instances/Instances.tsx` → `useInfiniteQuery`.
     - `web/packages/teleport/src/Users/UserAddEdit/UserAddEdit.tsx`, `UserDelete/UserDelete.tsx` → `useMutation` with manual `queryClient.invalidateQueries`.
     - `web/packages/teleport/src/lib/locks/useResourceLock.ts` → `useMutation` + `useQuery`.
     - `web/packages/teleport/src/ManagedUpdates/ManagedUpdates/ManagedUpdates.tsx`, `WorkloadIdentity/WorkloadIdentities.tsx` → `useQuery` + `keepPreviousData`.
- Hook factory: `web/packages/teleport/src/services/queryHelpers.ts` exposes `wrapQuery(...)` → `{ queryKey, createQueryKey, useQuery, useInfiniteQuery }`. Naming convention is `useGetX`/`useListX` for queries and explicit `useDelete<Resource>` etc. for mutations. See JSDoc lines 178–250 for the canonical usage example.
- Suspense queries: `useSuspenseQuery` from React Query used in `web/packages/teleport/src/SessionRecordings/view/player/tty/TtyRecordingPlayer.tsx`.
- Caching note: where the older `services/userPreferences/userPreferences.ts` does its own in-module promise cache (`cache.pendingPreferences`), newer code defers caching to React Query.

---

## 4. Web UI — state management

- No Redux. State is split between three layers:
  1. **Per-feature React state / hooks** (`useState`, custom `use<Feature>` hooks following old container pattern).
  2. **Class-based singleton "stores"** — see `web/packages/teleport/src/stores/`. `StoreUserContext`, `StoreNav` extend `shared/libs/stores/store` (a tiny EventEmitter store with `setState`/`subscribe`/`useStore`). The `TeleportContext` instance from `boot.tsx` holds these — components access via `useTeleport()` → `ctx.storeUser` / `useStore(ctx.storeUser)`.
  3. **React Context** for cross-cutting concerns:
     - `TeleportContextProvider` (the singleton bag of services + stores).
     - `User/UserContext.tsx` — `UserContextProvider` loads `UserPreferences` (with localStorage fallback) and exposes `useUser()` (preferences, mutators, cluster-pinned-resources cache via `useRef`).
     - `FeaturesContext.tsx` — `FeaturesContextProvider` / `useFeatures()` for the registered, ACL-filtered feature list.
     - `Main/LayoutContext.tsx` — `LayoutContextProvider` for top-bar / sidebar layout flags.
     - `shared/components/ToastNotification` → `ToastNotificationProvider`.
     - `Console/consoleContext` — `ConsoleContext` holds the open-documents stores and tty factory.
- **Immer usage** is narrow but present:
  - `web/packages/teleport/src/Roles/RoleEditor/StandardEditor/useStandardModel.ts` uses `useImmerReducer` (from `use-immer`) for the role-editor form model.
  - In Teleterm (see §9–§12), Immer is the primary state mechanism (`web/packages/teleterm/src/ui/services/immutableStore/immutableStore.ts` wraps `produce()` over a `Store`; `workspacesService.ts` and `clustersService.ts` use `produce`/`applyPatches`).
- Global flags (`isEnterprise`, `isCloud`, `entitlements`, `automaticUpgrades`, `lockedFeatures`) live on the `TeleportContext` and on the imported `cfg` (`web/packages/teleport/src/config.ts`) initialized once from `window.GRV_CONFIG` injected by the proxy.

---

## 5. Web UI — gRPC client over HTTP

- The web app **does not** speak gRPC over HTTP at runtime. It calls REST/JSON endpoints under `/v1/webapi/*` through `web/packages/teleport/src/services/api/api.ts` (`api.get/post/put/delete` etc.). MFA is layered in via the `Teleport-Mfa-Response` header (`MFA_HEADER`).
- Generated protobuf TS types are used purely as **typed message schemas** when proto-based response shapes are returned by REST endpoints or used as request bodies. They are imported via the path alias `gen-proto-ts/*` (mapped by `web/packages/build/jest/config.js` to `/home/daniel/repos/teleport/gen/proto/ts/*`).
- Generator: `@protobuf-ts` (see Teleterm imports, e.g. `gen-proto-ts/teleport/userpreferences/v1/userpreferences_pb`). The generated TS code is checked in under `/home/daniel/repos/teleport/gen/proto/ts/`.
- Examples of proto types used by the web app:
  - `web/packages/teleport/src/services/userPreferences/userPreferences.ts` imports `UserPreferences`, `Theme`, `SideNavDrawerMode`, etc., and translates between the wire JSON shape returned by `/webapi/user/preferences` and the proto enums.
  - `web/packages/teleport/src/services/mfa/mfaOptions.test.ts` re-uses the teleterm SSO challenge types for shared validation.
- `@bufbuild/protobuf` / `connect-rpc` are **not** used.
- The only place where actual gRPC (over `@grpc/grpc-js`) is consumed in the front-end is the Electron app (Teleterm) — see §9.

---

## 6. Web UI — WebSocket / terminal

- Custom WebSocket wrapper: `web/packages/teleport/src/lib/AuthenticatedWebSocket.ts`. Extends native `WebSocket`, buffers messages until it has authenticated by sending the bearer token from `getAccessToken()` over the open socket.
- TTY abstraction: `web/packages/teleport/src/lib/term/tty.ts` (`class Tty extends EventEmitterMfaSender`). Opens an `AuthenticatedWebSocket` against `addressResolver.getConnStr(w,h)`, sets `binaryType = 'arraybuffer'`, and frames messages with `Protobuf` from `lib/term/protobuf.ts` (typed wire format: raw data, MFA challenge response, kube-exec data, db-connect data, file-transfer requests).
- xterm wrapper: `web/packages/teleport/src/lib/term/terminal.ts` (`class TtyTerminal`). Loads addons in `open()`:
  - `@xterm/addon-fit` (`FitAddon`) — always loaded; window-resize triggers `_debouncedResize`.
  - `@xterm/addon-web-links` — clickable URLs.
  - `shared/components/TerminalSearch` `SearchAddon` — Ctrl/Cmd+F search.
  - `@xterm/addon-image` (sixel/iTerm-image) — loaded inside `try/catch` because it depends on WebAssembly. The Vite plugin `web/packages/build/vite/guard-wasm.ts` rewrites bare `WebAssembly` references so the static import succeeds even where WASM is forbidden.
  - `@xterm/addon-webgl` — also `try/catch`; on construction-throw or `onContextLoss`, it is disposed and the renderer falls back to the canvas/DOM renderer. WebGL is the preferred renderer.
- Term-events: `lib/term/enums.ts` (`TermEvent.CLOSE`, `CONN_CLOSE`, `SESSION`, `LATENCY`, `DATA`, `RESIZE`, `RESET`).
- SSH page wiring: `web/packages/teleport/src/Console/DocumentSsh/DocumentSsh.tsx` (renders the terminal + file-transfer UI), `useSshSession.ts` (creates the Tty via `ConsoleContext`), `Terminal/Terminal.tsx` (instantiates `XTermCtrl`, registers keyboard handlers including the Alt-arrow workaround for word navigation, see lines 154–192 of `terminal.ts`).
- Kube exec / db connect reuse the same `Tty` + xterm stack (`Console/DocumentKubeExec`, `Console/DocumentDb`).
- Desktop session (RDP-in-browser): `web/packages/teleport/src/DesktopSession/DesktopSession.tsx` builds a TDP transport from an `AuthenticatedWebSocket` (`lib/tdp/webSocketTransportAdapter.ts`, `lib/tdp/playerClient.ts`) and feeds the shared `shared/components/DesktopSession/DesktopSession.tsx` + `shared/libs/tdp/client.ts` `TdpClient`, which uses the **ironrdp** WASM module (`shared/libs/ironrdp/pkg/ironrdp` + inlined `ironrdp_bg.wasm?inline`) for fast-path RDP frame decoding into a canvas.
- Session-recording playback (TTY) reuses `lib/term` via `lib/term/ttyPlayer.js` and `SessionRecordings/view/player/tty/TtyRecordingPlayer.tsx`.

---

## 7. Web UI — design system overview

Package: `@gravitational/design` at `web/packages/design/src/`.

- **Theme tokens** — `web/packages/design/src/theme/index.ts` re-exports `darkTheme`, `lightTheme`, `bblpTheme` (Bloomberg-only theme), and `breakpointsPx`. Each theme is a plain TS object implementing `Theme` (`themes/types.ts`) and is composed from `themes/sharedStyles.ts` (typography, space scale, border-radii, breakpoints, `dataVisualisationColors` palette). Color tokens include `levels` (deep / sunken / surface / elevated / popout), `spotBackground[3]` alpha overlays, semantic `text`, `interactive`, `accent`, `link`, `terminal`, etc.
- **Color modes** — selection lives in `web/packages/teleport/src/ThemeProvider.tsx` (web) / `web/packages/teleterm/src/ui/ThemeProvider/theme.ts` (Connect). The selected theme is mirrored to `localStorage` (key from `KeysEnum.USER_PREFERENCES`) and follows OS `prefers-color-scheme` for the favicon (see `web/packages/teleport/src/Teleport.tsx` `updateFavicon`). Web also supports custom client themes (`bblp`, `mc`) configured via `cfg.customTheme`.
- **ThemeProvider** — `web/packages/design/src/ThemeProvider/index.tsx` (`ConfiguredThemeProvider`) wraps `styled-components`' `ThemeProvider` + `StyleSheetManager` + a default `shouldForwardProp` filter built from `@emotion/is-prop-valid` (to keep `styled-system` props off DOM nodes). `globals.js` injects global CSS.
- **styled-system layer** — `web/packages/design/src/system/index.ts` re-exports the standard `styled-system` style functions (`space`, `color`, `flex`, `width`, …) plus custom additions (`gap`, `rowGap`, `columnGap`, `boxShadow`, `whiteSpace`) that read from theme keys. Typography helpers in `system/typography.ts`. Primitives (`Box`, `Flex`, `Text`, `Button`, …) compose these and accept `mt={2} bg="levels.surface" color="text.main"` etc.
- **Component categories** (67 top-level dirs in `web/packages/design/src/`):
  - **Layout/primitives:** `Box`, `Flex`, `Card`, `CardTile`, `CardError`, `CardIcon`, `CardSuccess`, `MultiRowBox`, `TopNav`, `Indicator`, `Image`, `Mark`.
  - **Forms:** `Input`, `LabelInput`, `TextArea`, `Checkbox`, `RadioButton`, `RadioGroup`, `FieldRadio`, `Toggle`, `ButtonSelect`, `DatePicker.tsx`.
  - **Buttons:** `Button` (+ `ButtonPrimary`/`ButtonSecondary`/`ButtonBorder`/`ButtonWarning`/`ButtonText`), `ButtonIcon`, `ButtonLink`, `ButtonWithMenu`.
  - **Data display:** `DataTable/` (with `Cells.tsx`, `Pager`, `InputSearch`, `useTable.ts`, `sort.ts`), `Pill`, `Label`, `LabelState`, `Tag`, `Status`, `StatusIcon`, `SyncStamp`, `ShimmerBox`.
  - **Navigation:** `Menu`, `Tabs`, `SlideTabs`, `StepSlider`.
  - **Feedback / overlays:** `Alert`, `AnimatedProgressBar`, `Dialog`, `DialogConfirmation`, `Modal`, `Popover`, `Tooltip`, `CollapsibleInfoSection`.
  - **Icons:** `Icon/Icon.tsx` + 229 SVG components in `Icon/Icons/` (auto-generated by `Icon/script/`), plus `ResourceIcon/` (resource-kind glyphs) and `SVGIcon/`.
  - **Utility:** `utils/copyToClipboard.ts`, `utils/useResizeObserver.ts`, `utils/useElementSize.ts`, `utils/mergeRefs.ts`, `platform.ts` (`getPlatformType()`), `keyframes.ts`, `formatters.ts`, `constants.ts`, `logger.ts`.
- The barrel `web/packages/design/src/index.ts` exports almost every component for `import { Box, Flex, Button, Indicator } from 'design';`.

---

## 8. Web UI — testing

- **Test runner:** Jest. Shared config at `web/packages/build/jest/config.js`.
  - Uses a patched JSDOM env at `jest-environment-patched-jsdom.js`.
  - Module aliases: `shared/* → web/packages/shared/*`, `design → web/packages/design/src/*`, `teleport → web/packages/teleport/src/*`, `teleterm → web/packages/teleterm/src/*`, `e-teleport/* → e/web/teleport/src/*`, `gen-proto-ts/* → gen/proto/ts/*`, `gen-proto-js/* → gen/proto/js/*`.
  - CSS / image / YAML imports mocked. The ironrdp WASM import (`shared/libs/ironrdp/pkg/ironrdp_bg.wasm?inline`) is mocked by `web/packages/shared/libs/ironrdp/mock_ironrdp.js`.
- **Setup file:** `web/packages/build/jest/setupTests.ts`. Loads `@testing-library/react` (`configure(act)`), `jest-fail-on-console` (with an extendable ignore-list from `e/web/testsWithIgnoredConsole.js`), `jsdom-testing-mocks` (`configMocks({ act })`).
- **Libraries:** `@testing-library/react` ^16.3, `@testing-library/jest-dom` ^6.9, `@testing-library/user-event` ^14.6, `jest-websocket-mock` (`web/packages/teleport/package.json` devDep).
- **MSW usage** is currently **Storybook-first**, not Jest-first. `msw` v2.14.2 + `msw-storybook-addon` are configured at the repo root (`package.json` lines 57–58, 105). Story files declare per-story `parameters.msw.handlers` of `http.get/post(...)` (examples: `web/packages/teleport/src/Notifications/Notification.story.tsx`, `web/packages/teleport/src/Integrations/status/AwsRa/AwsRaDashboard.story.tsx`, `web/packages/teleport/src/Console/Console.story.tsx`, `web/packages/teleport/src/Clusters/ManageCluster/ManageCluster.test.tsx`). A small number of `.test.tsx` files (e.g. `Clusters/ManageCluster/ManageCluster.test.tsx`) also use MSW; for most tests, however, services are mocked by hand via `jest.mock('teleport/services/...')` or fixtures under `*/fixtures/`.
- **Test data** — feature folders each carry a `fixtures/` subdir (e.g. `Audit/fixtures`, `Sessions/fixtures`, `Apps/fixtures`); shared `web/packages/teleport/src/mocks/` and `web/packages/teleport/src/test/helpers/` provide `MemoryRouter` wrappers and resource builders.
- **Storybook:** `web/.storybook/main.mts` (uses `@storybook/react-vite`, Vite config at `web/.storybook/vite.config.mts`). Stories follow `*.story.tsx` and are discovered under both `web/packages/**` and `e/web/**`.

---

## 9. Teleterm — process architecture

Three OS processes plus an embedded native binary:

```
+--------------------------------------------------------------------+
|  Electron Main process  (Node.js)                                  |
|    src/main.ts → src/mainProcess/mainProcess.ts                    |
|    - spawns tshd:      child_process.spawn(<tsh binary>, ['daemon','start',...])
|    - forks sharedProcess: child_process.fork(sharedProcess.js)     |
|    - exposes app-level IPC to renderer over ipcMain.handle/on      |
|    - holds clusterStore, AppUpdater, AgentRunner, configService    |
|    +------ stdio: pipe ------+    +------ stdio: pipe ----------+  |
+--|---------------------------|----|------------------------------|-+
   |  resolveNetworkAddress    |    | resolveNetworkAddress         |
   v  reads gRPC addr stdout   v    v                               v
+--------------------+   +--------------------+   +------------------+
| tshd child         |   | sharedProcess      |   | Renderer process |
| (Go: lib/teleterm) |   | (Node, src/shared- |   | (Chromium)       |
| gRPC server:       |   |  Process/...)      |   |  preload.ts +    |
|   TerminalService  |   | gRPC server:       |   |  src/ui/App.tsx  |
|   VnetService      |   |   PtyHostService   |   |                  |
|   AutoUpdateSvc    |   |                    |   |                  |
+--------+-----------+   +--------+-----------+   +--------+---------+
         ^                        ^                        |
         |  gRPC over TCP         |  gRPC over TCP         |
         |  (mTLS on Windows,     |  (mTLS on Windows,     |
         |   plaintext on         |   plaintext on         |
         |   *nix; see grpc-      |   *nix)                |
         |   Credentials)         +<-----------------------+
         +--------------------------------------------------+
                                                            |
   Reverse channel: tshd → renderer over gRPC               |
   "TshdEventsService" (server in renderer, client in tshd) +
   - server hosted by renderer via grpc-js in tshdEvents/index.ts
   - tshd dials it after preload calls UpdateTshdEventsServerAddress
```

Key files:
- Main entry: `web/packages/teleterm/src/main.ts` — Electron `app.requestSingleInstanceLock`, sets custom protocol (`teleport://` deep links), wires `WindowsManager`, configures `nativeTheme`, instantiates `MainProcess`, drives lifecycle (`will-quit`, second-instance, open-url, deep links).
- Main-process orchestrator: `web/packages/teleterm/src/mainProcess/mainProcess.ts` — spawns the **`tsh daemon start`** binary (`settings.tshd.binaryPath`) and the Node `sharedProcess.js`, captures their stdio, derives the gRPC bind addresses from stdout (`resolveNetworkAddress`), spins up `tshdClients` (`TerminalService`, `AutoUpdateService`) inside the main process for state-mirroring purposes, and exposes `MainProcessClient` to the renderer over Electron IPC.
- Preload bridge: `web/packages/teleterm/src/preload.ts` — runs in renderer's preload context, builds *separate* gRPC clients (renderer copies of `TerminalServiceClient`, `VnetServiceClient`, `PtyServiceClient`) via `@protobuf-ts/grpc-transport` over `@grpc/grpc-js`. Also stands up a **`TshdEventsService` gRPC *server*** in the renderer (`createTshdEventsServer` in `src/services/tshdEvents/index.ts`) and tells tshd its address via `tshClient.updateTshdEventsServerAddress`. Exposes the resulting object to renderer via `contextBridge.exposeInMainWorld('electron', ...)`.
- Renderer entry: `web/packages/teleterm/src/ui/boot.tsx` → `App.tsx` → `AppContextProvider` (singleton `AppContext` from `src/ui/appContext.ts`).
- Transport / credentials: `web/packages/teleterm/src/services/grpcCredentials/` (`shouldEncryptConnection`, `createClientCredentials`, `createServerCredentials`, `generateAndSaveGrpcCert`, `readGrpcCert`). On Windows the channels are mTLS using node-forge certs persisted under `settings.certsDir`. On macOS/Linux they are plaintext localhost.
- Per-process logging: each child's stdout/stderr is piped through `web/packages/teleterm/src/services/logger/` (Winston-backed `createFileLoggerService`) into rotated files plus a `KeepLastChunks` buffer for crash dialogs.

---

## 10. Teleterm — Go daemon (`lib/teleterm`)

Package layout (under `/home/daniel/repos/teleport/lib/teleterm`):

- `teleterm.go` — `Serve(ctx, cfg)` entry. Sets up TLS-mode credentials (mTLS on Windows, plaintext otherwise via `createGRPCCredentials`), `clusters.NewStorage` (file-backed `client.FSClientStore` rooted at `cfg.HomeDir`, configured with a YubiKey PIV `hwks` service that prompts the user through the tshd events client), `daemon.New(...)`, `apiserver.New(...)`. Starts a hardware-key agent server in parallel when enabled.
- `config.go` — `Config` struct (Addr, CertsDir, PrehogAddr, KubeconfigsDir, AgentsDir, HardwareKeyAgent, InsecureSkipVerify, WebauthnLogin, etc.) + `CheckAndSetDefaults`.
- `grpccredentials.go` — cert generation / verification helpers.
- `daemon/` — `daemon.go` is the central `Service` (~lots of LOC). Wraps `clusters.Storage`, builds a lazy `TshdEventsClient` (`tshdEvents.go`, `events_client.go`) used to call back into the renderer for `Relogin`, `SendNotification`, `SendPendingHeadlessAuthentication`, MFA prompts and hardware-key prompts. Owns the per-tenant gateway lifecycle (`gateway.go`, headless auth, MFA prompts, hardware-key prompts), the usage reporter (`usagereporter/daemon`), and the modal-dispatch queue (`maxConcurrentImportantModals = 1`).
- `clusteridcache/` — in-memory cache mapping tenant URI to clusterID.
- `clusters/` — domain-specific cluster facades that wrap an `apiclient.Client` and the `lib/client` SDK:
  - `cluster.go` (lifecycle, login state)
  - `cluster_auth.go` / `cluster_headless.go` (SSO, headless auth)
  - `cluster_apps.go`, `cluster_databases.go`, `cluster_kubes.go`, `cluster_windows_desktops.go`, `cluster_resources.go`, `cluster_access_requests.go`, `cluster_file_transfer.go`, `cluster_gateways.go`, `cluster_leaves.go` (per-resource).
  - `gateway_creator.go`, `storage.go`, `config.go`.
- `gateway/` (parent dir, not shown above) — per-protocol local proxy gateways (DB, app, kube). Spawned by `daemon` to provide localhost ports for CLI clients.
- `gatewaytest/` — test helpers.
- `vnet/` — Cross-platform VNet daemon adapter exposing the `VnetService` gRPC API (`service.go` plus per-OS files `service_darwin.go`, `service_daemon_darwin.go`, `service_linux.go`, `service_windows.go`, `service_other.go`). Manages the userspace TUN device, DNS interceptor, admin-process IPC.
- `services/` — feature-specific RPC implementations split from `daemon`:
  - `connectmycomputer/` — agent management.
  - `desktop/` — Windows desktop session orchestration.
  - `unifiedresources/` — paginated cross-resource search.
  - `userpreferences/` — wraps user-preferences endpoints.
- `apiserver/` — gRPC server boilerplate.
  - `apiserver.go` — registers `TerminalServiceServer`, `VnetServiceServer`, `AutoUpdateServiceServer`. Single `grpc.NewServer` with `MaxConcurrentStreams = defaults.GRPCMaxConcurrentStreams` and an error-mapping interceptor.
  - `middleware.go` — error logging / panic recovery.
  - `config.go`.
  - `handler/` — one Go file per resource exposing each TerminalService RPC (`handler_access_requests.go`, `handler_apps.go`, `handler_auth.go`, `handler_clusters.go`, `handler_connectmycomputer.go`, `handler_databases.go`, `handler_desktops.go`, `handler_device_trust.go`, `handler_file_transfer.go`, `handler_gateways.go`, `handler_headless.go`, `handler_kubes.go`, `handler_tshd_events.go`, `handler_unified_resources.go`, `handler_usage_events.go`, `handler_user_preferences.go`, plus `time_converter.go`).
- `autoupdate/` — implements `AutoUpdateService` for Connect's auto-update flow.
- `cmd/` — helpers to build `tsh` subcommands (used by gateway processes).
- `api/` — uri helpers (`api/uri` package referenced by daemon).
- `teleterm_test.go`, `webauthnmock.go` — testing surface.

Proto definitions (input to the generators) live in `/home/daniel/repos/teleport/proto/teleport/lib/teleterm/`:
- `v1/`: `service.proto` (the `TerminalService` definition, the bulk of the API), `tshd_events_service.proto` (reverse channel), `cluster.proto`, `auth_settings.proto`, `database.proto`, `gateway.proto`, `kube.proto`, `app.proto`, `label.proto`, `server.proto`, `windows_desktop.proto`, `access_request.proto`, `target_health.proto`, `usage_events.proto`, `vnet_service.proto`.
- `vnet/v1/` and `auto_update/v1/` for the other two services.
- Generated outputs: Go → `gen/proto/go/teleport/lib/teleterm/...`; TS → `gen/proto/ts/teleport/lib/teleterm/...` and `.client.ts` / `.grpc-server.ts`.

---

## 11. Teleterm — Electron main

- Single entry `web/packages/teleterm/src/main.ts` (covered in §9). Notable behavior:
  - `app.requestSingleInstanceLock()` enforces one running instance; subsequent launches focus the existing window through `'second-instance'`.
  - On macOS, an early `'open-url'` listener buffers the URL into a module-level variable so a deep link that fires during async init isn't lost. `setUpDeepLinks` later drains it.
  - Custom protocol: `CUSTOM_PROTOCOL = 'teleport'` from `web/packages/shared/deepLinks.ts`; registered with `app.setAsDefaultProtocolClient`.
  - Hard sets `app.commandLine.appendSwitch('gtk-version', '3')` to work around Electron 36 GTK4 issues.
  - On Linux/macOS/Windows it relocates the Electron `sessionData` cache directory to OS conventions (`updateSessionDataPath`).
  - On startup runs `migrateOldTshHomeOnce` (legacy tsh-home migration; scheduled for removal in v20).
- **Window management**: `web/packages/teleterm/src/mainProcess/windowsManager.ts` — owns the `BrowserWindow`, persists window geometry under storage key `'windowState'`, builds context menus (`selectionContextMenu`, `inputContextMenu`), exposes:
  - `frontendAppInit` promise resolved when the renderer signals `WindowsManagerIpc.SignalUserInterfaceReadiness`.
  - `enterBackgroundMode()` / `isInBackgroundMode` for the run-in-background tray flow.
  - `crashWindowPromise` for showing a crash window.
  - `launchDeepLink(parseResult)` forwards parsed deep links to the renderer.
- **OS integration**:
  - `tray.ts` — system-tray icon + menu (gated by config `runInBackground`).
  - `mainProcess/protocolHandler.ts` — `setUpProtocolHandlers` registers the file `protocol://` schemes for packaged-app assets; `registerAppFileProtocol` whitelists app file URLs.
  - `mainProcess/navigationHandler.ts` — blocks non-whitelisted in-app navigation (`registerNavigationHandlers`).
  - `mainProcess/contextMenus/` — tab and terminal context menus (`tabContextMenu`, `terminalContextMenu`).
- **Native notifications** — implemented in the renderer via `NotificationsService` (`src/ui/services/notifications`) but the actual OS notification fires via Electron's `Notification` API surfaced over IPC; tshd → renderer notifications come through `TshdNotificationsService` (`src/ui/services/tshdNotifications/tshdNotificationService.ts`) driven by the reverse `TshdEventsService` `SendNotification` RPC.
- **Auto-update flow** — `web/packages/teleterm/src/services/appUpdater/`:
  - `appUpdater.ts` wraps `electron-updater` (`UpdateInfo`, `download progress`, `quitAndInstall`).
  - `clientToolsUpdateProvider.ts` is a custom `electron-updater` provider that consults `/v1/webapi/find` of the user's cluster and the `AutoUpdateService` gRPC client (`autoUpdateService.getConfig/getClusterVersions/getInstallationMetadata`) for the target version.
  - `autoUpdatesStatus.ts` computes the user-visible state.
  - `nsisDualModeUpdater.ts` handles the Windows per-user vs per-machine NSIS update split.
  - `errors.ts`, `index.ts`, tests.
  - Events are emitted to the renderer via `RendererIpc.AppUpdateEvent` (see `mainProcess.ts` lines 211–219). Renderer subscribes through `src/ui/AppUpdater/`.
- **Other main-side services in `mainProcessClient`**:
  - `agentRunner/` (manages "Connect My Computer" tsh agent child processes); `agentDownloader/` (downloads the agent tarball with `FileDownloader`); `agentCleanupDaemon/` (separate node script that kills orphaned agents).
  - `awaitableSender/`, `terminateWithTimeout/`, `ipcSerializer.ts`, `profileWatcher/`, `clusterLifecycleManager/`, `clusterStore/` (canonical cluster list mirrored from tshd, queried via the main-process tshd client).

---

## 12. Teleterm — Electron renderer

Entry: `web/packages/teleterm/src/ui/boot.tsx` → `App.tsx`.

- **Top-level providers** (in `src/ui/App.tsx`):
  - `CatchError` → `StyledApp` (`./components/App`) → `DndProvider` (`react-dnd-html5-backend`, used for tab reordering) → `AppContextProvider` (singleton instance of `appContext.ts`) → `QueryClientProvider` → `AppUpdaterContextProvider` → `ResourcesContextProvider` → `ConnectionsContextProvider` → `VnetContextProvider` → `ThemeProvider` → `DeepLinks` + `AppInitializer`.
- **Top-level feature folders** (under `web/packages/teleterm/src/ui/`):
  - `AppInitializer/` — pulls initial state (`appCtx.pullInitialState()`), shows splash / restores workspaces, registers the tshd-events context-bridge service (`createTshdEventsContextBridgeService` in `tshdEvents.ts`).
  - `AppUpdater/` — UI for the auto-updater (banner, modal).
  - `Vnet/` — local Teleport VNet panel and status (subscribes to `VnetServiceClient` from preload).
  - `Search/` — global command palette / fuzzy search (resources, profiles, commands).
  - `TopBar/` — top bar with workspace switcher, profile menu, VNet status, Connections menu (`Connections/connectionsContext`).
  - `StatusBar/` — bottom bar, e.g. `DesktopSessionControls`.
  - `TabHost/` — tab-strip + open-document orchestration (`TabHost.tsx`, `useTabShortcuts`, `useNewTabOpener`, `ClusterConnectPanel/`).
  - `Tabs/` — re-orderable tab UI.
  - `Documents/` (`DocumentsRenderer.tsx`, `workspaceContext.tsx`, `KeyboardShortcutsPanel.tsx`) — renders the currently active workspace's documents.
  - `Document/` — generic shell for a document (visibility, focus, foreground-session helpers).
  - `DocumentCluster/` — main resource browser (UnifiedResources-backed); contains `resourcesContext`.
  - `DocumentTerminal/` — SSH / shell terminal documents. Wraps `Terminal/` (xterm host) and `useDocumentTerminal.ts` (binds to pty service).
  - `DocumentDesktopSession/DocumentDesktopSession.tsx` — RDP session inside Connect. Uses the **same shared TDP client** (`shared/libs/tdp`, `shared/components/DesktopSession`) and **ironrdp WASM** module as the web app, but the WebSocket transport is replaced by a streaming gRPC call surfaced through `TshdClient` (see `cloneAbortSignal`, `setSharedDirectoryForDesktopSession` blocked in renderer, and `MainProcessClient` for OS-side dialogs).
  - `DocumentGateway/`, `DocumentGatewayApp/`, `DocumentGatewayCliClient/`, `DocumentGatewayKube/` — local-proxy gateway tabs that wrap tshd `createGateway`/`startGatewayCliClient`.
  - `DocumentAccessRequests/`, `DocumentAuthorizeWebSession/` — request-flow tabs.
  - `DocumentsReopen/` — restore previous-session UI.
  - `AccessRequests/` + `AccessRequestCheckout/` — sidebar checkout UX.
  - `ClusterConnect/`, `ClusterLogout/` — login/logout modals.
  - `ConnectMyComputer/` — sidebar wizard / status for the local agent.
  - `DeepLinks/` — handles parsed deep-link payloads launched from main.
  - `HeadlessAuthn/` — modal for headless auth approval (driven by tshd events).
  - `ModalsHost/` — central modal stack (`modalsService`).
  - `LayoutManager.tsx` — splits screen into nav/topbar/documents.
  - Supporting code: `appContext.ts`, `appContextProvider.tsx`, `commandLauncher.ts`, `tshdEvents.ts`, `types.ts`, `uri.ts` (typed resource URIs and `routing` helpers), `utils/`, `hooks/`, `fixtures/`, `ThemeProvider/`.
- **gRPC-to-tshd client** lives in **preload** (`src/preload.ts`) and is exposed to renderer through `contextBridge`. The wrapped clients are:
  - `TshdClient` = `cloneClient(new TerminalServiceClient(transport))` from `src/services/tshd/createClient.ts`. `cloneClient` (`src/services/tshd/cloneableClient.ts`) makes the client safely transferable across the Electron context bridge.
  - `VnetClient`, `AutoUpdateClient` similar.
  - `PtyServiceClient` is constructed by `src/services/pty/ptyService.ts` (the **shared process** owns the PTY gRPC server because PTY syscalls need to run outside the sandboxed renderer).
  - The renderer's gRPC clients use `@protobuf-ts/grpc-transport` over `@grpc/grpc-js` (TCP, localhost). Interceptors: `services/tshd/interceptors.ts` (`loggingInterceptor`, sensitive-field filtering for logs).
  - `withoutInsecureTshdMethods()` strips `setSharedDirectoryForDesktopSession` from the renderer-side client so the renderer can't choose arbitrary host paths to share over RDP — only the main process can.
- **State management** — Immer-backed `ImmutableStore` (`src/ui/services/immutableStore/immutableStore.ts` wraps `Store` from `shared/libs/stores` with `produce`/`enableMapSet`). All `*Service` classes (`ClustersService`, `WorkspacesService`, `ConnectionTrackerService`, `ModalsService`, `NotificationsService`, `TerminalsService`, `KeyboardShortcutsService`, `StatePersistenceService`, `FileTransferService`, `ResourcesService`, `UsageService`, `ReloginService`, `TshdNotificationsService`, `HeadlessAuthenticationService`, `ConnectMyComputerService`) extend or compose `ImmutableStore` and are aggregated by `AppContext`. State persistence (window layout, open tabs, pinned resources) flows through `StatePersistenceService` to a JSON file managed by `mainProcessClient.fileStorage` (debounced writes, see `web/packages/teleterm/src/services/fileStorage/`).
- **ironrdp WASM** is consumed in the renderer by importing `shared/libs/tdp/client.ts` → which imports `shared/libs/ironrdp/pkg/ironrdp` + `ironrdp_bg.wasm?inline`. The electron-vite config (`web/packages/teleterm/electron.vite.config.mts` lines 138 + 156–170) explicitly marks `**/shared/libs/ironrdp/**/*.wasm` as an asset, then drops the emitted `.wasm` file from the bundle because it's already inlined via `?inline`. Same WASM is shared with the web app — there is one Rust crate at `web/packages/shared/libs/ironrdp/` (Cargo.toml + `src/lib.rs`) built via `pnpm build-wasm` (calls `wasm-bindgen`) and a `mock_ironrdp.js` for Jest.

---

## 13. Teleterm — packaging

- `web/packages/teleterm/package.json` — defines scripts `start` (`electron-vite dev`), `build` (`electron-vite build`), `package` (`electron-builder build --config electron-builder-config.js --publish never -c.extraMetadata.name=teleport-connect`). Uses `electron 41.1.1`, `electron-builder ^26.8.2`, `electron-updater ^6.8.3`, `electron-vite ^5.0.0`.
- `web/packages/teleterm/electron.vite.config.mts` — three Vite sub-configs (`main`, `preload`, `renderer`). Main/preload externalize Node-builtins + every `package.json` dep (`nodeExternalOptions`). Renderer bundles everything and registers `cspPlugin` (CSP from `web/packages/teleterm/csp.ts`) and `drop-wasm-assets`. `manualChunks` keeps `strip-ansi`/`ansi-regex`/`d3-color` as their own chunks.
- `web/packages/teleterm/electron-builder-config.js` — the canonical packaging manifest. Key facts:
  - `appId = 'gravitational.teleport.connect'`. Custom URL scheme `teleport://` declared via `protocols: [{ name: appId, schemes: ['teleport'], role: 'Viewer' }]`.
  - `asar: true`, `asarUnpack: '**\\*.{node,dll}'`.
  - **macOS** (`mac`):
    - Targets `['zip', 'dmg']` (zip is for `electron-updater`).
    - `notarize: true`, `hardenedRuntime: true`, `gatekeeperAssess: false`.
    - Entitlements: `build_resources/entitlements.mac.plist` when signing creds present, else `build_resources/entitlements.mac.adhoc-signed.plist`.
    - Bundles either `tsh.app` (`CONNECT_TSH_APP_PATH`, signed externally — Touch ID variant) or a plain `tsh` binary (`CONNECT_TSH_BIN_PATH`), copied into `Contents/MacOS/tsh.app/Contents/MacOS/tsh`.
    - `afterPack` works around `@electron/universal` adding `ElectronAsarIntegrity` to embedded `tsh.app/Contents/Info.plist`, restoring the original.
    - Apple env mapping: `APPLE_USERNAME → APPLE_ID`, `APPLE_PASSWORD → APPLE_APP_SPECIFIC_PASSWORD`, `TEAMID → APPLE_TEAM_ID` (so `make release-connect` "just works").
  - **Windows** (`win`):
    - Target `nsis`. Signing via PowerShell helper that calls `build.assets/windows/build.ps1 Invoke-SignBinary`; gated on `CI === 'true'`.
    - Bundles `tsh.exe`, `wintun.dll`, optional `msgfile.dll` via env vars `CONNECT_TSH_BIN_PATH`, `CONNECT_WINTUN_DLL_PATH`, `CONNECT_MSGFILE_DLL_PATH`.
    - `nsis` config: deterministic GUID `22539266-67e8-54a3-83b9-dfdca7b33ee1`, `differentialPackage: false`, `oneClick: false`, `selectPerMachineByDefault: true`, custom `installer.nsh` to add VNet messaging.
  - **Linux** (`linux`): targets `tar.gz`, `rpm`, `deb`. Bundles `tsh`, VNet polkit/dbus systemd assets (`examples/systemd/vnet/...`), AppArmor profile, tray icon. RPM/DEB `afterInstall` / `afterRemove` shell templates under `build_resources/linux/`.
  - `extraResources` always includes `build_resources/icon-macTemplate@2x.png` (template tray icon for dark mode on macOS).
  - `publish: [{ provider: 'custom' }]` — updates resolved by the custom `electron-updater` provider in §11 (`clientToolsUpdateProvider.ts`).
  - Output: `build/release/`.

---

## 14. AI-pointer index

| Concept | File / location |
|---|---|
| Web app entry / DOM mount | `web/packages/teleport/src/boot.tsx` |
| Top-level React tree (providers, router) | `web/packages/teleport/src/Teleport.tsx` |
| App context (singleton class) | `web/packages/teleport/src/teleportContext.tsx` |
| Server-injected config | `web/packages/teleport/src/config.ts` (`window.GRV_CONFIG`) |
| Route patterns | `web/packages/teleport/src/config.ts` (`ossRoutes`) |
| Router compat layer (Switch / Route / Redirect) | `web/packages/teleport/src/components/Router/Router.tsx` |
| Feature registry | `web/packages/teleport/src/features.tsx` (`getOSSFeatures`) |
| Feature interface / flags | `web/packages/teleport/src/types.ts` (`TeleportFeature`, `FeatureFlags`) |
| Authenticated gate | `web/packages/teleport/src/components/Authenticated/` |
| Login flow | `web/packages/teleport/src/Login/Login.tsx` + `useLogin.ts` |
| Invite / reset onboarding | `web/packages/teleport/src/Welcome/Welcome.tsx` + `Welcome/NewCredentials/` |
| User preferences context | `web/packages/teleport/src/User/UserContext.tsx` |
| Theme switch (light/dark/bblp/mc) | `web/packages/teleport/src/ThemeProvider.tsx`, `web/packages/design/src/theme/themes/*` |
| App shell (nav + topbar) | `web/packages/teleport/src/Main/Main.tsx`, `web/packages/teleport/src/Navigation/`, `web/packages/teleport/src/TopBar/` |
| Toast notifications | `web/packages/shared/components/ToastNotification/`, `web/packages/teleport/src/Notifications/` |
| HTTP API client | `web/packages/teleport/src/services/api/api.ts` |
| API path helpers | `cfg.api.*` and `cfg.get<X>Url()` in `web/packages/teleport/src/config.ts` |
| Service classes | `web/packages/teleport/src/services/*/` (one folder per resource) |
| React Query global setup | `web/packages/teleport/src/Teleport.tsx` (`new QueryClient(...)`) |
| Query hook helpers | `web/packages/teleport/src/services/queryHelpers.ts` |
| MFA handling | `web/packages/teleport/src/lib/useMfa.ts`, `web/packages/teleport/src/services/mfa/`, `web/packages/teleport/src/components/AuthnDialog/` |
| SSH terminal page | `web/packages/teleport/src/Console/DocumentSsh/DocumentSsh.tsx` |
| TTY / WebSocket protocol | `web/packages/teleport/src/lib/term/tty.ts`, `web/packages/teleport/src/lib/term/protobuf.ts` |
| Authenticated WebSocket | `web/packages/teleport/src/lib/AuthenticatedWebSocket.ts` |
| xterm wrapper + WebGL/Image/Search/Fit addons | `web/packages/teleport/src/lib/term/terminal.ts` |
| Desktop (RDP) session — web | `web/packages/teleport/src/DesktopSession/DesktopSession.tsx`, `web/packages/teleport/src/lib/tdp/` |
| TDP protocol client (shared) | `web/packages/shared/libs/tdp/client.ts` |
| TDP wire codec | `web/packages/shared/libs/tdp/codec.ts` |
| ironrdp WASM crate (Rust) | `web/packages/shared/libs/ironrdp/Cargo.toml`, `src/lib.rs` |
| ironrdp WASM bindings (generated) | `web/packages/shared/libs/ironrdp/pkg/ironrdp*` (gitignored, built by `pnpm build-wasm`) |
| Session-recording player | `web/packages/teleport/src/SessionRecordings/view/ViewSessionRecordingRoute.tsx`, `web/packages/teleport/src/lib/player/`, `web/packages/teleport/src/lib/term/ttyPlayer.js` |
| Audit log infinite-query | `web/packages/teleport/src/Audit/useAuditEvents.ts` |
| Roles editor (Immer reducer) | `web/packages/teleport/src/Roles/RoleEditor/StandardEditor/useStandardModel.ts` |
| Unified resources engine | `web/packages/shared/components/UnifiedResources/UnifiedResources.tsx` |
| Design system entry | `web/packages/design/src/index.ts` |
| Theme tokens | `web/packages/design/src/theme/themes/darkTheme.ts`, `lightTheme.ts`, `bblpTheme.ts` |
| styled-system style functions | `web/packages/design/src/system/index.ts` |
| Icon registry | `web/packages/design/src/Icon/Icons/` (229 icons) |
| DataTable | `web/packages/design/src/DataTable/Table.tsx`, `useTable.ts` |
| Web app Vite config | `web/packages/build/vite/config.ts` (+ `apphash.ts`, `guard-wasm.ts`, `html.ts`, `react.mjs`) |
| WASM static-import guard plugin | `web/packages/build/vite/guard-wasm.ts` |
| Jest config | `web/packages/build/jest/config.js`, `setupTests.ts` |
| Storybook root | `web/.storybook/main.mts`, `web/.storybook/vite.config.mts`, `web/.storybook/preview.tsx` |
| Generated TS protos | `gen/proto/ts/teleport/...` (alias `gen-proto-ts/*`) |
|  |  |
| **Teleterm — Electron main entry** | `web/packages/teleterm/src/main.ts` |
| Teleterm — MainProcess orchestrator | `web/packages/teleterm/src/mainProcess/mainProcess.ts` |
| Teleterm — WindowsManager (BrowserWindow + IPC) | `web/packages/teleterm/src/mainProcess/windowsManager.ts` |
| Teleterm — preload (renderer gRPC clients + contextBridge) | `web/packages/teleterm/src/preload.ts` |
| Teleterm — renderer entry | `web/packages/teleterm/src/ui/boot.tsx`, `App.tsx`, `appContext.ts` |
| Teleterm — TshdClient builder | `web/packages/teleterm/src/services/tshd/createClient.ts` |
| Teleterm — TshdEvents reverse-channel gRPC server | `web/packages/teleterm/src/services/tshdEvents/index.ts` |
| Teleterm — PTY shared-process service | `web/packages/teleterm/src/services/pty/ptyService.ts`, `sharedProcess/sharedProcess.ts` |
| Teleterm — gRPC credentials (mTLS) | `web/packages/teleterm/src/services/grpcCredentials/credentials.ts` |
| Teleterm — Immer store base | `web/packages/teleterm/src/ui/services/immutableStore/immutableStore.ts` |
| Teleterm — Workspaces + persisted state | `web/packages/teleterm/src/ui/services/workspacesService/workspacesService.ts`, `services/statePersistence/` |
| Teleterm — clusters mirror | `web/packages/teleterm/src/ui/services/clusters/clustersService.ts`, `mainProcess/clusterStore.ts` |
| Teleterm — RDP-in-Connect tab | `web/packages/teleterm/src/ui/DocumentDesktopSession/DocumentDesktopSession.tsx` |
| Teleterm — auto-update | `web/packages/teleterm/src/services/appUpdater/appUpdater.ts`, `clientToolsUpdateProvider.ts`, `nsisDualModeUpdater.ts` |
| Teleterm — deep links | `web/packages/teleterm/src/deepLinks.ts`, `mainProcess/navigationHandler.ts`, `mainProcess/protocolHandler.ts` |
| Teleterm — tray | `web/packages/teleterm/src/tray.ts` |
| Teleterm — Vite (3-process) build | `web/packages/teleterm/electron.vite.config.mts` |
| Teleterm — packaging | `web/packages/teleterm/electron-builder-config.js` |
| Teleterm — Connect My Computer (renderer) | `web/packages/teleterm/src/ui/ConnectMyComputer/` |
| Teleterm — Connect My Computer (main, agent runner) | `web/packages/teleterm/src/mainProcess/agentRunner/`, `agentDownloader/`, `agentCleanupDaemon/` |
|  |  |
| **tshd (Go) entry** | `lib/teleterm/teleterm.go` (`Serve`) |
| tshd — daemon | `lib/teleterm/daemon/daemon.go` |
| tshd — gRPC API server | `lib/teleterm/apiserver/apiserver.go` + `apiserver/handler/handler_*.go` |
| tshd — cluster facades | `lib/teleterm/clusters/cluster_*.go` |
| tshd — gateways | `lib/teleterm/gateway/`, `lib/teleterm/daemon/gateway.go` |
| tshd — VNet | `lib/teleterm/vnet/service*.go` |
| tshd — Connect My Computer | `lib/teleterm/services/connectmycomputer/` |
| tshd — desktop | `lib/teleterm/services/desktop/` |
| tshd — unified resources | `lib/teleterm/services/unifiedresources/` |
| tshd — user preferences | `lib/teleterm/services/userpreferences/` |
| tshd — auto-update RPC | `lib/teleterm/autoupdate/` |
| tshd — TshdEvents client (reverse) | `lib/teleterm/daemon/events_client.go`, `daemon/daemon_headless.go`, `daemon/mfaprompt.go`, `daemon/hardwarekeyprompt.go` |
| tshd — proto sources | `proto/teleport/lib/teleterm/v1/`, `proto/teleport/lib/teleterm/vnet/v1/`, `proto/teleport/lib/teleterm/auto_update/v1/` |
| tshd — generated Go protos | `gen/proto/go/teleport/lib/teleterm/...` |
