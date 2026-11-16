# Plugin routes

> **Language:** the canonical English technical reference lives under the
> path-stable extension surface: [Plugin routes (canonical)](../../extensions/routes.md).
> This page exists so `docs/zh-CN` and `docs/en-US` stay structurally parallel.
>
> 中文版：[插件路由（中文）](../../zh-CN/extensions/routes.md)

Author-facing guide for **plugin-declared HTTP routes**: Manifest V3 `routes[]`
semantics, core guards, the Protocol V2 `InvokeRoute` handler (and route
streams), the ingress probe + Nuxt proxy path, CSRF/PAT behavior, limits, and
testing — anchored on the `sforum-custom-content` fixture.

Contents (canonical page):

- How a plugin route works (registry admission → API ingress plan → plugin RPC → Nuxt probe/proxy)
- Minimal example (manifest `routes[]` + Go `InvokeRoute` server)
- Manifest declaration reference (field-by-field, route actions, schemas)
- Guards (`core.guard.public`/`login`/`permission`/`guest`; `inherit` resolves when the target is resolvable; custom guards are live exact-artifact, trust-gated plugin code; `core.guard.raw_request` was **removed** on 2026-10-16 — raw request authority now requires a package-declared `kind: raw_request` guard; the legacy `/api/v1/extensions/:id/*` prefix serves only `public`/`login`/`permission` and answers 404 for `guest`, `inherit`, and package-declared guards)
- Protocol V2 handler (`RouteRequest`/`RouteResponse` fields, streaming modes)
- Limits and timing (8 MiB buffered response, 3 s default RPC timeout, schema validation)
- Calling a plugin route from the browser
- Testing and checklist
