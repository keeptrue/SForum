# GitHub 账号改名 zhuchunshu → keeptrue：模块路径、镜像命名空间与运行时常量同步迁移

**Date:** 2026-10-26
**Status:** accepted
**Area:** Repository identity / Go module path / release artifacts

## Context

维护者的 GitHub 账号由 `zhuchunshu` 改名为 `keeptrue`。仓库本身的 git 历史与
GitHub 仓库 ID 不变（GitHub 对旧 URL 提供 301），但**字符串形式**的身份出现在
四类会静默失效的位置：

1. **Go module path**：`github.com/zhuchunshu/sforum/apps/api` 写在 27 个
   `go.mod` 的 `module` / `require` / `replace` 行里，并被 1500+ 个 Go 文件
   import。module path 与仓库实际路径不一致时，`go get` / `go install` 会失败，
   扩展作者的 `replace` 会指向不存在的模块。
2. **容器镜像命名空间**：`ghcr.io/zhuchunshu/sforum-{api,migrate,web}` 出现在
   `deploy.sh`、`upgrade.sh`、`sforum-bootstrap.sh`、compose 文件、release
   workflow 与部署文档中。账号改名后旧命名空间不再对外提供匿名拉取（本记录
   实测，见「验证证据」），继续引用会直接导致升级脚本 401/404。
3. **运行时常量**：`SystemUpdates.RepositoryOwner` 决定管理端「检查更新」请求
   `api.github.com/repos/<owner>/<repo>/releases`；`version.SourceURL` 决定
   `--version` 与健康检查展示的源码地址。
4. **发布与文档入口**：README 引导脚本下载地址、`gh attestation verify --repo`、
   npm 包的 `repository` / `homepage` / `bugs`、品牌资产 `slug`。

这些都是"改错不报错"的位置：Go 编译期只有 module path 不一致才会报错，镜像与
URL 的错误只在发布/升级时暴露，运行时只影响一次 HTTP 请求的 URL。因此必须在一次
改动中统一处理，并留下可复现的验证证据。

## Decision

1. **Go module path 统一改为 `github.com/keeptrue/sforum/...`**：根模块
   `apps/api`、`tools/proto`、`tests/compat`、8 个内置插件 backend、16 个
   fixture 插件 backend，共 27 个 `go.mod` 的 `module`、`require`、
   `replace` 三处同时改；仓库内不保留旧路径的兼容别名（Go 没有 module alias
   机制，保留只会制造第二套真相）。
2. **容器镜像命名空间改为 `ghcr.io/keeptrue/sforum-*`**。改名后 GHCR 把包迁到
   新命名空间，历史 tag 在新命名空间下可匿名拉取（已实测 3.0.13 三镜像 200），
   因此改写后的部署文档与脚本仍然指向真实存在的产物。
3. **运行时常量改为 `RepositoryOwner = "keeptrue"`**，
   `version.SourceURL = "https://github.com/keeptrue/SForum"`；`--ldflags`
   注入路径同步改为 `github.com/keeptrue/sforum/apps/api/version.Current`
   （Dockerfile ×3、`scripts/ci/build-release-assets.sh`）。
4. **安全联系邮箱 `zhuchunshu999@gmail.com` 不迁移。** 邮箱是独立于 GitHub
   账号的联系地址：改名不改变邮箱所有权，历史安全报告线程都落在该地址上，
   换邮箱需要同时改中英双语 SECURITY.md、GitHub Security Advisory 的联系方式
   和维护者收件箱，属于独立决策。改名只影响"代码在哪里"，不影响"漏洞报给谁"。
5. **历史归档不改写。** `docs/archive/**` 与 `knowledge/sessions/**` 记录的是
   当时的事实（例如 2026-07 的真实 import 路径、当时发布的旧命名空间 tag），
   改写会让历史证据失真，使后续排查无法还原当时的构建条件。约定：归档只读，
   改名只作用于当前有效的代码与文档。
   `knowledge/index.md` 中已被删除的历史分支名（`index.md:1697`）同理保留。
