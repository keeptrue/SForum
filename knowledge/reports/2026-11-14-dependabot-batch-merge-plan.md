# Dependabot 批量合并计划（PR 185–208）

- 日期：2026-11-14
- 范围：`keeptrue/SForum` 上 20 个开放 PR（19 个 Dependabot + 1 个 ImgBot）
- 目标：合成一条 `deps/batch-2026-11-14` 分支，只烧一轮 CI，供 v3.1.0 发版
- 本文只做调研与计划，未改动任何代码文件

## 0. 结论先行

| 分级 | PR | 处理 |
|---|---|---|
| green（直接合） | 187, 188, 191, 196, 199 | 进批量分支 |
| yellow（合了要补验证） | 185, 186, 192, 193, 197, 198, 200, 201, 202, 205, 206, 208 | 进批量分支，按 §6 补验证 |
| red（本轮不发） | 194, 195, 207 | 拆出，各自单独 PR + 单独 CI |

三条硬结论，先摆在这里：

1. **PR 208 一条就关掉两个 govulncheck 红点。** `fasthttp v1.75.0` 把 `andybalholm/brotli` 换成了 `molecule-man/go-brrr`，brotli 直接从依赖图里消失（GO-2026-6888 路径没了）；同批 `klauspost/compress` 到 `v1.20.3`（GO-2026-6865 修复版本）。PR 208 自己的 `Govulncheck / core-api` 已经是 SUCCESS。**工作区里那两行未提交的 brotli/compress 手工修复被 208 取代，不是叠加关系。**
2. **PR 194（typescript 7）必然红。** TS 7.0.2 不再发布 `lib/typescript.js`，`exports` 只暴露 `./lib/version.cjs`；`vue-tsc@3.3.6` 走 `require('typescript/lib/tsc')`，CI 上实测 `ERR_PACKAGE_PATH_NOT_EXPORTED`。`vue-tsc` 最新版仍是 3.3.14，没有 TS7 兼容分支。
3. **PR 207 的 postgres 18 不能顺手合。** PG18 官方镜像把 `PGDATA` 改成 `/var/lib/postgresql/18/docker`、`VOLUME` 改成 `/var/lib/postgresql`，而 `compose.yaml` 仍挂 `postgres_data:/var/lib/postgresql/data`——命名卷会变成无效挂载。

## 1. 前置状态（必须先解决，否则批量分支不可能全绿）

### 1.1 工作区带着未提交的 go.mod/go.sum（快照，会变）

调研开始时（19:34）的 `git status --porcelain`：

```
 M apps/api/go.mod
 M apps/api/go.sum
 M extensions/builtin/plugins/sforum-auth-github/backend/go.sum
 M extensions/builtin/plugins/sforum-content-policy/backend/go.mod
 M extensions/builtin/plugins/sforum-content-policy/backend/go.sum
 M extensions/builtin/plugins/sforum-search-site/backend/go.sum
 M extensions/builtin/plugins/sforum-shortcodes/backend/go.sum
 M extensions/builtin/plugins/sforum-smtp/backend/go.sum
 M extensions/builtin/plugins/sforum-storage-fs/backend/go.sum
 M extensions/builtin/plugins/sforum-storage-s3/backend/go.sum
 M extensions/builtin/plugins/sforum-web-push/backend/go.sum
```

内容是 brotli `v1.2.2→v1.2.7`、compress `v1.19.0→v1.20.3`。**它和 PR 208 直接冲突**（见 §5.3 的 `git apply` 实测）。合并前必须先决定这笔改动的去向：要么先落到 main 再让 208 覆盖，要么 stash 后由 208 一次性收口。**这一步由 lead 决定，本报告不动它。**

> **工作区是活的，本节数字会过期。** 19:56 复查时，除上述 11 个文件外还多了 `tests/compat/go.{mod,sum}`、`tools/proto/go.{mod,sum}`（tidy 波及），以及 5 个 `apps/api/app/Support/Extensions/*.go`（内核租约修复在写）。合并前一律以当时的 `git status --porcelain` 为准，别照抄本文的清单。

### 1.2 main 的 Go gate 现在是红的

`TestPostgresExtensionDatabaseKernelCleanupWorksForNonSuperCreateRoleHost` 在 68f1cc97 上持续失败。批量分支的「全绿」目标依赖这条先修好，否则 deps 分支的 Go gate 会继承同一个红。

**进展**：19:56 复查时工作区已出现对应的 5 个文件改动（`extension_database_kernel_non_super_integration_test.go`、`extension_database_disposition_postgres.go`、`extension_database_kernel_postgres.go`、`extension_database_migration_role.go`、`extension_database_runtime_lease_postgres.go`），说明修复正在并行推进。**批量分支必须等它落进 main 之后再开**，否则这一轮 CI 的 Go gate 结果无法归因。

