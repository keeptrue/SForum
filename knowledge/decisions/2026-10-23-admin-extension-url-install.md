# Decision: Admin Extension URL Install Is a Separate, Inert Entry

## Status

Accepted (2026-10-23; revised 2026-10-26).

## Context

Operators could only install an extension by using the hidden file input on the
extension overview page. That path is easy to miss, gives no explanation of the
archive contract, and cannot install a package hosted at a URL. The request was
for a dedicated **Install extension** page under the extension folder that
supports both drag/drop ZIP upload and a URL pull.

Two constraints shaped the design:

- Static installation in SForum is deliberately inert. It validates, previews,
  and stores an exact package; it never enables, activates, starts a runtime,
  imports frontend code, or grants trust. The new page must not change that.
- Server-side URL fetching is an SSRF boundary. It must not let an authenticated
  actor without install authority use the Host as a network probe, and it must
  not reach private, loopback, link-local, or cloud-metadata addresses.

## Decision

1. Add `/extensions/install` as a first-class admin page and sidebar entry,
   visible with `extension.plugin.manage` or `extension.theme.manage`. The
   existing overview upload button stays for compatibility.
2. Add `POST /admin/extensions/install-from-url` with a JSON body
   `{url, sha256?}`. It downloads through the shared
   `outboundhttp` SSRF-safe client and then calls the existing
   `InstallOrUpgradeArchive` path, so both sources share manifest validation,
   staged-upgrade semantics, audit actions, and inert-install behavior.
3. The downloader is HTTPS-only, rejects userinfo, private/loopback/link-local/
   metadata targets, follows at most three redirects, uses a 90-second timeout,
   requires HTTP 200, checks the ZIP magic, and enforces the same 50 MB ceiling
   as uploads. Authorization is checked **before** the outbound request.
4. An optional SHA-256 pins the downloaded ZIP bytes. It is checked before the
   package is stored. It is intentionally distinct from the extracted package
   tree digest returned as `packageDigest`.
5. Record provenance in the existing install/upgrade audit as `sourceUrl`
   (userinfo, query, and fragment stripped) and `sourceDigest` (ZIP bytes).
6. No new permission key, database migration, environment variable, proxy
   support, private-address allowlist, async job, or auto-enable behavior.

## Consequences

- **Accepted risk: synchronous download.** A slow 50 MB package occupies one
  admin request and up to 50 MB of memory until the download finishes. There is
  no concurrency limiter in this iteration; the route is permission-gated and
  admin-only. Reverse proxies must allow a read timeout above 90 seconds.
- **Accepted risk: no preview/confirmation.** The page installs directly and
  shows the result card. This is acceptable because installation is inert; a
  package still needs an explicit enable/activation and, for executable code, an
  exact-artifact `super_admin` trust confirmation.
- **Accepted risk: no proxy support.** HTTPS-only direct egress means an
  operator behind a mandatory proxy cannot use URL install; local upload remains
  available. This is a deliberate scope choice, not an oversight.
- **Provenance is audit-only.** `sourceUrl`/`sourceDigest` are not displayed in
  the extension list and are not used by lifecycle decisions.
- **Two upload entry points.** The overview quick upload and the new page use
  separate UI code paths for now. Consolidating them is deferred until the
  install UI needs shared progress or async status.
- **Revised 2026-10-26 — auto-enable exists only as a separate host action.**
  This route still stages and nothing else. "Install and enable" is the
  page-level `extension.install.enable` action: the Host confirms, installs
  through this same route, then enables through its own lifecycle route, and an
  executable backend still needs the exact-artifact `super_admin` confirmation.
  There is no auto-enable flag, no config switch, and no change for manual URL
  installs — the default remains an inert install. See
  `2026-10-24-admin-page-host-action.md` (2026-10-26 revision).

## References

- `apps/api/app/Support/ExtensionDownload/downloader.go`
- `apps/api/app/Http/Controllers/Extensions/remote_install.go`
- `apps/web/app/pages/admin/extensions/install.vue`
- `apps/web/app/components/admin/extensions/SFAdminExtensionInstallPanel.vue`
- `contracts/openapi/paths/extensions.yaml#/adminExtensionInstallFromURL`