6. **当前有效的文档（`knowledge/index.md`、`knowledge/decisions/*`、docs、
   README、AGENTS）按新命名空间改写**——它们描述"现在怎么用"，不是历史快照。
   改写后仍然事实正确：GHCR 已把 3.0.13 三镜像迁到新命名空间并实测可拉取。

## Constraint: 不要对生成代码裸 sed

`apps/api/sdk/plugin/v2/gen/**/*.pb.go` 的 `rawDesc` 是**序列化后的
FileDescriptorProto 字节串**，`go_package` 以「tag + 长度前缀 + 内容」的形式
内嵌其中。账号名从 `zhuchunshu`(10) 变成 `keeptrue`(8)，内容短 2 字节，因此
**两处长度前缀必须同步减 2**：

```text
旧: ...Response B O Z M github.com/zhuchunshu/...
新: ...Response B M Z K github.com/keeptrue/...
              ↑ ↑   ↑ ↑
              | |   | └─ 0x4B = 75  go_package 字符串长度（原 0x4D = 77）
              | |   └─── 0x5A = field 11 (go_package)
              | └─────── 0x4D = 77  FileOptions 子消息长度（原 0x4F = 79）
              └───────── 0x42 = field 8 (options)
```

只替换字符串、不重算前缀，`protoimpl.TypeBuilder` 在包 `init()` 解析 rawDesc
时会越界（表现为 `slice bounds out of range [-4:]` 之类的 panic），而且**编译期
完全正常**，只有运行时第一次 import 才炸。

约束：**后续任何人不要对 `*.pb.go` 做裸 sed / 批量字符串替换。** 改 `.proto`
的 `option go_package` 后，一律执行：

```bash
bash scripts/proto.sh generate     # 用 tools/proto 里钉住的 buf + protoc-gen-go 重生成
bash scripts/proto.sh check        # 重生成并断言与已提交内容一致（要求干净工作树）
```

本次即按此路径重生成，重生成结果与施工方提交的工作树内容**逐字节一致**
（见验证证据 V6）。

## Impact

- 1651 个文件，+4198 / −4198，**每一行改动都只包含旧或新账号名**，没有夹带
  逻辑变更（验证证据 V2）。
- 27 个 `go.mod`；1534 个 `.go` 文件（含 import 与 `-ldflags` 注入路径）。
- 8 个 `.proto` 的 `option go_package` + 11 个生成文件（8 个 `*.pb.go` 的
  rawDesc、3 个 `*_grpc.pb.go` 的 import）。
- 部署面：`deploy.sh`、`upgrade.sh`、`sforum-bootstrap.sh`、`sforum-latest.sh`、
  4 个 compose 文件、`deploy/scripts/*`、`scripts/ci/*`、`.env.production.example`。
- 前端面：`apps/web/packages/{admin-sdk,plugin-ui}/package.json` 的
  `repository` / `homepage` / `bugs`，以及 3 个引用仓库地址的测试断言。
- **对外破坏性**：仍固定在旧命名空间 `ghcr.io/<旧账号>/sforum-*` 的既有部署，升级时会
  拉不到镜像（旧命名空间匿名 token 已被 GHCR 拒绝，实测 DENIED）。升级说明必须
  提示把镜像前缀改成 `ghcr.io/keeptrue/`；这不是回滚能恢复的，是账号改名的既成
  事实。

## Verification evidence

全部在 `main` 工作树、未提交状态下实测（2026-10-26 21:48–22:05 +08:00）。

