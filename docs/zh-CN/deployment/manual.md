# 手动 Compose 部署

[← 生产部署](../deployment.md)

这页给不想使用 `sforum-bootstrap.sh` / `deploy.sh` 的操作者：自己下载部署
包、自己写 `.env.production`、自己执行迁移和启动。流程可以完成生产部署，
但它是**次选路径**：没有自动备份、镜像身份核对、部署锁、`.deployrc` 和
蓝绿切换。生产环境仍推荐[一键安装与更新](./one-click.md)。

## 与脚本路径的差异

| 能力 | 脚本路径 | 手动路径 |
| --- | --- | --- |
| 工具包刷新 | bootstrap 自动完成 | 自己下载新包并替换 |
| 版本解析 | stable/prerelease + 不可变标签 | 自己写 `SFORUM_VERSION` |
| 部署前备份 | `deploy.sh` 自动 | 自己先跑备份 |
| 镜像身份核对 | `deploy.sh` 自动 | 自己执行 `--version` |
| 并发保护 | `.deploy.lock` | 无，靠操作纪律 |
| 状态记录 | `.deployrc` | 无；可事后接管生成 |
| 零停机切换 | `upgrade.sh` 蓝绿 | 需要自己设计 |

## 步骤 0：获取并校验部署包

```sh
(
  set -eu
  mkdir -p sforum
  cd sforum
  curl -fsSLo sforum-deploy.tar.gz \
    https://github.com/keeptrue/SForum/releases/latest/download/sforum-deploy.tar.gz
  curl -fsSLo SHA256SUMS \
    https://github.com/keeptrue/SForum/releases/latest/download/SHA256SUMS
  awk '$2 == "sforum-deploy.tar.gz" { print }' SHA256SUMS > sforum-deploy.sha256
  test "$(wc -l < sforum-deploy.sha256 | tr -d '[:space:]')" = 1
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c sforum-deploy.sha256
  else
    shasum -a 256 -c sforum-deploy.sha256
  fi
  if command -v gh >/dev/null 2>&1; then
    gh attestation verify sforum-deploy.tar.gz --repo keeptrue/SForum
  fi
  tar -xzf sforum-deploy.tar.gz --strip-components=1
)
```

固定版本时把两个 `latest/download` 地址换成
`download/<TAG>/...`。校验失败就不要解压，更不要执行包内脚本。

## 步骤 1：主机准备

- Linux `amd64` 或 `arm64`；不支持 Windows。
- Docker Engine 与 Docker Compose `2.24.4+`。
- `curl`、`tar`、`openssl`（或 `/dev/urandom`）。
- 一个指向本机的域名；宿主机 Caddy/Nginx 负责 TLS。
- loopback 的 `WEB_PORT`、`API_PORT` 可用。

## 步骤 2：写 `.env.production`

从示例复制，再改值：

```sh
cp .env.production.example .env.production
chmod 600 .env.production
```

最小必填项如下。完整参考见[环境变量与密钥](./environment.md)。

| 变量 | 要求 |
| --- | --- |
| `APP_ENV` | 必须是 `production` |
| `APP_URL` | 公开的 `https://` 地址 |
| `SFORUM_VERSION` | 具体不可变标签，例如 `<TAG>` |
| `POSTGRES_PASSWORD` | 至少 32 字符，建议 `openssl rand -hex 32` |
| `DATABASE_URL` | `postgres://sforum:<同一密码>@postgres:5432/sforum?sslmode=disable` |
| `REDIS_PASSWORD` | 至少 32 字符 |
| `SESSION_HASH_SECRET` | 至少 32 字符 |
| `IDENTITY_SUBJECT_HMAC_SECRET` | 至少 32 字符 |
| `APP_OPTION_ENC_KEY` | 64 位 hex |
| `ALTCHA_SECRET` | 至少 32 字符 |
| `MARKETPLACE_ED25519_PUBLIC_KEY_HEX` | 64 位 hex；官方 Marketplace 需要官方公钥 |
| `CSRF_TRUSTED_ORIGINS` | 公开站点 origin，例如 `https://forum.example.com` |
| `NUXT_PUBLIC_API_BASE_URL` | 必须是 `/api/v1` |
| `NUXT_PUBLIC_ADMIN_ROUTE_PREFIX` | 安全路径，例如 `/control-panel` |
| `MIGRATE_ON_STARTUP` | 建议 `false`，然后显式执行迁移 |

生成密钥：

```sh
openssl rand -hex 32   # 可重复执行，每个秘密单独生成
```

`APP_DOMAIN` 只给宿主机 Caddy 示例使用，不会进入应用容器。反向代理后要
设置 `TRUST_PROXY=true` 和精确的 `TRUSTED_PROXIES`；生产默认不信任任何
网段。

## 步骤 3：校验 Compose 展开

先定义一个 shell 数组，后面所有命令复用：

```sh
COMPOSE=(docker compose --env-file .env.production \
  -f compose.yaml -f compose.prod.yaml -f compose.release.yaml)

# 后面的 docker run 也要用同一个标签，先从环境文件读出来并导出。
SFORUM_VERSION="$(sed -n 's/^SFORUM_VERSION=//p' .env.production)"
export SFORUM_VERSION

"${COMPOSE[@]}" --profile tools config --quiet
```

加 `--profile tools` 让 `migrate` 服务也参与展开检查。`config --quiet` 会检查
变量插值、服务定义和 `SFORUM_VERSION` 是否存在。
`compose.release.yaml` 对缺失版本会直接报错，这是有意的 fail-closed。

