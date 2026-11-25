# Knowledge Index

Project memory entry point. Read this file, one relevant module note, and only
the active plan or hot handoff needed for the task. Cold sessions, reports, and
archives are historical evidence, not current status.

## Active Workstreams

### CI/CD And Test Speedup (2026-11-24 / 2026-11-25)

- Status: M0-M6 landed; waiting for a real Release run, remote confirmation,
  and GHCR path observations.
- No-cache `./scripts/test.sh`: about 195s in the M5 record; steady
  `./scripts/test.sh --fast`: about 43s.
- Handoff: [2026-11-25-ci-cd-test-speedup-batch4.md](sessions/2026-11-25-ci-cd-test-speedup-batch4.md)
- Plan: [2026-11-24-ci-cd-and-test-speedup.md](plans/2026-11-24-ci-cd-and-test-speedup.md)
- Decisions:
  [template DB and package parallelism](decisions/2026-11-25-go-template-db-and-package-parallelism.md),
  [container and path gating](decisions/2026-11-25-ci-container-and-path-gating.md),
  [release asset critical path](decisions/2026-11-25-release-asset-critical-path-split.md).

### Search Page Aurora Glass (2026-11-24)

- `/search` uses an independent Host island (`components/forum/search/`) bound
  as `forum.component.search_page`; `SFHomePage` serves only the home page.
- Five public search components have stable V3 catalog identities.
- Handoff: [2026-11-24-search-page-aurora-glass.md](sessions/2026-11-24-search-page-aurora-glass.md)
- Modules: [search](modules/search.md), [frontend](modules/frontend.md).

## Long-Lived Programs

- V3 trusted plugin/theme platform:
  [plan](plans/2026-07-13-trusted-plugin-theme-platform-v3.md),
  [progress](plans/2026-07-13-trusted-plugin-theme-platform-v3-progress.md),
  [module](modules/extensions.md).
- Extension authoring and surface density:
  [plan](plans/2026-10-16-plugin-frontend-experience.md),
  [module](modules/extensions.md).
- Trust levels:
  [N-tier plan](plans/2026-11-11-trust-levels-n-tiers.md),
  [conditional thresholds](plans/2026-11-14-trust-level-conditions.md),
  [module](modules/trust-levels.md).

## Module Navigation

| Module | Current ownership |
| --- | --- |
| [ai](modules/ai.md) | AI gateway, providers, replies, tools, budgets |
| [attachments](modules/attachments.md) | Upload, storage, variants, attachment policy |
| [backend](modules/backend.md) | Go API, persistence, routing, runtime assembly |
| [badges](modules/badges.md) | Rules, grants, facts, catalog, notifications |
| [extensions](modules/extensions.md) | Plugin/theme lifecycle, trust, runtime, registries |
| [forum](modules/forum.md) | Topics, comments, taxonomy, content policy |
| [frontend](modules/frontend.md) | Nuxt, UI shell, Page Registry, themes, browser behavior |
| [identity](modules/identity.md) | Auth, sessions, RBAC, profiles, external identity |
| [jobs](modules/jobs.md) | River jobs, scheduling, reconciliation |
| [localization](modules/localization.md) | Locales, language packs, translated contracts |
| [mail](modules/mail.md) | Mail providers and transactional delivery |
| [moderation](modules/moderation.md) | Reports, review workflows, moderation policy |
| [notifications](modules/notifications.md) | Fanout, channels, preferences, push, SSE |
| [options](modules/options.md) | Runtime site settings and public options |
| [profile](modules/profile.md) | Public/self profile and activity surfaces |
| [search](modules/search.md) | Site search, PostgreSQL FTS, optional Meilisearch |
| [trust-levels](modules/trust-levels.md) | Tiers, conditions, limits, earnable permissions |

`modules/README.md` explains the module-note convention.

## Project Memory

| Path | Use |
| --- | --- |
| `modules/` | Current durable feature truth; prefer over dated prose |
| `decisions/` | Accepted architecture/product decisions |
| `plans/` | Active, ready, blocked, and blueprint task books |
| `plans/README.md` | Plan status index |
| `sessions/` | Only current hot handoffs are default context |
| `sessions/archive/` | Retained cold checkpoints; explicit recovery only |
| `reports/` | Point-in-time evidence; explicit recovery only |
| `runbooks/` | Reproducible operational procedures |
| `glossary.md` | Shared framework vocabulary |
| `archive/` | Legacy material retained only when still referenced |

The root `.rgignore` hides cold sessions, reports, and archives from default
`rg` traversal. Use `rg -u` or explicit paths when recovering evidence.

## Session Rules

1. Read root `AGENTS.md` and the relevant `apps/api/AGENTS.md` or
   `apps/web/AGENTS.md`.
2. Read this index, one relevant module note, and only the active plan/handoff
   needed for the task.
3. Prefer CodeGraph for code discovery. Use `rg -n` plus `sed -n` slices for
   docs; do not bulk-load cold history.
4. Do not read `sessions/archive/`, `plans/archive/`, `reports/`, or `archive/`
   unless recovering historical evidence.
5. Update the living module note instead of appending completed work here.
6. Keep one short hot handoff per active workstream. Add a decision for durable
   architecture/product choices and update plan status when it changes.

## Open Questions

- What are the real Release job timeline, final asset set, attestation result,
  and GHCR read-path variance after the CI/CD speedup lands remotely?
- Should narrow search results render summaries, and should the duplicate
  `SFContentColumnFooter`/theme footer be consolidated?
- What backup destination and retention policy should operator deployments use
  by default?
