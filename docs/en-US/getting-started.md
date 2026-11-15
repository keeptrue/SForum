# Getting started

[← English docs home](./README.md)

Run SForum locally in development with the smallest number of steps.

## Prerequisites

| Tool | Notes |
| --- | --- |
| Docker + Docker Compose | PostgreSQL, Redis, Mailpit |
| Go **1.26.11+** | API / worker (anchored by `apps/api/go.mod`) |
| [Air](https://github.com/air-verse/air) | API hot reload: `go install github.com/air-verse/air@latest` |
| [Bun](https://bun.sh) | Frontend packages and dev server |
| Git | Clone the repo |

Optional: set a local HTTP proxy before package downloads if required in your network (see `AGENTS.md`).

## Three steps

### 1. Dependencies + migrations

From the repository root:

```sh
./scripts/dev.sh
```

This will:

- Create `.env` from `.env.example` when missing
- Start **PostgreSQL, Redis, and Mailpit** via Compose
- **Not** start Meilisearch by default (optional; see below)
- Run database migrations after health checks
- **Not** start the web app or API (you run those as host processes)

Useful flags:

```sh
./scripts/dev.sh --build      # rebuild images after Dockerfile/deps changes
./scripts/dev.sh --no-migrate # start deps only; skip one-shot migrate
```

Stop dependencies:

```sh
./scripts/dev-down.sh
```

### 2. API (embedded worker)

In another terminal:

```sh
./scripts/api-dev.sh
```

- Loads root `.env`
- Builds/stages built-in plugins for dev
- Starts the API with Air; the same API process always consumes background jobs

### 3. Frontend

```sh
cd apps/web && bun install   # first time
cd apps/web && bun run dev
```

Open: <http://127.0.0.1:3000>

> **Note:** If port 3000 is already serving Nuxt, assume it is the user’s process—do not kill it without asking.

## Health checks

| Check | URL |
| --- | --- |
| Site | http://127.0.0.1:3000 |
| API liveness | http://127.0.0.1:3000/api/v1/health |
| API readiness | http://127.0.0.1:3000/api/v1/ready (PostgreSQL required) |
| Web health | http://127.0.0.1:3000/health |
| Mailpit UI | http://127.0.0.1:18025 |

## First administrator

1. Register the **first** account in the browser  
2. That user becomes the protected **`super_admin`**  
3. Admin default prefix: `/control-panel`  
4. Details: [First registration & super admin](./usage/first-login.md)

## Default development ports (loopback)

| Service | Default |
| --- | --- |
| Web | `127.0.0.1:3000` |
| API direct | `127.0.0.1:8080` |
| PostgreSQL | `127.0.0.1:15432` |
| Redis | `127.0.0.1:16379` |
| Mailpit SMTP / UI | `11025` / `18025` |
| Meilisearch (optional) | `127.0.0.1:17700` |

Ports follow the root `.env`.

## Optional: Meilisearch

Default search is built-in **site search** (PostgreSQL FTS)—no Meili required.

```sh
docker compose --profile search up -d meilisearch
```

Then scan and trust `plugins/sforum-search-meilisearch` from an independent repository via `EXTERNAL_EXTENSION_ROOTS`, select `search.provider`, and reindex. See [Search](./usage/search.md).

## Next

- Operators: [Usage](./usage/README.md)
- Developers: [Setup](./development/setup.md) · [Workflow](./development/workflow.md)
- Production: [Deployment](./deployment.md)