## 步骤 4：拉取并核对镜像

```sh
"${COMPOSE[@]}" pull migrate api web

docker run --rm "ghcr.io/keeptrue/sforum-api:${SFORUM_VERSION}" sforum-api --version
docker run --rm "ghcr.io/keeptrue/sforum-migrate:${SFORUM_VERSION}" sforum-migrate --version
```

输出应包含 `SForum <版本>`。如果标签写错或 Registry 返回了别的构建，不要
继续启动。

## 步骤 5：启动依赖、迁移、启动应用

```sh
"${COMPOSE[@]}" up -d --wait postgres redis
"${COMPOSE[@]}" run --rm -T --pull never migrate
"${COMPOSE[@]}" up -d --no-build --pull never postgres redis api web
```

顺序不能反：迁移要在 API 启动前完成。`--pull never` 确保迁移和启动使用
刚刚核对过的本地镜像，不会在旧应用停止后再次依赖 Registry。

如果你把 `MIGRATE_ON_STARTUP` 设为 `true`，API 启动时也会尝试迁移；两种
模式二选一即可，但不要设为 `false` 又不执行 `migrate`。

## 步骤 6：健康检查

```sh
curl -fsS "http://127.0.0.1:${API_PORT:-18080}/api/v1/ready"
curl -fsS "http://127.0.0.1:${WEB_PORT:-3000}/health"

"${COMPOSE[@]}" ps
"${COMPOSE[@]}" logs --tail=200 api web
```

`/api/v1/ready` 依赖 PostgreSQL；Redis 或 Meili 不可用时是降级就绪，不会
让整个站点不可用。

## 步骤 7：反向代理与 TLS

宿主机 Caddy 示例在 `deploy/caddy/Caddyfile`。核心目标：

- 普通 HTTP 指向 `127.0.0.1:${WEB_PORT:-3000}`；同域 `/api/v1/*` 由 Nuxt
  转发到 `api:8080`。
- WebSocket Upgrade 指向 `127.0.0.1:${API_PORT:-18080}`。
- TLS 由宿主机终止；容器不监听公网。
- 反代后设置 `TRUST_PROXY=true`，并把 `TRUSTED_PROXIES` 设为反代地址或
  网段，例如同机 Caddy 用 `127.0.0.1/32`。不要写 `0.0.0.0/0`。

Caddy 进程读取的是宿主机环境变量，不是容器内的 `.env.production`：

```sh
APP_DOMAIN=forum.example.com WEB_PORT=3000 API_PORT=18080 caddy run \
  --config deploy/caddy/Caddyfile --adapter caddyfile
```

## 步骤 8：首个管理员与站点设置

浏览器打开 `APP_URL`，完成第一个账号注册；该账号会成为受保护的
`super_admin`。管理后台地址是 `APP_URL + NUXT_PUBLIC_ADMIN_ROUTE_PREFIX`，
默认 `/control-panel`。

随后在后台配置站点名称、HTTPS URL、SMTP、附件存储和搜索。邮件不配置时，
密码重置和通知不可用。

## 步骤 9：首次备份

```sh
./deploy.sh --action backup
```

它会先启动 `postgres`，再把备份写到 `backups/<db>-<UTC>.sql`，权限 `0600`。
也可以用 `SFORUM_BACKUP_DIR` 指定宿主机路径。备份文件不要只留在容器卷里。

## 手动更新

```sh
# 1. 先备份
./deploy.sh --action backup

# 2. 改 .env.production 的 SFORUM_VERSION 为目标标签，并重新导出
SFORUM_VERSION="$(sed -n 's/^SFORUM_VERSION=//p' .env.production)"
export SFORUM_VERSION

# 3. 拉取并核对
"${COMPOSE[@]}" pull migrate api web

# 4. 停应用，迁移，再启动
"${COMPOSE[@]}" stop api web
"${COMPOSE[@]}" run --rm -T --pull never migrate
"${COMPOSE[@]}" up -d --no-build --pull never postgres redis api web

# 5. 健康检查
curl -fsS "http://127.0.0.1:${API_PORT:-18080}/api/v1/ready"
curl -fsS "http://127.0.0.1:${WEB_PORT:-3000}/health"
```

这段是停机更新：`api`/`web` 停止期间站点不可用。需要零停机时用
`upgrade.sh`，不要手工模拟蓝绿。

## 手动回滚

只有确认数据库迁移向后兼容时，才能只回退镜像：

```sh
# 把 SFORUM_VERSION 改回旧标签，并重新导出
SFORUM_VERSION="$(sed -n 's/^SFORUM_VERSION=//p' .env.production)"
export SFORUM_VERSION
"${COMPOSE[@]}" pull migrate api web
"${COMPOSE[@]}" stop api web
"${COMPOSE[@]}" up -d --no-build --pull never postgres redis api web
```

迁移不兼容时，回退镜像会让旧代码读到新表结构。此时应恢复部署前备份：

```sh
SFORUM_CONFIRM_RESTORE=RESTORE ./deploy.sh --action restore
```

恢复脚本会在独立临时库中单事务导入，校验应用表后才原子切换库名；任一
SQL 错误都不会替换原库。

## 交回脚本管理

手动安装没有 `.deployrc`，bootstrap upgrade 会拒绝。先跑一次部署动作让
`deploy.sh` 记录状态：

```sh
./deploy.sh --action deploy --version <TAG>
```

它会备份、迁移、启动并写入 `.deployrc`。之后就可以使用
`./sforum-bootstrap.sh upgrade` 和蓝绿更新。
