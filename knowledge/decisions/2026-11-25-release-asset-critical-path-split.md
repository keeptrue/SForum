# 2026-11-25 Release Asset Critical-Path Split (M6)

**Status**: accepted
**Scope**: `.github/workflows/release.yml`,
`scripts/ci/release_workflow_test.rb`

## Decisions

1. **darwin CLI assets do not wait for Linux image merging.** The
   `release-assets-darwin` matrix builds only `darwin/amd64` and
   `darwin/arm64`, with exact dependencies `[prepare, verify-ci]`. It receives
   no GHCR package permission because it only cross-compiles the CLI.
2. **Linux backend bundles still consume scanned merged images.** The
   `release-assets-linux` matrix keeps exact dependencies
   `[prepare, merge-candidate]` and read-only GHCR access. It must not extract
   binaries from platform candidates before merge and scan have succeeded.
3. **Artifact identity stays stable.** Both jobs upload
   `release-asset-<goos>-<goarch>`. `verify-release-assets` depends on
   `[prepare, release-assets-darwin, release-assets-linux, deploy-asset]` and
   downloads them with the unchanged `release-asset-*` pattern.
4. **Verification semantics stay unchanged.** Checksum finalization,
   attestation, and the `verified-release-assets` upload remain one downstream
   step after all four platform assets and the deploy asset are present.
5. **Existing release safety rules remain ratcheted.** The workflow test
   continues to enforce the six native platform builds, no QEMU, Trivy before
   digest upload, cache-writer policy, merged-candidate smoke, SDK publication
   dependencies, three-image promotion, and the reviewed Node publishing
   toolchain. It additionally rejects a returning legacy combined
   `release-assets` job.

## Guardrails

- Do not make darwin depend on `merge-candidate` or `publish-candidate-platform`.
- Do not make Linux extract from unmerged platform candidates.
- Do not rename release artifacts or replace the `release-asset-*` download
  pattern without updating `finalize-release-assets.sh` and the workflow test.
- Do not move checksum or attestation work into either platform-producing job.
- A real tag-driven Release run remains required to measure the remote critical
  path and validate GHCR extraction, attestations, and downstream promotion.
