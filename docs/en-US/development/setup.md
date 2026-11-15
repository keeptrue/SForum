# Environment setup

[← Development](./README.md)

## 1. Clone and tools

```sh
git clone <your-fork-or-upstream> SForum
cd SForum
```

Install Docker, Go 1.26.11+ (the toolchain is anchored by `apps/api/go.mod` and
that file is authoritative), Air, Bun, and optionally Ruby for OpenAPI ref
validation.

### Network proxy

If required, export proxies before `go get` / `bun install` (see `AGENTS.md`).

## 2. Environment file

```sh
./scripts/dev.sh   # creates .env from .env.example when missing
# or: cp .env.example .env
```

Notes:

- `config.Load` does **not** auto-read `.env`; dev scripts export vars  
- Many site options are admin-managed at runtime  
- Production uses `.env.production`—see [Deployment](../deployment.md)

## 3. Start the stack

See [Getting started](../getting-started.md). Recommended terminals:

| # | Command |
| --- | --- |
| 1 | `./scripts/dev.sh` |
| 2 | `./scripts/api-dev.sh` |
| 3 | `cd apps/web && bun run dev` |

The API always embeds and owns the Worker. `scripts/worker-dev.sh` remains only
as an explicit compatibility error so it cannot start duplicate consumers.

## 4. Troubleshooting

| Issue | What to do |
| --- | --- |
| API port busy | `api-dev.sh` only reclaims leftover `sforum-api`; refuses other owners |
| Port 3000 busy | Treat as the user’s Nuxt; do not kill blindly |
| Built-in digests | Dev stages builtins outside the tracked tree |
| Download failures | Check proxy/DNS |

## Next

- [Daily workflow](./workflow.md)  
- [Testing](./testing.md)  
