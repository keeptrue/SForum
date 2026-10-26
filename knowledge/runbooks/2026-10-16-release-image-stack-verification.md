# Runbook: 发布镜像栈（`compose.release.yaml`）的验证与排错

适用对象：需要验证「已发布镜像能不能把站点跑起来」的运维/发布人员，以及想在本地复现
release 组合而不碰 registry 的开发者。

## 这个文件做什么

`compose.release.yaml` 是**覆盖文件**，它：

1. 把三个服务的 `build` 置空（`!reset null`）——发布栈不接受本地构建；
2. 把镜像固定为 `${SFORUM_REGISTRY:-ghcr.io/keeptrue}/sforum-<service>:${SFORUM_VERSION}`；
3. 设 `pull_policy: always`。

`SFORUM_VERSION` 是**必填**的：不设置时 compose 会逐服务报错并拒绝启动
（`Set SFORUM_VERSION to an immutable release tag`）。这是刻意的 fail-closed——
发布栈不允许"悄悄用 latest"。

## 正确用法

```sh
export SFORUM_VERSION=v1.2.3          # 不可变 tag
docker compose -f compose.yaml -f compose.release.yaml up -d
```

## 用本地构建的候选镜像验证同一条链路（不改仓库）

需要一个不在仓库里的覆盖文件，把 `pull_policy` 改回 `never`，并（可选）把 web 与
postgres 暴露到宿主便于探测：

```yaml
# /tmp/release-smoke-override.yaml
services:
  api:
    pull_policy: never
  migrate:
    pull_policy: never
  web:
    pull_policy: never
    ports:
      - "8097:3000"
  postgres:
    ports:
      - "15433:5432"
```

```sh
for t in api migrate web; do docker tag sforum-$t:candidate ghcr.io/keeptrue/sforum-$t:smoke-local; done
export SFORUM_VERSION=smoke-local APP_ENV=production \
  DATABASE_URL='postgres://sforum:sforum@postgres:5432/sforum?sslmode=disable' \
  REDIS_ADDR=redis:6379 EXTENSION_ROOT=/var/lib/sforum/extensions \
  NUXT_API_INTERNAL_BASE_URL=http://api:8080/api/v1 \
  SESSION_HASH_SECRET=… ALTCHA_SECRET=… APP_OPTION_ENC_KEY=… \
  IDENTITY_SUBJECT_HMAC_SECRET=…
docker compose -p sforum-release-smoke -f compose.yaml -f compose.release.yaml \
  -f /tmp/release-smoke-override.yaml up -d --no-build
docker compose -p sforum-release-smoke -f compose.yaml -f compose.release.yaml down -v
```

## 必须知道的三个坑（都实测过）

1. **不要把开发 `.env` 带进发布栈。** compose 会读仓库根的 `.env`，其中的 dev 值会
   覆盖 compose 的默认值。实测泄漏项：
   - `EXTENSION_ROOT=../../storage/extensions`（相对路径）。`config.Load` 在生产环境
     要求**绝对路径**，所以 API 会以命名配置错误退出——这是 fail-closed 生效，不是 bug；
   - `NUXT_API_INTERNAL_BASE_URL=http://127.0.0.1:8080/api/v1`：容器内的 `127.0.0.1`
     是容器自己，web 的 SSR 会 502/超时。发布栈需要 `http://api:8080/api/v1`；
   - `DATABASE_URL` / `REDIS_ADDR` 指向宿主端口。容器内必须用服务名（`postgres`/`redis`）；
   - `EXTERNAL_EXTENSION_ROOTS` 指向宿主绝对路径 → 容器里只产生
     `external extension source skipped` 警告（无害）。
   结论：**用干净环境或显式 env 文件跑发布栈**，不要复用开发 `.env`。

2. **`down` 也必须带同一组 `-f` 与 `SFORUM_VERSION`。** 否则 compose 会在插值阶段
   直接失败，容器与卷会留在机器上。清理时先确认：
   `docker ps -a | grep <project>` 与 `docker volume ls | grep <project>` 都为空。