### 1.3 所有 20 个 PR 的 CI 结果都不能直接采信

PR 创建时间跨 10-29 至 11-12，多数分支基于旧 main。典型证据（PR 206 的 Contracts job 130623386232）：

```
Error: new or moved UI surface needs a reviewed stable identity mapping:
apps/web/app/components/admin/forum/trust-levels/SFAdminTrustLevelColorPicker.vue
    at scripts/v3-catalog/generate.mjs:803
```

这条错误在当前 main 上已不存在（68f1cc97 的 Contracts job 是绿的）。**结论：合之前全部 rebase 到当前 main，PR 侧的历史红不作判据。**

## 2. 逐条清单

版本号取自 `gh pr diff <n>`；文件路径取自 `gh pr list --json files`。

**编号说明**：开放 PR 是 185–188、191–202、205–208，共 20 条。189、190、203、204 不存在（不是漏查）。

### 2.1 前端依赖组（11 条，全部改 `apps/web/package.json` + `apps/web/bun.lock`）

当前钉死值（`apps/web/package.json` 实测）：

```
dependencies:    @nuxtjs/i18n ^10.4.0 · @tiptap/core ^3.31.3 · extension-character-count ^3.31.3
                 extension-code-block 3.31.3 · extension-file-handler 3.31.3 · extension-image ^3.31.3
                 extension-link ^3.31.3 · extension-placeholder ^3.31.3 · extension-table 3.31.3
                 extension-text-style 3.31.3 · extension-underline ^3.31.3 · markdown ^3.31.3
                 pm 3.31.3 · starter-kit ^3.31.3 · suggestion ^3.31.3 · vue-3 ^3.31.3
                 altcha ^3.1.0 · highlight.js ^11.11.1 · nuxt ^4.4.8
devDependencies: @vitejs/plugin-vue 6.0.7 · esbuild ^0.28.1 · typescript ^6.0.3
                 vue-router ^5.1.0 · vue-tsc ^3.3.6
```

| PR | 依赖 | 变更 | 档位 | 分级 |
|---|---|---|---|---|
| 191 | `@vitejs/plugin-vue` | 6.0.7 → 6.0.10 | patch | green |
| 192 | `@tiptap/starter-kit` | ^3.31.3 → ^3.32.0 | minor | yellow |
| 193 | `@tiptap/suggestion` | ^3.31.3 → ^3.32.0（lock 里现为 3.31.4） | patch | yellow |
| 194 | `typescript` | ^6.0.3 → ^7.0.2 | **major** | **red** |
| 195 | `@nuxtjs/i18n` | ^10.4.0 → ^10.6.0 | minor | **red** |
| 196 | `highlight.js` | ^11.11.1 → ^11.12.0 | minor | green |
| 197 | `@tiptap/extension-code-block` | 3.31.3 → 3.32.0 | patch | yellow |
| 198 | `@tiptap/extension-file-handler` | 3.31.3 → 3.32.0 | patch | yellow |
| 199 | `esbuild` | ^0.28.1 → ^0.28.2（deps + devDeps 各一行） | patch | green |
| 200 | `@tiptap/extension-table` | 3.31.3 → 3.32.0 | patch | yellow |
| 201 | `altcha` | ^3.1.0 → ^3.5.0 | minor | yellow |

五个 tiptap PR（192/193/197/198/200）单看都是 patch/minor，**但单合并任何一个都会把 tiptap 家族撕成两代**：每个 extension 的 `peerDependencies` 是精确钉死的 `@tiptap/core: 3.32.0`，而 `@tiptap/core` 仍是 3.31.3。PR 197 的 lock diff 已经把这个后果写出来了——bun 只能再塞一份嵌套副本：

```
+ "@tiptap/starter-kit/@tiptap/extension-code-block": ["@tiptap/extension-code-block@3.31.3", ...]
+ "@tiptap/extension-code-block": ["@tiptap/extension-code-block@3.32.0", ...]
```

同一扩展两份实例 = 两套 ProseMirror 插件 = 编辑器行为不确定。**正确做法不是逐个 merge，而是把整个家族一次性钉到 3.32.0。** 我按这个方案在隔离目录做了完整解析验证（§5.1），结果干净。

### 2.2 后端 Go 依赖组（4 条）

| PR | 模块 | 变更 | 档位 | 分级 |
|---|---|---|---|---|
| 187 | `google.golang.org/protobuf` | 1.36.11 → 1.36.12 | patch | green |
| 188 | `golang.org/x/oauth2` | 0.36.0 → 0.38.0 | minor | green |
| 202 | `aws-sdk-go-v2` 家族（5 个直接 + 12 个间接） | 见下 | minor（v1.x） | yellow |
| 208 | core-go 组 18 个 | 见下 | patch/minor + river 0.x 破坏性 | yellow |

