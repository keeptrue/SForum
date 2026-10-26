# 2026-10-26 Profile Tab l2Widget Handoff

## Changed

- Backend: `forum.profile.tabs` (`profileSection`) accepts `type: l2Widget`
  with a required `componentId` and optional `permission`. Validation
  (`app/Support/ExtensionManifest/contribution_validate.go`) requires the
  referenced component to be declared in the same manifest, `action: add`, and
  free of component-level `Permission`; `method`/`path`/`href` must be empty and
  the link branches must not carry `componentId`. Payload type moved to
  `profile_tab_payload.go`; `PayloadProfileTabL2Widget = "l2Widget"`.
- Backend read path: `ProfileTabProvider.ProfileTabs(ctx, viewer)` filters
  `permission` through `Actor.Can()` and caps widget tabs at 8 per render
  (overflow truncated in contribution order + `slog.Warn`).
  `ProfileExtensionTab` gained `componentId`, `kind: widget` (empty `url`), and
  `Permission json:"-"`. `Service.GetPublicProfileForViewer` is new;
  `GetPublicProfile` keeps its anonymous signature for PageViewModels/AITools.
  `Controller.publicProfile` resolves an optional actor and degrades to
  anonymous on failure instead of erroring.
- Frontend: `SFProfileShowPage.vue` appends widget tabs after `about` (order →
  `extensionId` → `id`, independent-field code-unit comparison) and mounts
  `SFExtensionWidget` in the panel (`v-if`, so switching away unmounts it).
  `SFProfileRightRail.vue` filters widget tabs out of the extension links;
  label resolution and the sort live in `profileExtensionTab.ts`, and
  `sforum-profile.css` lets the tab bar scroll horizontally on narrow screens.
- CSP: the shell (single fetch point) feeds `collectProfileWidgetRefs()` to
  `SFPageOutlet`; `SFPageOutletResolver` and `SFThemeTemplate` each aggregate
  once on mutually exclusive paths (`profile-page-policy.test.ts` pins the call
  counts). Omitting refs would not block the mount - only theme-path style
  pre-injection and the digest header are lost.
- Data fetch: `useProfileShowData()` is the only profile `useAsyncData` (shell
  creates + provides, island injects), default options keep payload purge.
  Different options log `Incompatible options detected` in dev; identical
  options still re-run the handler server-side (`payloadExtraction: false` → no
  `static.data`), so no `getCachedData`/`dedupe`.
- Tests: `profile_tab_widget_test.go` (payload matrix + full validation),
  `profile_contributions_test.go` (viewer filter, widget shape, 8-cap),
  `apps/web/tests/extensions/profile-tab-widget.test.ts` (render/mount/unmount).
- No `components[].contractVersion` bump; existing `extensionRoute`/`hostLink`
  payloads validate and render exactly as before.

## Decisions

- Only `forum.profile.tabs` is reopened, and only for `l2Widget`: a tab selects
  a panel the host already renders, so it is a container. Actions, sidebar,
  badges, and nav stay closed (M4 still governs them).
- Visibility gating lives on `payload.permission`, never on the component: a
  public L2 component must not carry `Permission` (same rule as regions and
  `AdmitPublicComponent`). The permission is a visibility filter, **not** a
  security boundary - plugin routes must authorize their own data.
- 8 widget tabs per profile render, truncated in contribution order; link tabs
  are uncapped. Built-in tab positions are fixed.
- Record: `decisions/2026-10-26-profile-tab-l2widget.md` (includes the boundary
  checklist for future points and the rollback conditions). M4 in
  `plans/2026-10-16-plugin-frontend-experience.md` now carries a
  partial-reopening note instead of a deleted conclusion.

## Next

- Landed after the original handoff: OpenAPI + authoring guide, generated
  catalogs, fixture `extensions/fixtures/plugins/sforum-profile-tab-demo`, and
  the CSP aggregation above.
- Remaining before merge: run `cd apps/api && go build ./... && go test
  ./app/Support/ExtensionManifest/... ./app/Models/Profile/... ./app/Providers/...
  ./app/Http/Controllers/Profile/...`, `cd apps/web && bun run typecheck && bun test`,
  `node tests/validate-architecture-boundaries.mjs`, then
  `./scripts/test.sh --fast`.
- Browser pass with the fixture package: the widget tab mounts, the
  `page-policy` request includes the component ref, and an anonymous visitor
  receives no restricted tab.

## Open Questions

- The 8-tab budget is global (first-come by contribution order), so one plugin
  can crowd out another. Accepted for now; change only against a real operator
  report.
- Restricted link tabs (`extensionRoute`/`hostLink` with `permission`) are
  filtered too. That is cosmetic today; no decision has been made about whether
  link tabs should ever be permission-gated.