3. **播种要在 API 启动前，或在启动后重启一次 API。** 首页内容依赖搜索索引对账：
   实测「先启动、后播种」时首页是空/错误态（81,801 字节、无分类/主题链接），
   重启 API 触发对账后恢复正常（106,147 字节、3 条种子主题链接）。

## 已验证过的期望结果（2026-10-16）

用本地候选镜像跑 release 组合（`APP_ENV=production`）：

```text
compose 解析            → 三个服务均为 ghcr.io/keeptrue/sforum-<svc>:<tag>，pull_policy: always
不设 SFORUM_VERSION     → compose 逐服务报错并拒绝启动（fail-closed）
启动后                  → postgres/redis healthy，api 自跑迁移（MIGRATE_ON_STARTUP=true），
                          GET /api/v1/health → 200 {"environment":"production"}
web                     → GET / → 200（桌面 navbar 1 + 移动 navbar 1 + footer），
                          /c/general → 200；播种 + 重启 API 后首页含 3 条种子主题链接
扩展台账                 → 宿主 CLI `extension list` 读到镜像内置：5 个 enabled、3 个 installed、主题 1
```

未验证：从 registry 真正 `docker pull` 已发布 tag（本次用的是本地候选镜像，
`pull_policy` 被覆盖为 `never`），以及 `compose.prod.yaml` 的反代/证书层。

## 真实已发布 tag 的验证（2026-10-17，补上「从 registry pull」残留）

`SFORUM_VERSION=v3.0.9`（ghcr 上最新完整 semver tag），**不写任何覆盖 pull_policy 的
文件**（保持 `pull_policy: always`），只用一个端口发布覆盖；env 用独立的干净文件
（`--env-file`），compose 因此**不读**仓库根的开发 `.env`——runbook 坑 1 由构造直接排除。

```sh
export SFORUM_VERSION=v3.0.9   # + 代理（pull 需要）
docker compose -p sforum-release-real -f compose.yaml -f compose.release.yaml \
  -f /tmp/ports-override.yaml --env-file /tmp/sforum-release-real.env pull
docker compose -p sforum-release-real -f compose.yaml -f compose.release.yaml \
  -f /tmp/ports-override.yaml --env-file /tmp/sforum-release-real.env up -d --no-build
```

实测（ghcr.io/keeptrue/sforum-{api,migrate,web}:v3.0.9）：

```text
migrate → 迁移 145 表 + 7 River（二次启动 "No migrations to apply"）
api     → GET /api/v1/health → 200 {"status":"ok","environment":"production"}
web     → GET / 200（82,148 字节 SSR，标题 SForum）、/c/general 200
扩展面   → 容器内多个 backend/plugin 子进程（不可变摘要路径）
```

### fail-closed 配置实测（v3.0.9 的命名报错，值得背下来）

1. `APP_OPTION_ENC_KEY` 必须是 **64 位 hex（32 字节）**，否则：
   `api bootstrap: create option cipher: crypto: invalid encryption key (expected 32-byte hex)`。

它在命名报错上 fail closed，不会带病启动。

> 历史记录：`MARKETPLACE_ED25519_PUBLIC_KEY_HEX` 曾是第二个 fail-closed 输入，
> 该预留代码已于 2026-10-24 整包删除（见
> `knowledge/decisions/2026-10-24-app-store-out-of-tree.md`），部署时不再需要该变量。

## `compose.prod.yaml` 反代/TLS 层验证（2026-10-17，行 9 第二残留）

拓扑与生产一致：`compose.yaml + compose.prod.yaml`（API/web 只发布到
`127.0.0.1:${API_PORT}/${WEB_PORT}`）+ **宿主网络里的 Caddy**（`deploy/caddy/Caddyfile`
按设计就是宿主层反代：它代理的是 `127.0.0.1` 上的已发布端口）：

