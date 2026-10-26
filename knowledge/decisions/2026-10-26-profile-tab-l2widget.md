# Decision: `l2Widget` On `forum.profile.tabs` (Profile Tab Panels)

## Status

Accepted (2026-10-26). **Partially supersedes** the M4 conclusion in
`../plans/2026-10-16-plugin-frontend-experience.md` and the matching bullet in
`../decisions/2026-10-16-plugin-frontend-surface-contract.md`, for exactly one
contribution point: `forum.profile.tabs` (`profileSection`).

## Context

M4 of the plugin-frontend-experience task book was cancelled after inspection:
descriptor contribution points looked like content surfaces but were all
link/action/badge shaped, so accepting `l2Widget` there would have produced
declarations that validate, are documented for authors, and render nothing.
That reasoning is still correct for the points it examined.

A product requirement reopened one of them: a plugin needs to render content
*inside* a member profile instead of linking away to `/extensions/{id}` or
owning a page region. Re-reading the render path shows `forum.profile.tabs` is
not shaped like the other points:

- The host tab bar in `apps/web/app/components/profile/SFProfileShowPage.vue`
  owns a real panel area in the profile content column. Selecting a tab swaps
  the panel that is rendered there. The tab is a **container with a host-owned
  render site**, not a link or an action.
- `forum.topic.actions`, `forum.topic.sidebar`,
  `forum.topic.list.badges`, `forum.topic.badges`, `forum.nav.items`, and
  `forum.composer.toolbar` remain `type` + `method`/`path`/`href` or badge-tone
  descriptors rendered as `<a>` / `<NuxtLink>` / buttons. They have no panel to
  mount into, so M4's dead-slot failure mode still applies to every one of
  them.
- The mount primitive already carries the failure handling M4 was worried
  about: `SFExtensionWidget` provides a fallback slot (the profile page passes
  the SSR skeleton), failure isolation, bounded retry, and its own retry button,
  so a broken widget degrades inside the panel instead of rendering nothing.

## Decision

1. **Scope is one point and one payload type.** Among descriptor contribution
   points, only `forum.profile.tabs` accepts `type: "l2Widget"`. Page regions
   (`forum.page.regions`) already accept it and are unchanged. This record is
   not a precedent for `forum.topic.actions`, `forum.topic.sidebar`,
   `forum.topic.list.badges`, `forum.topic.badges`, `forum.nav.items`,
   `forum.composer.toolbar`, admin surface kinds, or any future point.
2. **The widget must be a declared public L2 component.** `componentId` is
   required, must match a `components[]` entry in the same manifest with
   `action: add` and a non-empty `l2Component`, and that component must carry
   **no component-level `Permission`**. `method`/`path`/`href` must be empty for
   `l2Widget`; the `extensionRoute` and `hostLink` branches must not carry
   `componentId`. This is the same admission rule as region placement
   (`validateRegionPlacementContributionPayload`,
   `ComponentRegistry.AdmitPublicComponent`): a public L2 component is public by
   definition, and the mount site is what gets gated.
3. **Visibility gating lives on `payload.permission`, never on the component.**
   The key is optional, must be declared by the manifest, is filtered server-side
   with `Actor.Can(permission)`, and is serialized as `json:"-"` on
   `ProfileExtensionTab` so it never reaches the browser. The public profile
   endpoint resolves an optional actor and degrades to anonymous when the session
   cannot be resolved; `GetPublicProfile` keeps its anonymous signature for
   `PageViewModels` and `AITools`, and only the HTTP entry uses
   `GetPublicProfileForViewer`. **The permission is a visibility filter, not a
   security boundary:** a viewer can still call the plugin's own routes, so every
   data access must be authorized by the plugin route/command itself. Hiding a
   tab is not an ACL on data.
4. **Budget: at most 8 widget tabs per profile render.** Truncation happens in
   effective-contribution order (`order`, then extension id, then contribution
   id) and logs a warning; that is the same comparator the web tab bar uses for
   display, so the surviving set is the first eight in display order. Link tabs
   are not capped because they cost no L2 mount or CSP aggregation. Built-in
   tabs (`topics`/`replies`/`about`) keep fixed positions and widget tabs append
   after `about`. Known cost: truncation is a global first-come budget, not a
   per-extension quota, so a plugin that contributes many tabs can push another
   plugin's tabs out. That is accepted for now; revisit only with a real
   operator report, not speculatively.
