# Decision: 登记 GHSA-fc5q-39xc-vjpf（picomatch ReDoS）为审计基线例外，不升级依赖

## Status

Accepted（2026-11-18）。只改 `scripts/bun-audit-baseline.json` 一条记录，不动依赖树、
不改 `scripts/bun-audit-gate.mjs` 判据。

## Context

- **advisory**：GHSA-fc5q-39xc-vjpf / CVE-2026-112366，
  「picomatch vulnerable to ReDoS through single-branch repeated extglobs」。
- **严重度**：GitHub 评级 `high`，CVSS 3.1 **7.5**
  （`AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H`）——只有可用性影响，无机密性/完整性影响。
  发布 2026-10-19。
- **影响版本**：npm `picomatch` `<= 4.0.7`；`first_patched_version = null`。
  2026-11-18 核对：registry 上最新版本就是 `4.0.7`，上游没有修复版本。
- **触发场景**：把攻击者可控的 glob 模式喂给 picomatch 的 `makeRe`/匹配器，单分支重复
  extglob 导致正则回溯爆炸（CWE-1333）。
- **失败点**：Security run `44141921908` 的 `Dependency audit / web` job 在
  「Check the audit report against the ratchet baseline」步骤报
  `bun audit: 新增 1 条未登记 advisory`。

### 引入面（lockfile 实测，不是估计）

`apps/web/bun.lock` 共 **21** 条 picomatch 解析，落成 **3 份安装**：

| 版本 | 位置 | 在 advisory 范围内 |
| --- | --- | --- |
| `4.0.7` | 提升到 `node_modules/.bun/picomatch@4.0.7` | 是（`<=4.0.7`） |
| `4.0.5` | 嵌套（tinyglobby、unplugin 系列、@rollup/pluginutils 等） | 是 |
| `2.3.2` | 嵌套（micromatch、anymatch） | 是（`<=4.0.7` 含 2.x） |

20 个直接依赖方（`bun pm why picomatch`）：

- **Nuxt / Vite 构建链**：`nuxt`、`vite`、`@nuxt/icon`、`unplugin`、`unplugin-utils`、
  `unplugin-auto-import`、`unplugin-vue-components`、`unimport`、`tinyglobby`、`fdir`、
  `@rollup/pluginutils`、`@rollup/plugin-commonjs`、`rollup-plugin-visualizer`、
  `vite-plugin-checker`、`@vue/language-core`、`@vercel/nft`（Nitro 构建期文件追踪）、
  `@parcel/watcher-wasm`
- **文件匹配 / 监听**：`micromatch`、`anymatch`
- **路由类型生成**：`vue-router`（命中的是 `dist/unplugin-*.mjs`，构建期插件，不是运行时
  路由入口）

`bun audit --prod` 仍然会看到它：`nuxt`、`@nuxt/ui`、`vue-router`、`@nuxtjs/seo` 这些
框架包本身在 `dependencies` 里，构建工具链随之进入生产依赖树。所以只能走「登记」，
不能靠「移出 --prod 范围」假装看不见——那等于改扫描口径。

## Decision

**把该 advisory 以 `high` 登记进 `scripts/bun-audit-baseline.json`（`packages: ["picomatch"]`），
不升级、不 patch、不改判据。** 三条实证：

1. **没有可升的版本**。`first_patched_version = null`，registry 最新即 `4.0.7`。在无修复
   版本的前提下改依赖，只是换一个版本号而不消除 advisory，换来的是 Nuxt 构建链的真实
   回归面。
2. **不进 SSR 运行时**。对本机生产构建产物（`apps/web/.output`，构建时间
   2026-11-18 00:17，晚于 `bun.lock` 的 11-17 17:12）实测：`.output/server` 里没有
   picomatch 模块目录，全目录仅 2 处字符串命中，且都是
   `node_modules/vue-router/package.json` 的依赖声明（第 137、151 行），没有任何代码
   `import`。Nitro 使用 `@vercel/nft` 追踪运行时依赖，构建工具链不会被打进服务器产物。
3. **没有可控输入**。第一方代码 0 处 import picomatch；`app/`、`server/`、`shared/` 内
   0 处 `tinyglobby` / `globby` / `fast-glob` 调用。ReDoS 需要攻击者提交 glob 模式；
   本仓库所有 pattern 都来自构建配置与目录扫描（图标集合、路由类型、文件监听），
   不来自 HTTP 请求参数。