| # | 检查 | 命令 | 结果 |
|---|---|---|---|
| V1 | 残留扫描 | `rg -n 'zhuchunshu' --hidden -g '!.git' -g '!node_modules' -g '!**/.nuxt*' .` | 55 处，全部落在白名单：SECURITY.md 邮箱 ×2、`docs/archive/**` ×33、`knowledge/sessions/**` ×19、`knowledge/index.md:1697` 历史分支名 ×1。大小写不敏感复扫同样干净。本决策记录落盘后总数为 69 处，多出的 14 处即本文件自身（见「残留扫描白名单口径」） |
| V2 | 纯字符串性 | `git diff -U0 \| grep '^[+-]' \| grep -vE '^(---\|\+\+\+)' \| grep -v zhuchunshu \| grep -v keeptrue` | **0 行**（8396 行改动全部含新旧账号名之一）；反向核对：新增行含旧名 0 行、删除行含新名 0 行 |
| V3 | 构建 | `cd apps/api && go build ./...` / `go vet ./...` | exit 0 / exit 0（vet 覆盖全部测试文件的类型检查） |
| V4 | 构建 | `cd tests/compat && go build ./...`；`bash scripts/build-builtin-plugins.sh` | exit 0；exit 0（8 built / 1 skipped，staged 到 gitignored 的 `storage/builtin-dev`） |
| V5 | 单测 | `go test ./app/Models/SystemUpdates/... ./version/... ./cmd/sforum/...` | 全 ok（cmd/sforum 12.8s） |
| V6 | protobuf 自洽 | `bash scripts/proto.sh generate` 后 `git diff` 与重生成前快照 `diff` | **逐字节一致**：重生成没有产生任何差异，证明 rawDesc 与 `.proto` 的 `go_package` 完全匹配 |
| V7 | protobuf 运行时 | `go run ./cmd/sforum extension builtins --root ../../extensions/builtin` | 正常列出 **9** 个包，exit 0，无 panic（该命令 `go list -deps` 确认依赖三个 `gen/sforum/**` 包，init 必然执行） |
| V8 | protobuf 运行时 | `go test ./sdk/plugin/v2/...` | ok 0.94s（真实 marshal/unmarshal 路径） |
| V9 | 版本注入 | `go build -ldflags "-X github.com/keeptrue/sforum/apps/api/version.Current=v9.9.9" -o /tmp/sf-test ./cmd/api && /tmp/sf-test --version` | `SForum v9.9.9 (...)`；`go tool nm` 显示符号 `github.com/keeptrue/sforum/apps/api/version.Current` |
| V10 | 运行时常量 | `rg RepositoryOwner apps/api/app/Models/SystemUpdates/types.go`；`rg SourceURL apps/api/version/version.go` | `keeptrue`；`https://github.com/keeptrue/SForum` |
| V11 | 架构门禁 | `node tests/validate-architecture-boundaries.mjs` | passed（1938 生产文件） |
| V12 | 契约 | `ruby scripts/validate-openapi-refs.rb` | OK（2907 refs / 61 文件） |
| V13 | 前端 | `cd apps/web && bun test tests/admin tests/forum` | 835 pass / 0 fail（102 文件） |
| V14 | 前端 | `cd apps/web && bun run typecheck` | exit 0 |
| V15 | 部署脚本子集 | `bash tests/validate-docs_test.sh`、`bash scripts/ci/verify-public-images_test.sh`、`bash deploy/scripts/deploy_test.sh` | 全部 `all checks passed`（完整 release-tooling gate 见「Blocking findings」） |
| V16 | git remote | `git remote -v` | `origin https://github.com/keeptrue/SForum.git`（fetch/push） |
| V17 | 工作树卫生 | `git status --porcelain` | 1653 个 ` M`（施工 1651 + baseline + sforum-latest.sh.sha256）+ 本决策记录 1 个新文件；**无其它未跟踪文件**；`storage/builtin-dev`、`tools/proto/.bin`、`tests/.logs`、`apps/web/.nuxt` 均被 gitignore |
| V18 | 镜像命名空间（外部） | GHCR anonymous token + manifest 探测 | `ghcr.io/keeptrue/sforum-{api,migrate,web}:3.0.13` 全部 HTTP 200（index 可拉取）；旧命名空间匿名 token 返回 `DENIED` |
| V19 | 完整门禁 | `./scripts/ci/release-tooling-gate.sh`（B1/B2 修复后） | **exit 0**：release/deploy 12 个 `*_test.sh`、Web SDK 打包 + 离线消费构建、docs 失败路径、builtin 契约（8 个）、latest-wrapper 校验全部通过 |