**PR 202** 直接依赖：`aws-sdk-go-v2 1.41.5→1.47.8`、`config 1.31.16→1.33.16`、`credentials 1.18.20→1.20.16`、`service/s3 1.97.3→1.114.9`、`smithy-go 1.24.2→1.28.8`。间接层新增 `service/signin`，`internal/ini` 消失。全部 `go 1.24` 指令，不抬工具链。

**PR 208** 直接依赖（16 条 require 变更 + 2 条 protobuf/grpc 重复项）：

```
altcha-lib-go/v2   v2.0.0-20260512100103 → v2.4.1
gofiber/fiber/v3   3.4.0 → 3.6.1
gofiber/storage/redis/v3  3.5.0 → 3.6.3
jackc/pgx/v5       5.10.0 → 5.12.0
mileusna/useragent 1.3.4 → 1.3.5
pressly/goose/v3   3.27.2 → 3.29.0
redis/go-redis/v9  9.21.0 → 9.23.0
riverqueue/river   v0.40.0 → v0.51.1  (+ riverdriver/riverpgxv5, rivertype)
santhosh-tekuri/jsonschema/v6  6.0.2 → 6.0.3
shirou/gopsutil/v4 4.26.5 → 4.26.10
valyala/fasthttp   1.72.0 → 1.75.0
yuin/goldmark      1.8.2 → 1.8.7
golang.org/x/image 0.45.0 → 0.47.0
golang.org/x/sys   0.49.0 → 0.50.0
google.golang.org/grpc     1.83.2 → 1.84.0
google.golang.org/protobuf 1.36.11 → 1.36.12
```

`riverqueue/river` 是 v0.x，按 semver `0.40 → 0.51` 属于破坏性区间，不能当 minor 看。**已实测编译通过**（§5.2）。

### 2.3 容器与工作流（3 条）

| PR | 内容 | 变更 | 档位 | 分级 |
|---|---|---|---|---|
| 185 | `apps/web/Dockerfile`（deps 与 prod 两个 stage） | `oven/bun:1.3.14-alpine` → `1.4.4-alpine` | major | yellow |
| 186 | `apps/api/Dockerfile` | `golang:1.26.10-alpine` → `1.27.0-alpine`；`alpine:3.22` → `3.24`（api/worker/migrate 三个 target） | major | yellow |
| 205 | `.github/workflows/release.yml`、`security.yml` | `docker/login-action` v4.6.0→v4.8.0；`actions/attest-build-provenance` v3→v4.2.2；`github/codeql-action` SHA 刷新（仍是 v4） | minor×3 + major×1 | yellow |

PR 205 的四个 SHA 都做了实名核对：

```
docker/login-action@8aa5831fb8d4  → 与 tag v4.8.0 指向同一 commit  ✅
actions/attest-build-provenance@4d101475d8b2 → 与 tag v4.2.2 同一 commit ✅
github/codeql-action@3ea37a44b6e4 → 真实 commit（2026-11-11），比当前 v4 tag 落后 68 个提交
```

`attest-build-provenance` v3→v4 是 major，但 v4.0.0 release note 明确写了「v4 只是 `actions/attest` 的 wrapper，现有用法可继续」。`release.yml` 用的是 `subject-name` / `subject-digest` / `subject-path` / `push-to-registry`，这些输入在 v4 保留。

### 2.4 其余两条

| PR | 内容 | 分级 |
|---|---|---|
| 207 | `compose.dev.yaml` / `compose.yaml` / `compose.zero-downtime.yaml` 五个镜像：mailpit v1.30.7→v1.33.1、postgres 17.11-alpine→**18.4-alpine**、redis 7.4.10-alpine→**8.10.2-alpine**、meilisearch v1.53.1→v1.56.1、caddy 2.10.2-alpine→2.11-alpine | **red** |
| 206 | ImgBot，39 个文件（1 个站点 logo SVG + 8 个插件 brand icon + 8 个 brand social-preview + docs 资源 + 11 张 knowledge 报告截图） | yellow |

PR 207 的 red 只针对 postgres 那一行；redis 7→8 是同批里可接受的 major（`redis-server --appendonly yes --requirepass` 参数未变，go-redis v9 客户端兼容）。

## 3. 两处已知冲突：上游实证

### 3.1 `bun-version: 1.3.14` vs `oven/bun:1.4.4-alpine`

**仓库侧事实**：`ci.yml` 有两处钉死（第 80、112 行），PR 185 只动 Dockerfile：

```
$ grep -n "bun-version" .github/workflows/ci.yml
80:          bun-version: 1.3.14
112:          bun-version: 1.3.14
```

