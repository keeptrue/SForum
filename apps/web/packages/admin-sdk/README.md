# @sforum/admin-sdk

Typed bridge contracts for trusted SForum admin plugin page bodies.

```ts
import type { AdminPageBridgeV1 } from '@sforum/admin-sdk'
import { ADMIN_MICRO_FRONTEND_API_VERSION } from '@sforum/admin-sdk'
```

Bundle this package into the plugin's final ESM output. The Host supplies the
bridge at mount time and continues to own routing, permissions, navigation,
appearance, and failure isolation.

## Host icons

`AdminPageBridgeV1.icons` resolves Host icon names to sanitized inline SVG:

```ts
const icons = await bridge.icons?.resolve(['i-lucide-inbox'])
element.innerHTML = icons?.['i-lucide-inbox'] ?? ''
```

The member is optional: a Host that predates `1.1.0` omits it, and a plugin must
degrade instead of failing. Names must match
`i-(lucide|tabler)-<kebab-name>`; the Host never returns an icon for a name it
does not know. `@sforum/plugin-ui` wraps all of this in `SPluginIcon`, so most
plugins never call the resolver directly.

## Host version (1.3.0)

Both bridges may carry the Host build identity. It is optional: a Host that
predates `1.3.0` omits it, so feature-detect and treat a missing value as
*unknown* — never guess a version and never hardcode one.

```ts
const core = bridge.host?.coreVersion      // strict semver, compatibility authority
const build = bridge.host?.buildVersion    // dev-abc12 etc., display only
```

Compare `coreVersion` with a semver constraint; `buildVersion` must never be
used for compatibility decisions. `valid` is `false` when the Host build label
itself is not strict semver, in which case the Host skips its own manifest gate.

## Host-mediated install (1.2.0)

A page whose manifest declares `hostActions: ["extension.install"]` gets one
extra bridge member. Feature-detect it; never assume it exists:

```ts
const installed = await bridge.installExtension?.({
  url: 'https://cdn.example.com/sforum.store-1.4.0.zip',
  sha256: '9f2c…',                 // 64 lowercase hex
  expectedExtensionId: 'sforum.store',
  expectedType: 'plugin',
  expectedVersion: '1.4.0'         // optional
})
if (!installed) return // Host predates the action, or the page did not declare it
```

What the Host does, and what it does not:

- Pre-checks `extension.plugin.manage` / `extension.theme.manage`, then shows its
  own confirmation dialog (URL host, full URL, SHA-256, declared ID/version/type,
  `super_admin` note). Cancel sends no request.
- Sends one `install-from-url` POST with a 120 s budget and the declared identity.
  If the downloaded manifest disagrees, the Host aborts with
  `extension.expected_mismatch` instead of staging a different package.
- Resolves when the package is **staged**. It never enables, trusts, activates, or
  upgrades anything — the administrator finishes that in the core extension pages.
- Rejects with `install_permission_denied`, `install_cancelled`,
  `install_in_progress`, `install_invalid_request`, or `install_failed`. Match on
  the error's `code`, not on its text.
- Navigating away aborts the install and rejects with `install_cancelled`; the
  operator retries. One dialog/install per page at a time.

See `docs/extensions/trusted-admin-components.md` in the SForum repository.