说明：`bash scripts/proto.sh check` 在**未提交**的工作树上必然报 exit 1，因为它的
判定基线是 `git status -- HEAD`，而 HEAD 仍是改名前的旧路径。这不是缺陷；V6 用
「重生成前后 diff 快照相同」给出了等价且更强的证明。提交后 `check` 即可通过。

## Blocking findings（验收轮发现，收尾轮已修复）

首轮验收时 `./scripts/ci/release-tooling-gate.sh` **exit 1**，两处红，均为派生工件未同步：

1. **`tests/builtin-plugin-release-baseline.json` 未刷新** → `validate-builtin-plugin-versions.mjs`
   报 source contract drifted。改名改了每个插件 `backend/go.mod` 的 `replace` 行，以及
   `apps/api/sdk/plugin/**`、`contracts/proto/**` 的内容，而 baseline 记录的正是这些内容的
   sha256。`--write` 后 diff 精确 **9 行**（`sharedRuntimeDigest` 1 项 + 8 个插件的
   `sourceDigest`），`version` 字段 **0 行**变化——纯改名不改变插件契约，不需要 bump 版本。
   修复：`node tests/validate-builtin-plugin-versions.mjs --write`；`SFORUM_BUILTIN_VERSION_BASE`
   在全仓没有任何设置点，版本递增校验当前处于休眠态。
   **另有一处与本次改名无关的既有漂移**：在未改名的 HEAD 上单独跑同一脚本同样失败，只有
   `sharedRuntimeDigest` 漂移（`4f9416cd…` → `09fb43ed…`），来源是 baseline 上次更新
   （`44716f8bc`，2026-10-25）之后的 `bf09379ae`、`a12e64f3a` 改过 `apps/api/sdk/plugin`。
   即 main 在这条 gate 上本来就是红的，本次只是被 `deploy/**` 变更触发了 `release-tooling.yml`。
   **为什么最终值是 `c406b255…` 而不是 `09fb43ed…`**：`digestSharedRuntime()` 是整包内容哈希，
   输入是 `contracts/proto/**/*.proto`、`apps/api/sdk/plugin/**` 下全部非测试 `.go`、
   `apps/api/app/Support/PluginBootstrap/contract.go` 的「文件名 + 内容」，再加 `apps/api/go.mod`
   里 go-plugin/grpc/protobuf 三行依赖，逐文件 `hash.update(name + "\0" + body + "\0")` 后取
   SHA-256。哈希之间没有可组合关系：`09fb43ed…` 只是「HEAD 旧内容」的哈希，改名重写同一批文件
   后必然得到另一个完整哈希。独立复核：在临时 worktree 里把本次全量 patch 应用到 HEAD 后跑
   `--write`，得到的 `sharedRuntimeDigest`（`c406b255…`）与 8 个 `sourceDigest` 与提交值
   **逐字节相同**——同时证明 baseline 里没有夹带改名之外的改动。
2. **`sforum-latest.sh.sha256` 未重新生成** → `validate-sforum-latest.sh` exit 1：`sforum-latest.sh`
   因仓库 slug 改名而改变（HEAD 侧 `7149f696…`，新内容 `d4cdd5c4…`）。README 与 4 份部署文档的
   一键安装/自更新都靠 `sha256sum -c sforum-latest.sh.sha256`，未同步会让所有按文档安装的用户失败。
   修复：`sha256sum sforum-latest.sh > sforum-latest.sh.sha256`，现为 `d4cdd5c4…`，校验通过。

两处修复后完整 gate **exit 0**（V19）。

## Rollback