于是形成三方错位：**CI 1.3.14 / Docker 1.4.4 / 本机 1.4.2**。本机 bun 已经是 1.4.x，这一项其实早就不同步了。

**上游事实**：

- `bun-v1.4.4` 发布于 2026-10-22（`gh api repos/oven-sh/bun/releases`）。
- 官方镜像 Dockerfile 在 `bun-v1.4.4` 是 `FROM alpine:3.24`，`bun-v1.3.14` 是 `FROM alpine:3.22`。**PR 185 实际还隐含了一次 Alpine 大版本跳变**，不只是 bun。
- `apps/web/Dockerfile` 的 prod stage 钉了 `libcrypto3>=3.5.7-r0` / `libssl3>=3.5.7-r0`（CVE-2026-45447）。实测两个 Alpine 仓库该包版本相同：

```
alpine v3.22 main/x86_64 libcrypto3 → V:3.5.9-r0
alpine v3.24 main/x86_64 libcrypto3 → V:3.5.9-r0
```

所以 `>=3.5.7-r0` 在 3.24 上仍可满足，**不会构建失败**。

**锁文件兼容性（本机实测，这是关键）**：`apps/web/bun.lock` 是 `"lockfileVersion": 1`。用本机 bun 1.4.2 在隔离目录跑：

```
$ bun install --frozen-lockfile --lockfile-only
bun install v1.4.2
IDENTICAL: bun 1.4.2 keeps lockfileVersion 1 unchanged
```

即 **bun 1.4.x 不会改写现有 lock**。所以 PR 185 不强制重生成锁文件。

**结论（yellow）**：PR 185 必须与 `ci.yml` 的两处 `bun-version` 一起抬到 `1.4.4`，同一笔提交。只抬 Dockerfile 会让门禁和产物跑在两个 bun 上，`bun test` / `bun run build` 的行为差异永远无法在 CI 里被验证。

### 3.2 `typescript 6.0.3 → 7.0.2`（PR 194）

**上游事实一：TS 7.0.2 已经不是「带编译 API 的 npm 包」。**

```
$ curl -s https://registry.npmjs.org/typescript/7.0.2
"bin": {"tsc": "bin/tsc"}
"type": "module"
"exports": {".": "./lib/version.cjs", "./unstable/*": ...}

$ tar tzf typescript-7.0.2.tgz | grep -E '^package/lib/[^/]+\.(js|cjs|d\.ts)$'
package/lib/getExePath.d.ts
package/lib/getExePath.js
package/lib/tsc.js
package/lib/version.cjs

$ tar xzOf typescript-7.0.2.tgz package/lib/version.cjs
const { version } = require("../package.json");
exports.version = version;
exports.versionMajorMinor = "7.0";

$ tar tzf typescript-7.0.2.tgz | grep -c 'lib\.es.*\.d\.ts'
0
```

没有 `lib/typescript.js`，没有 `tsserver`，没有标准库 `lib.es*.d.ts`。它是 Go 原生编译器的 npm 壳，JS 侧只剩 `version` 与 `./unstable/*` 新 API。

**上游事实二：`vue-tsc` 走的正是被删掉的那条路。**

- `vue-tsc@3.3.6`：`bin.vue-tsc → bin/vue-tsc.js`，`require('../index.js').run()`；`peerDependencies: { typescript: ">=5.0.0" }`——只声明了下界，对 7 没有任何约束力。
- `@vue/language-core@3.3.6` 内部有 5 处 `ts.createSourceFile`，即依赖完整编译器 API。
- `vue-tsc` 最新版是 **3.3.14**（`dist-tags.latest`），不存在 4.x 兼容分支。

**CI 实证**（PR 194 的 Web gate，job 123259569147，2026-10-29）：

```
+ typescript@7.0.2
+ vue-tsc@3.3.6
$ NUXT_BUILD_DIR=.nuxt-typecheck nuxt prepare && vue-tsc --noEmit ...
Error [ERR_PACKAGE_PATH_NOT_EXPORTED]: Package subpath './lib/tsc' is not defined
by "exports" in .../node_modules/typescript/package.json
    at Object.run (.../vue-tsc/index.js:40:32)
error: script "typecheck" exited with code 1
```

这是启动即崩，和「旧 main 上的 trustLevels 测试」无关，**rebases 也救不回来**。而 `scripts/ci/web-gate.sh` 第一件事就是 `bun run typecheck`。

**结论（red）**：本轮不发。等 `vue-tsc` 提供 TS7 入口（或改走 `typescript/unstable` API）再单独评估。

### 3.3 附带查实的第三处：`@nuxtjs/i18n 10.6.0`（PR 195）

lead 只点了两处，但这条同样过不了关。`@nuxtjs/i18n@10.6.0` 没有 `peerDependencies`，依赖里有三个会撕裂运行时的东西：