5. **One entry per contribution, one place.** Widget tabs render in the main
   tab bar only. `SFProfileRightRail` filters them out of its extension links so
   the same contribution does not appear twice; `extensionRoute` and `hostLink`
   tabs keep their previous right-rail behavior.
6. **Page policy is part of the feature, for a narrower reason than it looks.**
   A widget tab is an L2 mount, so the profile page's document CSP aggregates
   its component refs through `GET /extensions/runtime/page-policy`:
   `SFPageOutletResolver` on the native path, `SFThemeTemplate` on the
   theme-template path, mutually exclusive and each aggregating exactly once.
   **Omitting the refs does not block the mount.** The app then writes no
   `Content-Security-Policy` for that response, and L2 loads over same-origin
   `import()` (`script-src 'self'`), same-origin `<link rel=stylesheet>`
   (`style-src 'self'`), and same-origin descriptor/asset fetch (`connect-src
   'self'`) - all allowed either way. The real costs are (a) on the
   theme-template path, no digest-bound style pre-injection, so the widget's CSS
   arrives after first paint (visible flash), and (b) no document policy and
   therefore no `X-SForum-Document-Policy-Digest` evidence for that response.
   Diagnosing a broken widget tab as "CSP blocked it" is wrong; check the
   widget's own descriptor/trust/lease path first.
7. **Ref collection is per-ref fail-closed.** `collectProfileWidgetRefs` drops
   invalid refs one at a time, deliberately unlike `collectRegionWidgetRefs`
   (all-or-nothing): one plugin's bad contribution must not void the whole
   page's L2 policy, and the dropped tab fails closed on its own.
8. **No `contractVersion` bump.** This extends a contribution payload with
   optional fields and adds a serialized `kind`; it does not change any declared
   component's props or schema shape.

## Boundary checklist (what "no" looks like next time)

Before accepting `l2Widget` on another point, all four must be true:

1. The point already has a host-owned panel/container render site, and the host
   owns selection plus unmount. If a new render site must be invented, the answer
   is no.
2. The point cannot express the same outcome as a link/action or as a page
   region. If it can, use that instead.
3. There is a binding test tying the declaration to that render site (the
   region↔`<SFRegionOutlet>` pairing check is the model), so a validated
   declaration cannot silently stop rendering.
4. A separate decision record is written for that point. **This record must not
   be cited as precedent for anything except `forum.profile.tabs`.**

## Consequences

- A profile tab can now hold extension-owned UI without the plugin owning a
  route; the profile page gains one extension-controlled panel area.
- The 8-tab budget is fixed, not site-configurable. Operators that need more
  content should use page regions, the general-purpose content surface, which
  already aggregate CSP per page.
- Profile reads become viewer-aware at the HTTP layer only. Any future host read
  surface that calls `GetPublicProfileForViewer` must state why it needs a
  viewer; the default stays anonymous.
- A tab permission that guards real data must be re-checked by the plugin's own
  route; this decision does not make tab visibility an authorization mechanism.

## Rollback conditions

Remove `l2Widget` from `forum.profile.tabs` (keep `extensionRoute`/`hostLink`
working throughout) if any of the following holds:

1. A published package can declare an `l2Widget` tab that never renders on a
   real profile page — i.e. the declaration-to-render-site binding regresses.
2. The 8-widget budget cannot be enforced (a package splits declarations to
   exceed it, or the cap is bypassed), allowing unbounded L2 mounts and CSP
   aggregation on one page.
3. Permission gating leaks: a restricted tab's identity or `componentId`
   reaches a viewer without the permission, or the feature is used as if tab
   visibility authorized data access.
4. Profile widget refs cannot be aggregated into the page document policy on
   both the native outlet and theme-template paths, so profile pages that mount
   widgets lose the strict document policy, its digest evidence, and style
   pre-injection.
5. Viewer resolution destabilizes the public profile (errors instead of
   degrading to anonymous) or makes the page's tab set depend on session state
   in a way that breaks SSR/hydration consistency.