**核对边界（诚实标注）**：第 2 条证据是本机生产构建产物，不是线上部署快照。若部署形态
改变（例如直接把 `nuxt` 当服务器跑、或在 server route / 插件运行时里跑 glob），结论不再
成立，按复位条件 2 重开。

## 基线登记方式

`--refresh` 会整体重算基线（重写全部条目与包集合），所以不能对着真文件盲刷：

1. 备份原文件；
2. 刷新到临时文件，`diff` 核对；
3. 本次 diff 只有新增 1 条（6 行），其余 44 条与包集合零变动（`packageDrift = 0`），
   再写回真基线。

## 复位条件（满足任一条就重开本条，不要沿用「构建期工具链」的结论）

1. **上游出现修复版本**：`first_patched_version` 不再为 null，或 registry 发布修复版。
   动作：升级 picomatch（必要时用 overrides 覆盖嵌套版本，三份安装都要离开受影响的
   范围）、重算 `bun.lock`、跑 `bun audit --prod` 确认该 advisory 消失、**删掉基线里这
   一条**，让门自然变绿。不允许保留条目「以防万一」——基线里多出来的条目会让门变红。
2. **进入运行时**：`.output/server` 出现 picomatch，或第一方代码 / server route / 插件
   运行时开始把**外部输入**喂给 picomatch 或依赖它的 glob 库。此时「无可控输入」的前提
   消失，先评估替换 matcher 或把该能力隔离，再谈登记。
3. **advisory 元数据变化**：影响范围收缩（例如不再包含 2.3.2）、严重度变化或 advisory
   被撤回。重跑 `bun audit --prod`：若报告里不再出现，删条目；若严重度变化，门会自己
   判红并要求 `--refresh` 记账。
4. **扫描口径变化**：`security.yml` 从 `--prod` 改为全量扫描，或重新引入
   fail-on-severity 口径。届时「生产依赖树里存在但不可达」不再能作为豁免理由，重开裁决。

## 不得绕过

不得用**降低严重度**、**删条目**、`--unseeded-policy=notice`、`continue-on-error`、
包排除、或修改 `scripts/bun-audit-gate.mjs` 判据的方式让门变绿。棘轮本身堵住了「塞进去
就忘掉」：基线里有、报告里没有的条目会判红（`--self-test` 场景 3），所以这条登记只能
随 advisory 消失而删除，不能被静默继承。

## 复核命令

```sh
cd apps/web && export https_proxy=http://127.0.0.1:7897 http_proxy=http://127.0.0.1:7897 \
  all_proxy=socks5://127.0.0.1:7897
bun audit --prod --registry https://registry.npmjs.org --json > /tmp/audit.json

cd .. && node scripts/bun-audit-gate.mjs --report /tmp/audit.json \
  --baseline scripts/bun-audit-baseline.json --unseeded-policy=fail; echo "exit=$?"   # 期望 0
node scripts/bun-audit-gate.mjs --self-test                                          # 8 scenarios passed

cd apps/web
bun pm why picomatch | grep -E '^  [├└]─ ' \
  | sed -E 's/^  [├└]─ (optional peer )?//; s/@[0-9][^ ]*//; s/ \(requires.*//' \
  | sort -u | wc -l                                              # 20 个直接依赖方
grep -c 'picomatch": \[' bun.lock                               # 21 条解析
ls node_modules/.bun | grep -c picomatch                        # 3 份安装（4.0.7/4.0.5/2.3.2）
grep -rn picomatch .output/server | wc -l                       # 2，均为 vue-router 的 package.json 声明
find .output -name 'picomatch*' | wc -l                         # 0，运行时产物里没有该模块
```

## References

- 扫描门：`.github/workflows/security.yml`（job `Dependency audit / web`）
- 棘轮实现与语义：`scripts/bun-audit-gate.mjs`（新增 / 失效 / 严重度变化三条硬失败）
- 基线：`scripts/bun-audit-baseline.json`
- 同类裁决（写法参照）：`knowledge/decisions/2026-11-16-go-2026-5932-xcrypto-openpgp.md`
- 失败证据：Security run `44141921908`