```
unhead        ^3.2.3     （nuxt 4.4.8 依赖的是 unhead ^2.1.15）
vue-router    ^5.2.0     （仓库钉 ^5.1.0，lock 实装 5.1.0）
@nuxt/kit     ^4.5.1     （nuxt 4.4.8 精确依赖 @nuxt/kit 4.4.8）
```

我在隔离目录按 PR 195 的实际依赖集做了完整解析（bun 1.4.2，`--lockfile-only`），结果：

```
unhead             → 3.4.3（提升到根）
@unhead/vue/unhead → 2.1.15（嵌套）
nuxt/unhead        → 2.1.15（嵌套）
vue-router         → 5.1.0（根）
@nuxtjs/i18n/vue-router → 5.5.1（嵌套）
@nuxt/kit          → 4.5.1（提升到根）
nuxt/@nuxt/kit     → 4.4.8（嵌套）
```

**同一个进程里两套 unhead（v3 + v2）、两套 vue-router（5.1.0 + 5.5.1）、两套 @nuxt/kit。** `@unhead/vue` 2.1.15 只会用到嵌套的 unhead v2 实例，而 i18n 用 v3 —— `useLocaleHead` 产出的 hreflang/SEO 条目能否被 Nuxt 的 head 管理器收下，取决于两代实例的条目去重语义。这类问题 CI 抓不到，只有浏览器渲染能看出来。

**结论（red）**：本轮不发。理由不是「版本号大」，而是「一条 PR 引入三个重复运行时，且失败模式在 CI 之外」。发版后单独开 PR，配浏览器 hreflang / 语言切换 / SEO 元数据验证。

## 4. 冲突矩阵与合并顺序

### 4.1 文件撞车矩阵

| 文件组 | PR | 条数 |
|---|---|---|
| `apps/web/package.json` + `apps/web/bun.lock` | 191,192,193,194,195,196,197,198,199,200,201 | 11（本轮实际取 9） |
| `apps/api/go.mod` + `go.sum` | 208 | 1 |
| `extensions/.../backend/go.{mod,sum}` | 187,188,202 | 3 |
| `.github/workflows/ci.yml` | 185（需追加，非 PR 自带） | 1 |
| `.github/workflows/{release,security}.yml` | 205 | 1 |
| `apps/*/Dockerfile` | 185,186 | 2（互不相交） |
| `compose*.yaml` | 207 | 1 |
| 静态资源 | 206 | 1 |

**跨组隐藏耦合（本报告最重要的一条）**：8 个内置插件模块 + `tests/compat` 都通过相对 replace 依赖 api 模块：

```
$ grep -l "keeptrue/sforum/apps/api =>" extensions/builtin/plugins/*/backend/go.mod tests/compat/go.mod
extensions/builtin/plugins/sforum-auth-github/backend/go.mod
... （8 个插件全中）
tests/compat/go.mod
```

所以 **PR 208 一改 `apps/api/go.mod`，10 个模块的 go.sum 全部失效**。这不是理论推断，PR 208 的 `Container / api` 就是死在这上面（job 130968161778）：

```
#17 0.751 go: updates to go.mod needed; to update it:
#17 0.751 	go mod tidy
#17 ERROR: process "/bin/sh -c cd /app/extensions/builtin/plugins/sforum-smtp/backend && ... go build ..." 
          did not complete successfully: exit code: 1
```

失败点在**第一个插件（smtp）**，不在 apps/api。这就是「合并 go.mod 之后必须对每个 extension 模块补 tidy」的真实机制。

### 4.2 建议顺序（一笔一个关注点，最后一次性跑 CI）

```
C0  chore(ci): bun 1.4.4 对齐           ← 185 + ci.yml 两处；先做，后面所有 web 步骤才在同一 bun 上
C1  chore(deps): Go 依赖批量             ← 208 + 202 + 187 + 188，然后 10 个模块统一 tidy
C2  chore(deps): web 依赖批量            ← 191 196 199 201 + tiptap 家族 3.32.0（192 193 197 198 200）
C3  chore(container): api 构建/运行镜像   ← 186
C4  chore(ci): actions 版本              ← 205
C5  chore(compose): 镜像批量（不含 PG18） ← 207 的 4 条
C6  chore(assets): ImgBot 图片           ← 206
```

为什么是这个顺序：

