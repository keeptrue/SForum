# 快速开始

[← 中文文档首页](./README.md)

用最少步骤在本机跑通 SForum（开发模式）。

## 前置条件

| 工具 | 说明 |
| --- | --- |
| Docker + Docker Compose | 启动 PostgreSQL、Redis、Mailpit |
| Go **1.26.11+** | API / worker（版本锚定于 `apps/api/go.mod`） |
| [Air](https://github.com/air-verse/air) | API 热重载：`go install github.com/air-verse/air@latest` |
| [Bun](https://bun.sh) | 前端依赖与 dev server |
| Git | 克隆仓库 |

可选：中国大陆网络下安装依赖前设置本地代理（见 `AGENTS.md`）。

## 三步启动

### 1. 依赖服务 + 迁移

在仓库根目录：

```sh
./scripts/dev.sh
```

会做什么：

- 若无 `.env`，从 `.env.example` 复制
- 用 Compose 启动 **PostgreSQL、Redis、Mailpit**
- **默认不**启动 Meilisearch（可选，见下文）
- 健康检查通过后跑数据库迁移
- **不会**启动前端或 API（由你在本机进程启动）

常用参数：

```sh
./scripts/dev.sh --build      # Dockerfile/依赖变更后重建镜像
./scripts/dev.sh --no-migrate # 仅起依赖、跳过一次性迁移
```

停止依赖：

```sh
./scripts/dev-down.sh
```

### 2. API（内嵌 worker）

另开终端：

```sh
./scripts/api-dev.sh
```

- 读取根目录 `.env`
- 编译/同步内置插件到开发 staging 树
- 用 Air 启动 API；队列任务始终由同一 API 进程消费

### 3. 前端

再开终端：

```sh
cd apps/web && bun install   # 首次
cd apps/web && bun run dev
```

打开：<http://127.0.0.1:3000>

> **注意：** 用户常年占用 3000 端口的 Nuxt 进程时，不要擅自 kill。

## 验证是否正常

| 检查 | URL |
| --- | --- |
| 站点首页 | http://127.0.0.1:3000 |
| API 存活 | http://127.0.0.1:3000/api/v1/health |
| API 就绪 | http://127.0.0.1:3000/api/v1/ready（需 PostgreSQL） |
| Web 健康 | http://127.0.0.1:3000/health |
| Mailpit 收件箱 | http://127.0.0.1:18025 |

## 首次管理员

1. 打开注册页完成**第一个账号**注册  
2. 该用户成为受保护的 **`super_admin`**  
3. 管理后台默认前缀：`/control-panel`（可由运行时配置调整）  
4. 详见 [首次注册与超级管理员](./usage/first-login.md)

## 默认开发端口（loopback）

| 服务 | 默认地址 |
| --- | --- |
| Web | `127.0.0.1:3000` |
| API 直连 | `127.0.0.1:8080` |
| PostgreSQL | `127.0.0.1:15432` |
| Redis | `127.0.0.1:16379` |
| Mailpit SMTP / UI | `11025` / `18025` |
| Meilisearch（可选） | `127.0.0.1:17700` |

端口以根目录 `.env` 为准。

## 可选：Meilisearch

默认搜索是内置 **站点搜索**（PostgreSQL FTS），无需 Meili。

若要试用 Meili 插件：

```sh
docker compose --profile search up -d meilisearch
```

再通过 `EXTERNAL_EXTENSION_ROOTS` 扫描并信任独立仓库中的 `plugins/sforum-search-meilisearch`，在后台选择 `search.provider` 并重建索引。详见 [搜索](./usage/search.md)。

## 下一步

- 站长：[使用说明](./usage/README.md)
- 开发者：[环境搭建](./development/setup.md) · [日常工作流](./development/workflow.md)
- 上线：[生产部署](./deployment.md)