```sh
docker compose -p sforum-prod-tls -f compose.yaml -f compose.prod.yaml \
  -f /tmp/images-override.yaml --env-file /tmp/sforum-prod-tls.env up -d --no-build
# images-override 只把 api/migrate/web 钉到 ghcr.io/...:v3.0.9（pull_policy: never），
# 避免重复本地构建；API_PORT=18080 WEB_PORT=13000。

docker run -d --name sforum-prod-tls-caddy --network host \
  -v ./deploy/caddy/Caddyfile:/etc/caddy/Caddyfile:ro \
  -e APP_DOMAIN=localhost -e API_PORT=18080 -e WEB_PORT=13000 \
  caddy:2.10.2-alpine caddy run --config /etc/caddy/Caddyfile --adapter caddyfile
```

实测探针（Caddy 自动为 `localhost` 签发内部 CA 证书）：

```text
HTTP  → HTTPS   : http://localhost/ → 308 Permanent Redirect（自动 HTTPS）
HTTPS 首页      : https://localhost/ → 200（82,095 字节 SSR）
安全响应头（H4） : strict-transport-security max-age=31536000; includeSubDomains
                  x-content-type-options nosniff / x-frame-options DENY
HTTPS API       : https://localhost/api/v1/health → 200 {"environment":"production"}
WebSocket 透传  : Upgrade 请求经 TLS 直达 Host API（Go 侧 401，不是 Caddy 错误）
未授权 SSE      : /api/v1/notifications/stream → 401（同上，证明流式路径可达 API）
```

一条运维注意：**必须用 `APP_DOMAIN` 配置的域名访问**（SNI 匹配）。用裸 IP
（`https://127.0.0.1`）握手会收到 `tlsv1 alert internal error`——Caddy 没有为 IP
签发证书，这是 ACME/内部 CA 的正常行为，不是缺陷。

清理：caddy 容器删除；`down -v` 必须带同一组 `-f`、`--env-file` 与 `SFORUM_VERSION`
（坑 2），容器与卷都确认清零。

## 管理端激活流程的自动化（`tests/admin-extension-activation.sh`）

M8 的「管理端激活自动化」在 M0 ADR 下不能绕过信任确认，所以自动化走的是**真实
HTTP 路径**：安装（惰性）→ 申请一次性精确制品信任挑战 → 带 token 启用。脚本：

```sh
SFORUM_SESSION_COOKIE=<super_admin 的 sforum_session> \
SFORUM_PACKAGE_ZIP=/tmp/pkg.sforum.zip \
SFORUM_UNINSTALL=1 bash tests/admin-extension-activation.sh
# 或提供 SFORUM_ADMIN_EMAIL + SFORUM_ADMIN_PASSWORD 让脚本自己登录取会话
```

行为与判定：

- 缺凭据或 zip 时打印 `SKIP` 并退出 0（离线/CI 友好）；`SFORUM_REQUIRE_LIVE=1` 时改为失败；
- 安装后断言 `status=installed`（惰性，绝不执行包代码）；
- 信任挑战：**可执行**包必须拿到 200 + token，否则判失败；不含可执行制品的包服务端返回
  `extension.trust_not_required`，脚本如实接受（这是服务端的正确判定，不是绕过）；
- 启用后断言 `status=enabled`；`SFORUM_UNINSTALL=1` 时再 disable + uninstall 收尾。

实测（2026-10-16，dev API `:8080`，一次性包 `acme.activation-probe`）：

```text
ok   session cookie supplied by environment
ok   install actprobe.sforum.zip → 201
ok   package is inert (status=installed)
ok   trust_not_required (package ships no executable artifact)
ok   enable acme.activation-probe → 200
ok   extension is enabled
ok   disable … → 200 / ok   uninstall … → 200
admin extension activation: OK
```

收尾后核对：扩展台账无该包记录、`storage/extensions/` 无残留目录。

## 双节点多运行验证（2026-10-17，M10 行 7 的真两节点证据）