1. **C0 在最前**，因为 C2 要重新生成 `bun.lock`。谁生成锁文件，就必须用 CI 将来校验它的那个 bun 版本。反过来做（先改依赖再抬 bun）会让锁文件的合法性依赖「1.4.2 恰好不改写 lockfileVersion 1」这个实测结论，虽然成立，但没必要留这个隐式前提。
2. **C1 独立成一笔**，因为它是唯一会失败在「Go gate + Container/api」上的改动，且失败原因（10 模块 tidy）与 web 无关。混进别的提交后，一旦 Go gate 红，无法判断是依赖问题还是 tidy 漏了。
3. **C2 里 5 个 tiptap PR 必须合成为一次家族升级**，不能逐个 merge（见 §2.1 的嵌套副本证据）。逐个 merge 会得到一棵有三代 tiptap 的树，而且每个中间态都是坏的。
4. **C3/C4/C5/C6 互不相交**，任何一笔单独 revert 都不会牵连依赖树。
5. **C6 放最后**：39 个二进制/资源文件，CI 无法验证，单独一笔便于「出问题就整笔 revert」而不影响依赖结论。

### 4.3 哪些必须单独一轮 CI

| 对象 | 为什么 |
|---|---|
| PR 194（typescript 7） | 已确认会打红 web-gate 的 typecheck，需等 vue-tsc 支持后单独验证 |
| PR 195（i18n 10.6） | 重复运行时问题只有浏览器能验，需要独立 PR + 渲染证据 |
| PR 207 的 postgres 18 部分 | 需要改 volume 路径 + 迁移验证 + CI service 版本决策，与发版解耦 |
| PR 206（ImgBot） | 非依赖、CI 无法判定。可以留在同批（C6 独立 commit），也可以拉独立 PR；两条路都成立，取决于 lead 想不想让图片 diff 出现在依赖 PR 里 |

**批量分支实际覆盖 17 条 PR**：185,186,187,188,191,192,193,196,197,198,199,200,201,202,205,206，加 207 的四个非数据库镜像。194、195 整体推迟，207 的 postgres 一行推迟。三者各自独立 CI。

## 5. 已完成的隔离验证（不是推测）

所有验证都在 `/tmp` 的仓库副本里做，未触碰工作区。

### 5.1 前端合并集能否解析

把 191/196/199/201 + tiptap 全家族 3.32.0 写进副本 `package.json`，`bun install --lockfile-only --ignore-scripts`：

```
Saved bun.lock (1366 packages) [31.00ms]
tiptap 家族解析结果：3.32.0（统一），残留 3.0.6 / 3.27.1 为既有历史副本
```

**结论：家族统一升级是干净解**，不会出现 3.31.x 与 3.32.x 并存的中间态。

同一轮还暴露一个必须写进提交说明的行为：`altcha` 的 range 是 `^3.5.0`，重新解析装的是 **3.6.0**，不是 PR 标题里的 3.5.0。要么接受 3.6.0，要么在 package.json 里钉精确版本。

### 5.2 PR 208 能否编译

把 PR 208 的 `go.mod`/`go.sum` 应用到 HEAD 版本的 `apps/api` 副本：

```
$ git apply pr208.diff          # 基于 HEAD 的 go.mod/go.sum
APPLIED-ON-PRISTINE

$ go build ./...                 # -mod=mod
BUILD_EXIT=0

$ go build ./...                 # 只读模式，等价 CI 行为
READONLY_EXIT=0

$ go mod tidy && diff go.mod.208 go.mod
（无输出：PR 自带的 go.mod/go.sum 已是 tidy-clean 状态）
```

**结论：`river 0.40→0.51` 在本仓库用到的 API 面上没有编译期破坏。** 运行期语义（队列重试、迁移器）仍需 `./scripts/test-api-isolated.sh --with-compat` 兜。

### 5.3 与工作区未提交改动的冲突（实测）

```
$ git apply pr208.diff          # 基于当前（含 brotli 1.2.7 的）工作区
error: 打补丁失败：apps/api/go.mod:4
error: apps/api/go.mod：补丁未应用
error: 打补丁失败：apps/api/go.sum:1
```

同一份补丁，干净的 HEAD 上能打，脏的工作区打不上。**§1.1 那笔未提交改动必须先有归宿。**

## 6. 两道 generated-drift 闸门怎么过

CI 里两道闸门的位置（`.github/workflows/ci.yml`）：

```
65:  run: git diff --exit-code -- apps/api/sqlc apps/api/database extensions fixtures     # Go gate 之后
90:  run: git diff --exit-code -- apps/web                                               # Web gate 之后
125: run: git diff --exit-code -- apps/api/sdk/plugin/v2/gen contracts                   # 本批不涉及
```

### 6.1 `apps/web` 闸门

触发条件：`package.json` 与 `bun.lock` 不一致，或构建/安装过程在 `apps/web` 下写出了被跟踪的文件。

应对：

