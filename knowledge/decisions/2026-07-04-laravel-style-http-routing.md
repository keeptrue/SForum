# Decision: Laravel-Style API Directory, Routing, And Providers

## Status

Accepted

## Context

The Fiber API scaffold has grown beyond a health endpoint. Identity work has
started, and route registration is already beginning to mix HTTP setup, module
dependencies, session wiring, and process bootstrap concerns.

SForum wants Laravel-like readability, but the backend is Go. The architecture
should borrow Laravel's proven mental model without hiding dependencies behind
runtime magic.

## Decision

Use a Laravel-style, Go-explicit backend composition structure:

- `cmd/api/main.go` stays as a process entrypoint. It should not know every
  domain module's routes or construction details.
- `bootstrap` is the application assembly layer, similar in role to Laravel's
  `bootstrap/app.php`: load config, prepare shared infrastructure, instantiate
  module providers, collect route providers, and return the runtime.
- `config` owns environment parsing and typed settings.
- `app/Http` acts like the HTTP kernel: Fiber config, global middleware,
  `/api/v1` grouping, health/system routes, centralized JSON error handling,
  and the route-provider interface.
- `app/Http/Controllers/<Area>` owns request/response DTOs, route
  declarations, and thin controller methods.
- `app/Providers` owns module provider wiring. Providers build stores,
  services, policies, controllers, route providers, jobs, and seeds from shared
  dependencies.
- `app/Models/<Domain>` owns domain-facing Go packages: types, services,
  policies, repository interfaces, and persistence adapters. These are not
  Laravel Eloquent models.
- `app/Support/*` wraps external systems and reusable infrastructure clients.
- `database/*` owns migrations, handwritten SQL, and generated `sqlc` code.

Prefer an explicit ordered provider list over package-level auto-registration,
`init` functions, filesystem scanning, or a general-purpose dependency
injection container.

## Routing Rules

- Register public API routes under `/api/v1`.
- Keep health/system routes in `app/Http`, not in domain packages.
- Keep domain route groups in their owning controllers.
- Put middleware at the narrowest useful level: global, API group, or route
  group.
- Route files may reference handlers and middleware, but not database clients
  or low-level platform setup directly.
- Services and stores must not register routes.

## Why Not A Full DI Container Now

Mature Go DI tools such as Google Wire or Uber Dig can be considered later, but
they are not the first choice for the current codebase. The backend is still
small, and explicit constructors are easier to inspect, test, and refactor.

If provider wiring becomes repetitive enough to cause real maintenance pain,
revisit generated dependency wiring before adopting runtime container behavior.

## Consequences

- Route registration has one visible path from bootstrap to providers to HTTP
  controllers.
- Adding a module requires a deliberate provider entry, making new surface area
  easier to review.
- HTTP setup stays independent from domain implementation details.
- Controller route files stay close to handlers and policies, so endpoint
  behavior remains discoverable.
- The project avoids Laravel-style hidden magic while retaining the parts of
  Laravel's organization that make large applications easier to navigate.
- The API module path is `github.com/keeptrue/sforum/apps/api`.

## Follow-up

- Extend `bootstrap` and `app/Providers` with future module providers as forum,
  moderation, search, human verification, and jobs are implemented.
- Keep route declarations in `app/Http/Controllers/*/routes.go` once a
  controller has more than a few endpoints.
- Keep OpenAPI route paths aligned with the module route files.

## References

- Laravel routing documentation: https://laravel.com/docs/13.x/routing
- Laravel service providers documentation: https://laravel.com/docs/13.x/providers
- Laravel request lifecycle documentation: https://laravel.com/docs/13.x/lifecycle
- Laravel middleware documentation: https://laravel.com/docs/13.x/middleware