拓扑：一台机器上**两个真实 API 节点**（独立容器/独立 hostname/独立 boot id）共享同一个
一次性 PostgreSQL + Redis，全部双节点用**真实已发布镜像 `v3.0.9`**。单独 compose 文件
（不与开发栈混用），两个 api 服务分别 `hostname: node-a / node-b`。

### 必须顺序启动（实测教训）

**两节点同时冷启动（空 schema + `MIGRATE_ON_STARTUP=true`）会互相死锁**：

```text
node-a 的 goose 会话级 advisory 锁由一个 idle 连接长期持有；
node-a 的 CREATE UNIQUE INDEX CONCURRENTLY 等 node-b 的先开事务结束；
node-b 的事务又在等 node-a 的 advisory 锁 —— 互等，无超时。
```

缓解（运维规则）：**先迁移（migrate 镜像/单节点先启动），再起第二节点；或第二节点
`MIGRATE_ON_STARTUP=false`**。部署惯例（release 栈里 migrate 服务先行、api 随后）
本来就不会命中此竞态；已按 v3.0.9 实测留证。第二类冷启动竞态：**同时首启**了两节点
会互相踩到 builtin 同步/legacy mail 迁移顺序（`builtin smtp plugin is not synchronized`
粗暴失败），顺序启动即不复现——写进 runbook：**多节点部署一律先启动单节点到 health 200。**

另：standalone compose 里不要漏 `BUILTIN_EXTENSION_ROOT: /app/extensions/builtin`
（compose.yaml 的 api 服务默认注入；漏了会因 builtins 不存在而启动失败）。

### 顺序启动后的实测探针（全部命中）

_prepare：节点 A 起来 → 用 `POST /api/v1/auth/register` 创建第一用户（initial super_admin，
CSRF 走 `GET /api/v1/csrf` + `X-Csrf-Token: <cookie值>`）→ 登录用 `{"login":…}` 字段 →_
_起节点 B。_

```text
P1 双节点注册     : plugin_runtime_nodes 两行 (node-a/api, node-b/api)，各自 boot_id，45s 租约
                    lease_live=t；heartbeat 每 10s 续。
P2 每节点每修订 ack: plugin_runtime_publication_acks 同一不可变 revision 下两行 status=applied
                    （node-a 17:35:20 / node-b 17:37:42，同 revision=1）。
P3 跨节点禁用     : node-a 管理端 disable sforum.web-push → 不可变 desired revision 2 →
                    node-b 在轮询窗口（5s 轮询 / LISTEN 唤醒）内 ack=applied，且
                    node-b 的 web-push 子进程消失（ps 实查）。
P4 跨节点启用     : node-a 再 enable → revision 3 → node-b ack=applied，web-push 以
                    **逐字节相同的不可变制品路径**（…/sforum.web-push/1.0.2/a982b361…/backend/plugin）
                    在两节点同时复活（exact-artifact 跨节点一致）。
P5 节点下线       : stop api-b → ≤45s 后 node-b 租约失效（lease_expired=t，last_seen 停止），
                    node-a health 200 不受影响。
P6 节点重入       : start api-b → 新 boot_id 注册并立刻 ack=applied 于最新 revision=3，
                    运行时全部复活；旧 boot 行保留到租约过期（不误删）。
```

### 复现（双节点）

```sh
# 1. 干净的一次性 DB + Redis（本 runbook 上一节的一次性库做法）；
# 2. /tmp/multinode-compose.yaml：两个 api 服务（v3.0.9，pull_policy: never，
#    hostname: node-a / node-b，同一个 DATABASE_URL/REDIS_ADDR，各发布 18091/18092），
#    postgres + redis healthcheck；DB 先清干净（含 sforum_core_v1 / sforum_host_extensions）。
# 3. 启动顺序：up api-a → health 200 → 起 api-b → 注册/登录 → 逐条跑 P1–P4；
#    然后 stop/start api-b 复跑 P5/P6。
# 4. 收尾：docker compose -p sforum-multinode -f /tmp/multinode-compose.yaml down -v。
```