1. **锁文件必须由本仓库、用 CI 的 bun 版本重新生成**，不要直接采纳 Dependabot 写的那份。原因：Dependabot 生成的条目 tarball URL 是 `""`（默认 registry），而本仓库既有条目全部指向 `registry.npmmirror.com`（本机 `~/.bunfig.toml` 设了 `registry = "https://registry.npmmirror.com"`）。PR 191/194/195 的 lock diff 里能直接看到 `+ "@vitejs/plugin-vue": ["@vitejs/plugin-vue@6.0.10", "", ...]` 这种空 URL 条目。混着提交会让后续每次 install 的 registry 归属都不确定。
2. **不要手改 lock**，只 `bun install` 后整体提交。
3. 生成物都是忽略项，不会误报——`.gitignore` 已覆盖：

```
55:.nuxt/
56:.nuxt-build/
57:.nuxt-typecheck/
65:apps/web/.nuxt/
66:apps/web/.nuxt-build/
67:apps/web/.nuxt-typecheck/
```

注意 `bun install` 会跑 `postinstall: nuxt prepare`（写 `.nuxt/`），`bun run typecheck` 写 `.nuxt-typecheck/`，`bun run build` 写 `.nuxt-build/`——三者都已忽略。
4. 本地预演闸门：`cd apps/web && bun install --frozen-lockfile && cd ../.. && git diff --exit-code -- apps/web`（要求工作区干净，否则 `git diff` 会带上无关改动）。

### 6.2 `apps/api/sqlc apps/api/database extensions fixtures` 闸门

触发条件：有步骤**写出了**这些路径下的文件。

本批的风险点与对策：

1. **`go mod tidy` 扫 10 个模块只会动 `go.mod`/`go.sum`**，而 `go.mod`/`go.sum` 不在这道闸门的路径里——所以 tidy 本身不会触发 drift。但**必须把这 10 个模块的改动全部提交**，否则 Go gate 的 `go build` 会先红。
2. **本批禁止执行 `sforum extension digest --write` 和 `scripts/build-builtin-plugins.sh`**。它们会重写 `extensions/**/sforum.extension.json` 里的 `packageFiles[].digest`。当前不需要跑：插件二进制被忽略（`.gitignore:114 extensions/builtin/plugins/*/backend/plugin`），而 `packageFiles` 只登记二进制与 schema，不含 `brand/`——我扫了全部内置扩展清单，`packageFiles` 里没有任何 brand/svg 条目：

```
$ python3 -c '...遍历 extensions/builtin/*/sforum.extension.json...'
（无输出：没有任何 brand 资源被登记为 packageFile）
```

   这条同时是 PR 206 的定心丸：ImgBot 改 `brand/icon.svg` 不会引起清单 digest 漂移。
3. **若有人跑了 `scripts/build-builtin-plugins.sh`**，`git status` 会出现 `sforum.extension.json` 改动，必须连同提交；否则 CI 的 drift 闸门直接红。
4. 提交前自查：`git status --porcelain` 的文件清单应当**恰好**等于「go.mod/go.sum ×10 + 各 PR 自带的业务文件」，多一个 `sforum.extension.json` 都算事故。

## 7. 合并脚本骨架（未执行，供 lead 复核后使用）