1. **代码**：`git revert <rename-commit>`（或 `git revert --no-commit` 后人工合并）即可把
   27 个 `go.mod`、proto、文档、脚本一次性还原；本次是纯字符串替换、零逻辑变更，revert
   不会引入语义纠缠。派生工件（baseline、`sforum-latest.sh.sha256`）会随 revert 一起回到
   旧内容，二者仍然自洽。
2. **git remote**：`git remote set-url origin https://github.com/zhuchunshu/SForum.git`
   ——旧地址会 301 到新仓库，但 Go module path 必须与仓库路径一致，因此**回滚代码时必须
   同时回滚 remote**，否则 `go get` 与 module path 再次不一致。
3. **镜像**：不可回滚。GHCR 已把包迁到 `keeptrue` 命名空间，旧命名空间匿名拉取被拒（V18）。
   回滚代码后需要重新以旧命名空间发布，或保留新命名空间并在部署文档中说明。
4. **不随回滚的部分**：SECURITY.md 邮箱、`docs/archive/**`、`knowledge/sessions/**` 本次
   未改动，无需处理。

## 已知外部待确认（仓库外，本仓库无法关闭）

- **GHCR 旧命名空间已不可匿名拉取**：实测 `ghcr.io/keeptrue/sforum-{api,migrate,web}:3.0.13`
  全部 HTTP 200；旧命名空间匿名 token 返回 `DENIED`（V18）。因此**仍固定在
  `ghcr.io/zhuchunshu/sforum-*` 的既有部署在升级时会直接拉不到镜像**，这是账号改名的既成
  事实，回滚代码也无法恢复。升级说明必须写明把 registry 前缀换成 `ghcr.io/keeptrue/`。
- **npm Trusted Publisher 绑定**：`@sforum/admin-sdk`、`@sforum/plugin-ui` 的 Trusted
  Publisher 在 npmjs.org 侧记录仓库名。GitHub 改名后 OIDC 的 `repository` 声明变为
  `keeptrue/SForum`，若 npm 侧仍是旧名，下一次 `npm publish` 会被拒。
  `docs/extensions/trusted-admin-components.md` 已按新名改写，但那只是**对 npm 侧状态的
  断言**：release 前须在 npmjs.org 用 `npm trust list` 核对并把结果回填，未核对前不得当作
  已验证事实。
- **GitHub Actions 侧无需改动**：`release.yml` 全部使用 `${{ github.repository_owner }}` /
  `${{ github.repository }}`，没有硬编码 slug，改名后自动跟随。

## 残留扫描白名单口径

旧账号名的合法残留只有四类，其余任何命中都按缺陷处理：

1. `SECURITY.md` 的安全联系邮箱 `zhuchunshu999@gmail.com`（中英双语各 1 处，共 2 处）——
   邮箱不随 GitHub 账号迁移，理由见「Decision 4」。
2. `docs/archive/**`（33 处）——历史归档只读，改写会让当时的事实失真。
3. `knowledge/sessions/**`（19 处）——同上，会话记录是历史快照。
4. `knowledge/index.md:1697` 的历史分支名 `zhuchunshu-patch-1`（1 处）——已删除分支的客观名称。

补充：**本决策记录自身必然出现旧账号名 14 处**（标题、Context、module path、镜像路径、
邮箱、rawDesc 说明与示例、V1/V2 命令、回滚命令、旧命名空间说明、本节白名单条目 ×2、
本节扫描命令）。这不是缺陷，但会让「裸跑一次 rg 就断言干净」失效，因此约定：

- 扫描命令固定为
  `rg -n 'zhuchunshu' --hidden -g '!.git' -g '!node_modules' -g '!**/.nuxt*' .`
- 允许集合 = 上述 4 类 + 本文件；新增合法残留必须同步更新本节，否则视为缺陷。
- 大小写不敏感（`rg -i`）同样适用；本次实测两种扫法结果一致（55 处，全部在允许集合内）。
