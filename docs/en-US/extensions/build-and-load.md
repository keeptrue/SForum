# Build, digest, and load your extension

> **Language:** the canonical English technical reference lives under the
> path-stable extension surface: [Build, digest, and load (canonical)](../../extensions/build-and-load.md).
> This page exists so `docs/zh-CN` and `docs/en-US` stay structurally parallel.
>
> 中文版：[构建与加载（中文）](../../zh-CN/extensions/build-and-load.md)

The complete author loop for plugins with an executable backend (and optional
frontend assets): scaffolding, backend Go module wiring (`replace` directive to
the host module), building the binary and prebuilt frontend artifacts,
refreshing exact digests, contract validation, packaging, and loading the
package into a running instance (external source roots, upload, or built-in
staging) with the trust/iteration cycle.

Contents (canonical page):

- Package shape after a build
- Backend Go module (`go.mod` + `replace github.com/keeptrue/sforum/apps/api => <checkout>/apps/api`)
- Building frontend assets (final self-contained ESM/CSS; the Host never compiles uploaded source)
- `extension digest --write` / `validate` / `test` / `package`
- Loading into a running instance (four paths + admin trust/enable)
- Iteration loop and out-of-band recovery
- Built-in packages (`build-builtin-plugins.sh` builds only hard-coded ids)
- Troubleshooting table
