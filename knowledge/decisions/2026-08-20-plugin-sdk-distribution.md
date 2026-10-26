# 2026-08-20 Plugin SDK Distribution And Author Build

## Status

Accepted and operational. Repository implementation, the npm namespace
bootstrap, and both Trusted Publisher settings are complete.

## Context

Plugin UI v1 provided a real Vue/Vite authoring workspace, but its declared npm
dependencies were not yet available from a public registry. The documented
build loop also asked beginner authors to run install, build, digest, validate,
and contract-test commands separately.

## Decision

- Release `@sforum/admin-sdk` and `@sforum/plugin-ui` as independently
  versioned public npm packages. Their semantic-version major must equal the
  Admin Micro-frontend bridge API version; scaffold dependency versions must
  match the repository packages.
- Treat `npm pack` tarballs as the release artifacts. CI verifies public
  metadata, license inclusion, required source files, SHA-512 integrity, and a
  network-free Vite consumer build from extracted tarballs.
- Publish from the tag Release workflow with GitHub-hosted OIDC, npm Trusted
  Publishing, and provenance. Do not store a long-lived npm write token.
- Make release retries idempotent: first compare registry and local tarball
  integrity. When archive bytes differ, compare a canonical digest over each
  package file's path, mode, size, and bytes so runtime-dependent gzip/tar
  encoding cannot create a false conflict. Fail on different canonical content
  under the same version. Image promotion waits for SDK publication.
- Add `sforum extension build [package-root]` as an author-only command. When
  `frontend/admin/package.json` exists it runs Bun install/build, then directly
  reuses digest refresh, full package/template validation, and contract tests.
  `--skip-install` and `--allow-scaffold` cover installed dependencies and a
  temporarily missing backend binary.
- Never invoke this build command from upload, installation, enable, recovery,
  or production runtime paths. Production continues to load only exact-digest
  `.mjs` and `.css` artifacts.

## Consequences

- Plugin authors get one supported command after editing Vue source, while each
  underlying safety gate remains independently available and testable.
- The first registry publication could not use package-level Trusted Publishing
  because the packages did not exist yet. On 2026-08-20 the `sforum` owner
  published both reviewed `1.0.0` tarballs with interactive 2FA, then bound both
  package settings to `keeptrue/SForum`, workflow `release.yml`, action
  `npm publish`, with no GitHub environment. Both packages require 2FA for
  interactive publication and disallow bypass-2FA tokens; OIDC Trusted
  Publishing remains compatible with that strict setting.
- The interactive bootstrap versions do not carry npm provenance. Provenance
  verification begins with the next SDK version that is actually published by
  the tag-driven OIDC Release job; an application tag that reuses exact
  `1.0.0` artifacts correctly exercises the idempotent skip path instead.
- Node 24 and earlier runtimes can produce different `npm pack` archive bytes
  for the same file set. Registry tarball SHA-512 remains the fast-path
  identity, while canonical file content is the fallback identity for release
  retries.
- Package versions change only when SDK content changes; an SForum application
  release may legitimately find and reuse the exact existing SDK versions.
