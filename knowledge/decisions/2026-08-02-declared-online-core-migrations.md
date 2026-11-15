# Decision: Declared online Core migrations

## Status

Accepted

## Context

The blue/green updater previously rejected every pending database migration.
That was safe, but the documented `deploy.sh` fallback did not recognize an
existing blue/green edge and misclassified its loopback ports as foreign. Many
Core migrations are additive and can preserve both old and new binaries, while
arbitrary SQL and River's internal schema cannot be assumed compatible.

## Decision

Online migration is opt-in and target-artifact authoritative:

- The migrator image advertises
  `io.sforum.migrations.online-safe-check=v1`. The host never passes the new
  check argument to an older image because older CLIs treat unknown arguments
  as a normal migration run. New migrator binaries reject unknown or combined
  operation arguments before loading configuration or touching the database.
- Every pending Core SQL migration must declare `-- +sforum OnlineSafe` in its
  Up section. The declaration means the expanded schema and data semantics keep
  every supported source binary and the target binary operational through a
  failed candidate start or router rollback.
- Declared migrations remain transactional and set bounded PostgreSQL
  `lock_timeout` and `statement_timeout`. Timeout failure leaves the old slot
  serving and prevents a traffic switch.
- River must already match the target exactly. River migrations and every
  undeclared, unbounded, non-transactional, destructive, or semantic-contract
  migration require a maintenance window.
- `upgrade.sh` orders the operation as target image verification, read-only
  classification, PostgreSQL backup, online migration, exact post-check,
  candidate health, router switch, Worker drain/start, and old-slot stop.
- `deploy.sh` recognizes a persisted blue/green topology as the owner of the
  configured ports. Its maintenance flow backs up first, stops all slots and
  the edge, migrates, and starts direct target services.

## Consequences

- A release may retain HTTP availability across additive Core migrations, but
  operators should still expect bounded latency from row or catalog locks.
- The declaration is a reviewable compatibility promise, not SQL inference.
  Destructive contraction belongs in a later maintenance release after old
  binaries are outside the rollback window. The author-facing pre-commit
  checklist for that promise lives in
  `2026-11-15-online-safe-migration-human-contract.md`.
- Published images remain immutable. Targets built before the capability label,
  including `v3.0.7`, continue to require the maintenance path even if their SQL
  would qualify under the new contract.