```bash
#!/usr/bin/env bash
# 合成 deps 分支。不要直接跑；每一段都先看 diff 再进下一段。
set -euo pipefail
export https_proxy=http://127.0.0.1:7897 http_proxy=http://127.0.0.1:7897 \
       all_proxy=socks5://127.0.0.1:7897

# ── 前置 ───────────────────────────────────────────────────────────────
git status --porcelain            # 必须为空（见 §1.1，先处理那 11 个文件）
git fetch origin --prune
git switch -c deps/batch-2026-11-14 origin/main

# ── C0: bun 对齐（PR 185 + ci.yml 两处）─────────────────────────────────
git merge --no-ff 4fa37f959f55 -m "chore(deps): bump oven/bun to 1.4.4-alpine (PR #185)"
sed -i 's/bun-version: 1\.3\.14/bun-version: 1.4.4/' .github/workflows/ci.yml
grep -n "bun-version" .github/workflows/ci.yml          # 期望两处都是 1.4.4
git commit -am "ci: align bun-version with web image (1.4.4)"

# ── C1: Go 依赖（208 先，202/187/188 后）───────────────────────────────
git merge --no-ff 563c3131c40e -m "chore(deps): bump core-go group (PR #208)"
( cd apps/api && go mod tidy && go build ./... )
for m in extensions/builtin/plugins/*/backend tests/compat; do
  ( cd "$m" && go mod tidy && go build ./... )
done
git add -A apps/api extensions tests/compat
git commit -m "chore(deps): tidy all modules that replace the api module"

for sha in c386148618ae e02c09fad9f6 800b9cbfce31; do   # 202, 187, 188
  git merge --no-ff "$sha" -m "chore(deps): bump extension Go modules ($sha)"
done
for m in extensions/builtin/plugins/*/backend tests/compat; do ( cd "$m" && go mod tidy ); done
git commit -am "chore(deps): tidy after extension module bumps" || true

# ── C2: web 依赖（tiptap 家族一次到位，不做 5 次 merge）────────────────
# 手工改 apps/web/package.json：全部 @tiptap/* 钉 3.32.0；
#   另 altcha ^3.5.0 / highlight.js ^11.12.0 / esbuild ^0.28.2（deps+devDeps 两处）
#   / @vitejs/plugin-vue 6.0.10。typescript 与 @nuxtjs/i18n 不动。
( cd apps/web && bun install )                    # 生成新 bun.lock，URL 归属本仓库 registry
git add apps/web/package.json apps/web/bun.lock
git commit -m "chore(deps): bump tiptap family to 3.32.0 and web deps (PR #191-#201)"

# ── C3–C6 ─────────────────────────────────────────────────────────────
git merge --no-ff bcd015fd9618 -m "chore(container): golang 1.27.0 + alpine 3.24 (PR #186)"
git merge --no-ff 6a7e43a3ec3e -m "chore(ci): bump github-actions group (PR #205)"

git merge --no-ff 038925725898 -m "chore(compose): bump images (PR #207, postgres kept at 17.11)"
# 手工把 compose*.yaml 里的 postgres:18.4-alpine 改回 postgres:17.11-alpine，
# 其余四个镜像保留；改完再 git commit --amend
git diff HEAD~1 -- compose.yaml compose.dev.yaml compose.zero-downtime.yaml

git merge --no-ff 1bd7eade5bbd -m "chore(assets): ImgBot image optimization (PR #206)"

# ── 本地门禁（与 CI 同口径）────────────────────────────────────────────
./scripts/test-api-isolated.sh --with-compat
( cd apps/api && go build ./... )
( cd apps/web && bun install --frozen-lockfile && bun run typecheck && bun test && bun run build )
node tests/validate-architecture-boundaries.mjs
git diff --exit-code -- apps/api/sqlc apps/api/database extensions fixtures
git diff --exit-code -- apps/web
git status --porcelain                            # 必须为空

# ── 推 PR（由 lead 决定时机）──────────────────────────────────────────
git push -u origin deps/batch-2026-11-14
gh pr create --base main --title "chore(deps): batch dependabot 2026-11-14" \
  --body-file knowledge/reports/2026-11-14-dependabot-batch-merge-plan.md
```

脚本里两处「手工」不是偷懒，是有意的：`@tiptap/*` 的家族升级和 compose 里 postgres 的回退都需要人看一眼 diff，自动 merge 会把这层判断藏起来。

## 8. 验收清单

推送前本地必须全绿：

- [ ] `apps/api`：`go build ./...`、`govulncheck ./...`（期望模块级 0 条，brotli 已离开依赖图）
- [ ] `./scripts/test-api-isolated.sh --with-compat`
- [ ] 依赖 api 模块的 10 个模块（`apps/api` + 8 个内置插件 + `tests/compat`）`go mod tidy` 无残留改动；`tools/proto` 不依赖 api 模块，按需即可
- [ ] `apps/web`：`bun install --frozen-lockfile`、`bun run typecheck`、`bun test`、`bun run build`
- [ ] `node tests/validate-architecture-boundaries.mjs`
- [ ] 两道 `git diff --exit-code` 闸门本地预演通过
- [ ] `git status --porcelain` 为空

CI 上必须全绿（同一个 commit）：Go gate、Web gate、Contracts、Container/api、Container/web、Govulncheck、CodeQL、Dependency review。Release 工作流在打 tag 后单独验，它是 PR 205 的真实考场。

## 9. 未验证 / 待确认

1. **river 0.40→0.51 的运行期语义**（队列重试策略、`rivermigrate` 迁移行为）只做了编译验证，没跑隔离测试集群。`--with-compat` 是必须的兜底。
2. **`go-brrr` 自身有没有 CVE**：govulncheck 本地库无法离线判定。合入后第一次 `Govulncheck / core-api` 是唯一权威答案。
3. **PR 207 的 postgres 18 迁移成本**：PG17 的 `postgres_data` 卷无法被 PG18 直接读取，需要 dump/restore 或 `down -v` 重建。这一步没有在本地演练过。
4. **PR 206 的 8 个 brand social-preview PNG**：只核对了 SVG 的语义等价（同一 `id="sforum-blue"`、同一路径、同一 viewBox、`title`/`aria-labelledby` 保留），PNG 无法用 diff 判断，需要人工看一眼。
5. **PR 185 之后 `bun test` 的行为差异**：本机 1.4.2 与 Docker 1.4.4 不是同一版本，只有在 CI 抬到 1.4.4 后才真正验证。
