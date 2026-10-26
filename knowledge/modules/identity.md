# Identity Module

## Purpose

Owns users, credentials, sessions, registration, login/logout, roles,
permissions, human-verification requirements for identity flows, and policy
helpers.

## Current Status

Initial identity foundation is implemented.

- **后台新增用户（2026-10-27）：** `POST /api/v1/users` 由 `user.manage` 授权，
  在单事务内创建用户、Argon2id 凭据、角色与 `user.admin_create` 审计。`password`
  与 `generatePassword` 二选一：自设密码走站点策略校验；随机密码由
  `GenerateCompliantPassword` 生成，只在 201 响应的 `generatedPassword` 出现一次，
  数据库只存哈希。角色是精确集合，空集合回落 `member`；授予 `super_admin` 仍要求
  操作者是超管。管理员创建不受开放注册开关影响，也不触发 `user.before_register`，
  但仍执行 host 用户名/邮箱/密码策略，并在提交后发出 `user.registered`
  （actor 为管理员）。实现放在 `AdminUserCreator` 聚焦协作者而非 `Service`，避免
  继续增长 God-object。后台 `/control-panel/users` 标题行新增「新增用户」按钮与
  `SFAdminUserCreateDialog`（两种密码模式、用户组多选、邮箱已验证勾选、字段级 422），
  创建成功后回到第 1 页并高亮新行；用户预览 inspector 同时抽成
  `SFAdminUserPreviewModal`，`users.vue` 架构 baseline 由 1516 降到 1138。
  V3 目录新增 `core.route.identity.create_user` 与两个 admin 组件稳定身份。

- **App / 机器客户端单请求登录换令牌（2026-10-02）：** `POST /auth/login` 新增可选
  `issueApiToken`（形状与 `POST /auth/tokens` 完全一致：`name` 必填、`scopes`
  显式且必须是当前账号权限子集、`expiresAt` 可选）。携带时在同一次请求内签发
  一枚 PAT，`data` 变为 `LoginIssuedToken { user, apiToken }`，明文 `token` 只在
  该响应出现一次；未携带时 `data` 仍是 `CurrentUser`，浏览器契约不变。请求形状
  错误（`api_token.name_required` / `api_token.scopes_required` /
  `api_token.invalid`）在会话签发前返回 422，不留半成功状态。令牌签发复用
  `APITokens.Create`，因此 scopes ∩ 当前权限收窄、`super_admin` 剥离、审计与
  过期规则与管理端点一致。令牌管理（列出/创建/轮换/吊销）仍要求 cookie 会话；
  App 登出目前只能清除本地令牌，服务端自吊销需要把 bearer 令牌身份加入
  `routes.DispatchRequest` 并放宽已评审的 guard 姿态，本轮未做。
  决策：`decisions/2026-10-02-app-client-login-token.md`。

- **邮箱验证 Page Registry 合同（2026-08-02）：** `/email-verification` 现在以
  `auth.email_verification` 纳入公共 Page Registry，并使用
  `sforum.page.email_verification@1` ViewModel 与
  `identity.component.email_verification` Host 岛。Core 路由壳只负责登录态与
  `SFPageOutlet`，默认主题提供对应的精确制品模板；发送、确认以及后台验证状态变更
  也已进入 V3 Route Catalog。后台状态变更继续由 Host Guard 权威要求
  `user.manage`、保护 `super_admin` 目标并严格校验请求体。该合同修复了公共 Nuxt
  页面目录完整性 CI，避免邮箱验证页绕过主题替换和稳定身份目录。

- **邮箱验证闭环（2026-08-01）：** 密码注册账号默认未验证，但注册成功不会自动
  投递验证邮件；用户必须在邮箱验证等待页主动请求发送。启用 ALTCHA 与
  `human_verification.scenarios.email_verification` 时，等待页必须先完成人机验证，
  验证通过后发送按钮才可用，且 API 会对同一策略做权威校验。验证邮件通过 Mail
  Outbox/River 投递；令牌只保存哈希、24 小时过期、单次消费，
  且绑定注册邮箱，修改邮箱会清空验证状态并使旧链接失效。OAuth 只有在提供方明确
  返回 `emailVerified=true` 时才直接标记已验证。API 提供
  `POST /auth/email-verification/request`、`GET/POST /auth/email-verification/confirm`，
  当前用户响应包含 `emailVerified`。启用
  `identity.registration.require_email_verification` 与
  `identity.registration.block_posting_until_verified` 后，API 权威阻止未验证用户
  创建主题、评论和上传附件；同时前端全局路由守卫会将任何已登录但未验证的用户从
  非后台页面送到认证布局下的邮箱验证等待页，验证页自身与自定义后台前缀下的页面
  豁免。跳转保留安全的原始回跳目标，且公共页面导航会刷新当前会话，使管理员远程
  重置验证状态后在用户下次切页时立即生效。等待页明确展示尚未发送/已发送状态，
  支持检查状态与重新发送；每次重新发送在策略开启时都需要新的验证码。用户只能
  点击邮件中的一次性链接完成验证，不提供手动验证码输入；
  同一浏览器点击链接后回到等待页成功状态，未登录浏览器则回到登录页。后台用户管理可通过
  `PUT /users/{userID}/email-verification` 手动标记已验证或重置为未验证；操作需要
  `user.manage`，非超级管理员不能修改 `super_admin` 用户。两种操作都会作废该用户
  所有未使用验证链接，并分别写入 `identity.email_verification.admin_verify` 或
  `identity.email_verification.admin_reset` 审计事件。用户列表与详情均返回
  `emailVerified`，后台面板展示当前状态并在确认后执行变更。
  迁移为 `202608010001_identity_email_verification.sql`，老用户按 `created_at` 一次性
  视为已验证以保持兼容。

- **OAuth Provider Core prerequisites (2026-08-01):** Core now exposes a
  dedicated `account-settings` Navigation Registry kind and private
  `GET /site/account-navigation` projection. Manifest V3 contributions can
  declare authenticated visibility, permission keys, and Host-owned retained
  resource keys; the browser receives only redacted labels and safe
  `/settings/*` or `/extensions/*` paths. Disabled/inert exact runtimes are
  omitted by the lifecycle-aware composer. The independent
  `Support/IdentityDelegation` contract issues plugin-scoped opaque subjects
  with allowlisted profile/email/auth_time claims, and
  `Support/ConsentBridge` binds one-use consent transactions to actor, session
  fingerprint, exact artifact, CSRF, TTL, and optional recent-auth. These are
  generic Host contracts; no OAuth provider models, tokens, client records, or
  plugin database access were added to Core.

- Attachment upload eligibility continues to use the existing
  `attachment.upload` effective permission, including role grants and direct
  user allow/deny overrides. Attachment-domain size policies are deliberately
  separate: they cannot grant upload authority, and editing the RBAC side still
  requires `role.manage` or `user.permission_override`. The default member seed
  now explicitly includes the upload permission already granted by the original
  attachment migration.
- Personal appearance is an authenticated self-service preference at
  `/settings/appearance`. `CurrentUser.appearance` is nullable: a missing
  `user_appearance_preferences` row means the account inherits the current
  operator-configured accent and daytime background. `PUT /auth/appearance`
  saves a validated override; `DELETE /auth/appearance` restores inheritance.
  The page keeps edits in memory for immediate preview and persists them only
  after the explicit save action. Normal documents and theme-defined system
  error documents consume the same effective appearance precedence, including
  authenticated hard-refresh errors.
- Account settings now split login methods, local password, device sessions,
  and personal access tokens into independent `/settings/*` routes. Personal
  access tokens live at `/settings/tokens`, use separate Create/Manage tabs,
  preset scope sets plus checkbox selection (no free-text scope entry), and
  still rely on the Host API to enforce
  `current actor permissions ∩ token scopes`. The shared tab track is
  non-shrinking, so the long Create panel cannot collapse or clip its labels in
  the desktop scroll column; authenticated desktop and 390x844 Browser QA pass.
- Password recovery now has a production dual-column Host flow shared by
  `/forgot-password` and `/reset-password`. Request initiation remains
  non-enumerating, requires the `password_reset` ALTCHA purpose by default
  once ALTCHA is configured (the per-scenario admin toggle remains available),
  and masks the submitted email. Password-recovery and email-verification
  manual sends share a server-authoritative runtime resend policy managed from
  Site Settings → Account security: minimum interval, rolling window, per-target
  maximum, and per-IP maximum default to 30 seconds, 60 minutes, 3, and 10.
  Success and `429` responses expose `retryAfterSeconds` plus `retryAt`; both
  public flows render the server time rather than a fixed client cooldown.
  Unknown and known recovery emails enter the same limiter before lookup and
  keep identical responses. Confirmation uses the runtime password policy, rejects
  missing/invalid tokens explicitly, and exposes completed, mismatch, and
  password-visibility states without changing the existing API contract.
- Login, registration, and both password-recovery routes share `SFAuthShell`.
  Its header utilities now include the same language menu as the public
  navbar, so guests change locale locally while signed-in users persist their
  preference through the existing identity locale flow.
- Current-user locale validation and persistence are owned by the focused
  `LocalePreferenceService`; the aggregate Identity `Service` no longer owns
  that mutation. The controller receives the capability explicitly and tests
  invalid, unavailable, and successful persistence paths.
- Extension permission role suggestions preserve immutable review history.
  Approval requires the exact active plugin artifact, live permission
  declaration/catalog, enabled target role, `role.manage`, CAS, grant evidence,
  and audit. Rejection remains actor-bound and audited but does not consume
  extension authority, so an authorized operator can close a pending suggestion
  after its artifact is disabled, uninstalled, missing, or superseded.
- GitHub social login V1 is **active; R1-R7 remediation complete, independent re-review requested**
  (2026-07-27). One protected built-in plugin, `sforum.auth-github`, is
  implemented; a fresh independent review remains before release
  acceptance. T1A-T7
  delivered the Host foundation includes Core-owned subject HMAC, Redis
  callback transactions +
  reserved Core GET callback, opaque registration tickets, atomic CAS
  activation catalog (defaults off), credential-less users, transactional
  external registration, external login/link/unlink orchestration,
  session-bound recent-auth, password setup upsert, `identity.provider.manage`,
  modular OpenAPI, Core Route Catalog entries with callback/session closed to
  Route Registry replacement, start/callback IP rate limits, lifecycle/security
  matrix tests, and bilingual operator docs.
  **Unlinked login choice continuation (2026-07-30):** a successful provider
  `login.complete` with no local link now enters the Page Registry surface
  `/auth/continue`. One opaque, one-use ticket offers independently authorized
  choices: local login followed by automatic binding, or the existing external
  registration flow followed by automatic binding. Existing-account binding
  reuses `ExternalIdentityLinkStore`, records the provider assertion truthfully
  as `login.complete`, and rechecks current actor, session-bound recent auth,
  provider login/link activation, exact live artifact, and subject uniqueness.
  The login page excludes the source provider during continuation to prevent a
  loop. Registration still uses `POST /auth/external-registration` and its
  authoritative policy, field checks, user/default-role/link/audit transaction.
  Closing site registration suppresses only registration, not existing-account
  binding. The callback and ticket are bound to a Host HttpOnly, SameSite=Lax
  browser secret so a copied ticket cannot cause account-binding CSRF. Provider
  email is never used to match or bind an existing account.
  **T1A security fixes:** `AuthProviderFlow.Complete` is assertion-only (no
  link write); public `POST …/complete` for login/registration/link returns
  `410 auth.provider_callback_required`; Host callback re-resolves live
  Registry provider and compares provider/operation/owner extension/version/
  package digest, re-checks activation before effects; link requires current
  session actor + recent-auth before complete and persist; Host PKCE verifier
  and absolute callback URL (runtime `site.url`, then trusted `APP_URL`
  fallback; production HTTPS) pass into complete; `redirectHint` validated
  before store/plugin; registration
  continuation is fixed `/register` + opaque ticket + independent safe
  redirect.
  **T1B secrets/stores:** `IDENTITY_SUBJECT_HMAC_SECRET` on `config.Config`;
  production validation via real `APP_ENV=production` (missing/weak/dev-default
  fail startup); bootstrap injects stable digest key (no process-random);
  development uses stable configurable default; in-memory callback/ticket
  stores are mutex-safe with Redis-aligned TTL clamp; Redis and memory store
  under `sha256(opaque-token)` keys with atomic one-use consume and used-
  tombstone replay detection; registration tickets require CreatedAt/ExpiresAt
  and operation/provider/artifact binding on Save/Consume.
  **T1C activation/catalog/probe:** atomic optimistic concurrency
  (`FOR UPDATE` + `WHERE revision = expected` + RowsAffected;
  `ErrProviderActivationNoMutation`); Host-derived ownership/artifact via
  `PrepareActivationInput` (browser ownership rejected; unsupported ops
  rejected); effective availability binds live Registry digest +
  RuntimeInstanceID + supported ops + Safe Mode; public
  `GET /auth/providers` returns only effectively available providers and fail
  closes on Host-state lookup errors; admin probe reports
  `probe_pending`/`probe_unavailable` with `ok=false` (never persists
  `ok=true` before real probe RPC); actor-bound audit for activation update/
  reset/probe.
  **T1D recent-auth/unlink/password/registration:** recent-auth is bound to
  `(user_id, session_fingerprint)` where fingerprint is non-reversible SID
  SHA-256; marked after successful password and external login; cross-session
  isolation tested. Unlink loads target link, verifies actor ownership +
  active + expected revision, enforces last-login-method inside the same
  transaction as the unlink mutation, and uses idempotency keys scoped to
  user/link/revision/request (never client IP). `user_credentials.password_hash`
  stays NOT NULL; external-only users have no credential row.
  `POST /api/v1/auth/password` (recent-auth) creates/updates the credential;
  password reset confirm and admin set-password upsert when absent.
  External registration reuses authoritative username/email/reserved-name/
  hooks/registration-mode validators and human verification; editable fields
  are validated before opaque ticket consume; default-role assignment must
  affect exactly one role or the TX rolls back; authenticated users reload
  through canonical `GetCurrentUser` before session issue. Zero-user
  bootstrap, non-enumerating email, and atomic user/role/link boundaries
  preserved.
  **T1E contracts/routes/tests:** modular OpenAPI for callback,
  external-registration, password, external-identities, admin providers
  (security + error schemas); Core Route Catalog declares reserved callback and
  documents non-replaceability; controller HTTP allowed/denied + replay/
  exact-artifact + redaction; model lifecycle (Safe Mode, disable, artifact
  upgrade, revoke, expiry, actor mismatch, zero-write, unlink race) and
  meaningful two-provider login/link execution; postgres transition/rollback
  tests (skip without `SFORUM_TEST_DATABASE_URL`); M0 ADR corrected for Host
  ownership of state/PKCE/callback URL/transaction + additive schemas.
  **T2 / M2A (2026-07-27):** protected built-in package at
  `extensions/builtin/plugins/sforum-auth-github` (`sforum.auth-github` /
  provider `sforum.auth-github.auth`). Manifest V3 identity operations for
  login/registration/link start+complete; settings `client_id` + SecretStore
  `client_secret`; protocol uses Host-injected state/PKCE/callback URL, returns
  raw `providerSubject` (numeric GitHub id) without digest; fake GitHub server
  + protocol unit tests; truthful bounded probe;
  `scripts/build-builtin-plugins.sh` includes the package.
  **T3 / M2B (2026-07-27):** SyncBuiltins exact-artifact staging proof (immutable
  snapshot under EXTENSION_ROOT, no Host activation / public catalog exposure);
  Dockerfile + `build-builtin-plugins.sh` packaging; Protocol V2 headless E2E
  through real plugin subprocess + local fake GitHub into Host
  login/registration-ticket/link session effects. **M2 exit complete.**
  **T4 / M3 (2026-07-27):** Host admin aggregate
  (`GET /admin/identity/providers`) exposes
  discovered/trusted/enabled/configured/probed/publiclyActivated, absolute
  `callbackUrl` from effective `site.url` with `APP_URL` fallback, and
  `settingsPath`. Browser-facing OAuth
  callbacks use `/auth/providers/{providerId}/callback`; the Web Host bridges
  them to the reserved `/api/v1` Core handler so provider configuration does
  not expose the API namespace;
  `IsProviderConfigured` wired from extension settings; `identity.provider.manage`
  may read/write auth-plugin settings (mail-style); Login Methods page
  `/admin/settings/login-methods` embeds `SFExtensionSettingsRenderer` with
  CAS toggles, callback copy, truthful probe, restore defaults (secrets
  preserved), zh-CN/en-US, operator role template alignment. The provider
  tabs reuse the admin settings button-tab geometry; the former v3-style
  `UTabs` contract rendered an empty track, while its full-width Nuxt UI 4
  replacement did not match adjacent admin settings pages. Settings callouts
  support validated external help links; the GitHub plugin supplies its
  official OAuth App application URL and complete credential/activation steps.
  **M3 exit complete.**
  **T5 / M4A (2026-07-27):** SSR-safe `useAuthProviders` reads Host public
  catalog only; login/register Host islands show provider buttons solely when
  `activatedOperations` includes login/registration; vendor `label`/`icon`
  declared on plugin Identity provider (LocalizedText + icon), mapped into
  Registry publication and resolved by Accept-Language on
  `GET /auth/providers`; Core web i18n keeps only generic shell templates and
  Host stable `ext_auth` reasons (no GitHub brand strings); opaque ticket
  continuation at `/register?ticket=` posts `POST /auth/external-registration`
  without password; guest middleware preserves success Toast across auth bounce.
  **T6 / M4B (2026-07-27):** account security (`/settings/security`) shows
  redacted external identities via `GET /auth/external-identities`; link entry
  only when Host `activatedOperations` includes `link` and session is available;
  unlink + last-login-method + session-bound recent-auth UX; inert status when
  provider disabled; at that milestone, external-only local password setup used
  `POST /auth/password` from the account-security surface. Current account
  settings have since split local password onto `/settings/password` while
  retaining the same Host API contract. Since 2026-10-17 local password lives at
  `/settings/security/password` inside the consolidated account-security subtree
  (same Host API contract, same page ID). Catalog label/icon only (no Core GitHub
  brand).
  **M4B exit complete (full M4).** **T7 / M5 (2026-07-27):** lifecycle matrix
  (restart HMAC stability, disable/uninstall/Safe Mode/ForceDrain, staged
  upgrade + new-digest activation + rollback, trust revoke, mid-flow artifact
  change); security matrix (replay/expiry/cross-provider/op/actor, CAS,
  registration ticket one-use, subject isolation, unlink race, non-enumerating
  unlinked login); Host start/callback IP rate limits (Redis/memory);
  redaction HTTP checks; Identity Extension Surface Matrix updated (closed
  callback/session surfaces documented); bilingual operator docs
  `docs/zh-CN|en-US/usage/github-login.md` + author notes. Independent review
  rejected closure. **T8A (2026-07-27):** `CompleteRegistration` re-checks
  Host registration activation and live Registry exact contribution
  (provider/owner/version/digest/contract) before any account effect; re-reads
  authoritative registration policy inside the user/role/link TX; writes
  `auth.external_register.success` on the same TX (alongside existing
  `identity.external_link.*` audit); emits `user.registered` observe exactly
  once after commit. Focused PG tests call `CompleteRegistration` for
  ticket-after-disable, artifact upgrade, policy-close race, rollback, event
  once, audit, and zero-write denial paths.
  **T8B (2026-07-27):** versioned `auth:provider.probe` runtime operation
  (manifest JSON Schema + Identity Registry + identity.runtime@1 allowlists);
  GitHub plugin invokes bounded `GitHubOAuth.Probe` with deadline and redacted
  reason/message; Host `AuthProviderFlow.Probe` + admin
  `POST …/providers/{id}/probe` persists real ok/reason (`probe_pending` is not
  a product implementation); admin directory merges Host extension/package
  catalog discovery with live Registry executable authority and activation
  state (pre-enable discovered, disabled/drifted inspectable; trust/enable
  authority unchanged); admin Login Methods consumes Host `label`/`icon` only
  (no Core github id substring branches). Focused model/controller and
  happy-dom rendered interaction tests cover discovered/trusted/enabled/
  configured/probed/activated/disabled/drifted/Safe Mode/reset + second fake
  provider.
  **T8C (2026-07-27):** production ignores fake-GitHub endpoint overrides
  (Host strips `SFORUM_AUTH_GITHUB_*_URL` when `APP_ENV=production`; plugin
  also refuses overrides in production so OAuth material only reaches fixed
  GitHub.com endpoints); Redis start/callback rate limit uses atomic Lua
  `INCR`+`PEXPIRE` with no-TTL heal and fail-open + Del on script error;
  auth start fail-closes unless external-auth service, activation store,
  callback transaction store, and provider catalog are all wired; migration
  `057` no longer mutates `user_credentials.password_hash` and Down preserves
  the `NOT NULL` invariant from `055`/`0001`. Focused rate-limit, controller
  wiring, protocol-env, migration, and plugin config tests. T8D release
  evidence was superseded by the R1-R7 remediation packet. It retains hard 429
  HTTP assertions, real runtime/browser evidence, and migration 058 to quarantine
  stale unaudited enabled built-ins before they reach the Identity Registry.
  **R4 remediation (2026-07-27):** the protected GitHub reference now declares
  Lifecycle V2 and implements a no-side-effect lifecycle stream. A real
  PostgreSQL production lifecycle fixture starts from normal exact-artifact
  enable, then disables an auth-shaped provider through `DisableWithInput`; it
  proves runtime stop plus live and durable Identity Registry retirement.
  **R5 remediation (2026-07-27):** migration 058 is now deliberately narrow:
  it quarantines an enabled protected built-in only when its current durable
  root, exact successful lifecycle activation, and enable audit evidence are
  all absent. Partial/corrupt durable history alone remains fail-closed but is
  not reclassified as stale operator state.
  See `reports/2026-07-27-github-social-login-t8d-requirements-matrix.md` and
  `sessions/2026-07-27-github-social-login-final-review-handoff.md`. Authoritative
  checklist: `plans/2026-07-27-github-social-login-builtin-plugin.md`;
  product boundaries: `decisions/2026-07-27-github-social-login-builtin-v1.md`;
  M0 freeze: `decisions/2026-07-27-github-social-login-m0-contract-freeze.md`.
  **R1-R7 remediation complete (2026-07-27):** the isolated runtime packet
  hard-asserted readiness, password fallback, configure/enable/probe/activate,
  login, explicit registration, link/unlink/password setup, callback replay,
  rate limit, Safe Mode, artifact drift, real disable, and restore. Its final
  public catalog contains the exact restored provider; the request is now
  **整改完成，等待独立复审**, not program closure. See
  `reports/2026-07-27-external-auth-r1-r7-requirements-evidence-matrix.md`.
  **Mobile callback repair (2026-08-09):** browser binding cookies use
  `SameSite=None; Secure` for HTTPS production/staging OAuth redirect chains,
  while HTTP development remains `SameSite=Lax`; the binding remains
  HttpOnly, short-lived, random, and server-digest validated. Callback reasons
  now render persistent errors on arbitrary return pages through the root
  surface, keep `/login` and `/register` errors inline, and auto-dismiss only
  non-error feedback. This fixes the silent homepage failure caused by SSR
  consuming the reason before the root page rendered an alert. Real provider
  mobile/browser-account-switch E2E still requires operator credentials.
  **Restart repair (2026-07-28):** the admin restart action now uses the
  Host-owned restart endpoint instead of reusing enable. The old GitHub
  Identity Registry publication is tombstoned before the runtime is stopped,
  then the exact Lifecycle V2 artifact is enabled and republished. Runtime
  evidence on the development database recorded successful disable/enable
  operations, `extension.restart` audit, and active Identity Registry revision
  7 for exact version id `15357`; the previous
  `extension lifecycle registry publication exact fence conflict` no longer
  occurs. **Interrupted disable recovery (2026-07-29):** durable Identity
  publication now shares the aggregate registry phase transaction. Startup may
  append a compensating active revision for an exact tombstoned graph only when
  the enabled artifact and latest failed, uncommitted disable ledger prove that
  state and registry publication both returned to source. Artifact drift,
  committed deactivation, incomplete tips, missing audit evidence, and older
  non-latest operations remain closed. The affected development database
  started successfully through `./scripts/api-dev.sh` after appending the
  compensating active revision. Development operations `4041`-`4045` then
  verified disable, enable, staged restart, and settings-triggered restart;
  all committed their target Registry and extension-state publications, with
  durable Identity active at revision 14 for exact version id `18215`.
  **Evidence output hardening (2026-07-29):** the isolated external-auth
  packet keeps credential login as an assertion-only operation with no returned
  evidence and records only a literal verified marker after success; credential
  setup records explicit status and empty-response proof. Its printable schema
  rejects password-named keys and checks the actual submitted secret values, so
  credentials and full credential-endpoint responses cannot become part of the
  persisted or logged evidence contract. The exact validated evidence document
  is then written and SHA-256 checksummed for reproducibility; this digest is
  not used for credential derivation, verification, or storage.

- PostgreSQL migrations create users, credentials, roles, permissions, role
  assignments, and audit events.
- Seed data includes `super_admin`, the default `member` role, built-in role
  templates (`moderator`, `operator`, `tech_admin`), and the permission catalog.
- Registration is operator-configurable: `identity.registration.enabled` plus
  `identity.registration.mode` (`open|invite|approval|closed`). Non-`open`
  modes currently close public self-registration (invite/approval product flows
  are deferred). While the site has zero users, registration is forced open so
  bootstrap cannot lock out the first install. The first registered user becomes
  the protected initial `super_admin`; later registrations receive `member`.
- Browser sessions are backed by Redis through Fiber sessions.
- API endpoints exist for registration, login, logout, current session, role
  listing, role creation/update/delete, role permission replacement, permission
  catalog/matrix reads, admin user listing/detail, admin user account/profile
  update (`PATCH /users/{userID}`), user role replacement, and user direct
  permission override replacement.
- CLI `go run ./cmd/sforum users:reset-password` provides an interactive
  out-of-band operator reset: email lookup, user-summary confirmation, hidden
  password entry/confirmation, site password-policy validation, credential
  upsert, token-version bump, and active-session revocation.
- Admin user list is paged (default 20 per page, max 100) and server-sorted
  through the OpenAPI `sortBy` / `sortOrder` contract. The whitelist supports
  registration time, update time, username, display name, email, and account
  status; omitted or invalid values resolve to newest registration first, and
  every order uses `id` as a deterministic pagination tiebreaker. Admin user detail
  includes `createdAt`/`updatedAt`, public `profile` (bio/signature/location/
  websiteUrl), effective permissions, and permission overrides. Detail also
  carries admin-only inspection fields for the users-page preview modal:
  `activity` (topic/comment/session counts, last login IP/UA), `sessions`
  (full `ipAddress` + raw `userAgent`, not the self-service masked prefix only),
  `recentAuthEvents` (login/register audit IP/UA), and `passwordChangedAt`.
  Self-service device lists still expose only masked `ipPrefix`. `PATCH` accepts
  partial account + profile fields; `user.manage` required, `banned` also needs
  `user.ban`; operators cannot change their own status; non-super-admin cannot
  edit super_admin accounts; initial super admin cannot be disabled/banned.
- The permission catalog includes `database.manage` for the read-only admin
  database table manager. `super_admin` receives it by migration and policy as
  part of the protected all-permissions role.
- Forum content revisions V1 M2 added `topic.revision.view_any` and
  `post.revision.view_any` to Go seed constants, permission catalog migration
  `202607220053`, frontend permission labels, and the moderator role template.
  Defaults grant them only to `super_admin` and built-in `moderator`; not to
  `member`, `operator`, or `tech_admin`. Plugin install/enable paths must not
  grant Host permissions. See
  `decisions/2026-07-22-forum-content-revisions-ledger.md`.
- V1 restore additionally requires the matching `topic.edit_any` or
  `post.edit_any`; history-view alone is inspection-only. `super_admin` retains
  the protected policy bypass and is the only actor allowed to redact a
  non-current revision payload.
- The permission catalog includes `tag.manage` for forum tag creation,
  approval, disabling, and policy management. Existing deployments receive it
  through the forum taxonomy migration, and `super_admin` receives it by
  default.
- Current core catalog (authoritative in
  `apps/api/app/Models/Identity/seeds.go`) covers admin access, identity,
  forum content actions, moderation policy/review, fine-grained settings,
  SEO/database, attachments, extensions, search, and jobs. Phase 1 splits
  high-risk parents into grantable children (`settings.*`, `forum.settings`,
  `user.view` / `user.permission_override`, `extension.view/plugin/theme/release`)
  and separates author topic edit/delete (`topic.edit_own` /
  `topic.delete_own`) from reply own keys. Legacy parents remain for upgrade
  compatibility via `permission_compat.go` expansion. Frontend labels live under
  `admin.permissionCatalog.*`; `tests/validate-identity-ui.js` requires every
  seed key and module to have zh-CN/en-US text.
- Decision: `knowledge/decisions/2026-07-12-fine-grained-permissions-phase1.md`.
- Built-in role templates (Phase 1 follow-up): system roles `moderator`,
  `operator`, and `tech_admin` are seeded by migration
  `202607120002_builtin_role_templates.sql`. Authoritative permission packs live
  in `SeedRoleTemplates` / `SeedMemberPermissions` in
  `apps/api/app/Models/Identity/seeds.go`. Templates are not deletable; their
  permission sets remain editable (unlike `super_admin`). Admin roles UI can
  apply the same packs when creating custom groups
  (`apps/web/app/config/roleTemplates.ts`, `admin.roleCatalog.*` i18n).
  Decision: `knowledge/decisions/2026-07-12-builtin-role-templates.md`.
- API exposes `/api/v1/auth/registration-status` for registration availability
  only: `nextUserIsInitialSuperAdmin` is always `false` on that public endpoint
  so the bootstrap window stays hidden, while `registrationEnabled` carries the
  bootstrap override. The "first registration becomes the initial `super_admin`"
  fact is told only to the registrant who actually receives it, through the
  `POST /auth/register` response.
- Registration human verification is supported but disabled by default. When
  the admin CAPTCHA settings `human_verification.provider=altcha` and
  `human_verification.scenarios.register=enabled` are enabled,
  `/api/v1/human-verification/challenge?purpose=register` returns an ALTCHA v2
  challenge, and `/api/v1/auth/register` verifies the submitted
  `humanVerification` token only after editable registration fields and
  username/email conflicts pass validation.
- Registration validation now returns actionable field-level API errors under
  `data.fields` for `username`, `email`, `password`, and `humanVerification`.
  The stable reason for editable registration fields is
  `auth.register_invalid`; login failures still use one generic
  `auth.invalid_credentials` reason to avoid account enumeration.
- If account creation succeeds but the browser session cannot be saved,
  registration returns `auth.session_unavailable` with the localized message
  "账号已创建，但自动登录失败，请直接登录。"; the user should log in rather than retry
  registration or human verification.
- Browser auth remains Redis-backed server session, not JWT-first. Sessions use
  HTTP-only SameSite=Lax cookies, secure cookies in production, 30-day idle
  timeout, 180-day absolute timeout, and 24-hour session-id renewal by default.
- `CurrentUser` responses now include `avatar` as the shared `AvatarView`
  contract. Navbar/admin chrome should render the session user's avatar from
  this field via `SFAvatar`.
- Nuxt treats only 401/`auth.required` from `/auth/session` as logged out.
  Transient API restart, timeout, or gateway failures keep the existing
  frontend user state and surface auth service unavailability instead of
  redirecting to login.
- Successful registration auto-login and every successful login write
  `audit_events` records with user id, action, IP address, User-Agent, and a
  salted session-id hash. The first version stores this for security/admin
  review and does not expose it to users yet.
- Login now treats only an explicit missing credential as
  `auth.invalid_credentials`; internal credential-loading errors, such as a
  missing permission table after code/schema drift, bubble up instead of being
  misreported as a wrong password.
- Password policy is now runtime configurable through public
  `identity.password.*` options. Username length/charset/reserved names use
  `identity.username.*`. Login consecutive failures can lock via Redis using
  `identity.login.max_failures` and `identity.login.lockout_minutes`.
  The pair and IP dimensions retain temporary hard locks. Distributed failures
  against one account only set a short-lived verification marker at the higher
  account threshold; they never hard-lock every source. After the password is
  verified, the API requires the `login_risk` challenge before clearing that
  account risk. Redis keys contain only hashed login/IP identifiers and Redis
  failures remain fail-open.
  Registration and password reset confirmation
  share the same backend `PasswordPolicy` validator; password hashing only owns
  Argon2id hashing and no longer hard-codes product policy. Stored Argon2id
  hashes are parsed with unsigned width checks and current-cost ceilings before
  derivation; salt and key lengths must match the Host-generated format, so a
  malformed database value cannot wrap parameters or request unbounded work.
- Nuxt has login/register pages, an admin route middleware, an admin overview,
  user management, editable user-group management, and a permission matrix. The
  matrix is an audit/comparison view rather than the primary editor: it caps the
  default displayed user groups, supports search and explicit comparison
  selection, and can show only permissions that differ inside the current
  comparison scope.
- The admin roles screen now lists exact-artifact extension permission role
  suggestions and supports Host-owned approve/reject/apply decisions with
  optimistic revision checks. Installation and enable never grant a mapping.
  Decision refresh preserves unrelated unsaved role fields and merges only the
  exact newly approved permission into a dirty draft before a later save. The
  screen separates the user-group list and permission reviews into query-synced
  fixed tabs; review data loads only after its tab is first opened, while the
  route-owned toolbar follows the active workflow.
- Protected Nuxt routes preserve `to.fullPath` in the login `redirect` query.
  Auth return navigation accepts only validated local absolute paths, rejects
  external, protocol-relative, malformed, and login/register destinations, and
  resolves explicit redirect, an optional usable same-origin browser
  `document.referrer`, then localized home. Nuxt SPA navigation does not
  guarantee referrer updates, so explicit `redirect` is the reliable protected
  route restoration path.
  The tracked default-theme login/register pages opt into the host `guest`
  middleware, which refreshes unknown session state and returns authenticated
  visitors before page setup; successful login/registration updates session
  state and uses the same replace-style return navigation. Development themes
  inherit this entry behavior when their auth pages opt into `guest` middleware.
- Role/user-group creation and updates now trim role fields and reject blank
  keys or aliases. Role keys are limited to stable ASCII path-safe identifiers;
  the roles admin form shows visible field labels and blocks empty submissions
  before calling the API. Migration `202607060002_role_input_constraints`
  removes historical blank custom roles caused by the earlier missing
  validation and adds database non-blank checks.

  **R1 remediation (2026-07-27):** `ExternalAuthService.ValidateLoginEffect`
  centralizes live Provider Registry, exact owner/artifact/contract, operation,
  activation, and Safe Mode validation for login. The Core callback invokes it
  immediately after provider `complete`, then invokes it again inside the
  session-policy admitted Host effect after risk evaluation and immediately
  before `Begin`/`Save` session work. That second successful check is the
  documented effect linearization point; it is deliberately not described as
  impossible cross-store atomicity. A blocking risk-provider controller test
  proves that entering Safe Mode during the in-flight callback yields no login
  success redirect and no session cookie. The implementation is provider-ID
  generic and leaves password login unchanged.
  **R2 remediation (2026-07-27):** external registration now requires an
  uncached `Options.Service.RegistrationEnabledTx` read in the same `pgx.Tx`
  as user/default-role/link/audit writes. The Options Postgres store holds a
  dedicated advisory transaction lock for the registration enabled/mode keys;
  normal updates take the same lock, including when a row is absent. The old
  independently pooled/cached callback is retained only as a pre-transaction
  fast reject and cannot authorize the mutation. Isolated PostgreSQL tests
  cover a post-fast-check close with zero writes, bootstrap rejection, default
  role rollback, and reader/writer serialization.
  **R3 remediation (2026-07-27):** external-registration audit writes now keep
  only provider ID, owner extension ID, and correlation ID. Migration 059
  removes `ownerPackageDigest` and all prohibited subject/OAuth/secret keys
  only from existing `auth.external_register.success` metadata. It deliberately
  leaves unrelated audit events and immutable extension artifact history
  untouched; isolated PostgreSQL migration evidence verifies both boundaries.

## Architecture Decisions

- Use one `users` table for public users, moderators, and administrators.
- The first registered user becomes the initial `super_admin`.
- The initial super administrator cannot be deleted, disabled, or stripped of
  the `super_admin` role.
- Registration remains open after bootstrapping.
- Later registrations receive the system `member` role by default.
- `member` can have a custom display alias, but its role key cannot change and
  it cannot be deleted while it is the default registration role.
- Built-in template roles (`moderator`, `operator`, `tech_admin`) are system and
  non-deletable; operators may still edit their permission checkboxes and
  display alias/description. `super_admin` remains permission-locked.
- Admin-managed custom roles are supported and can be presented as user groups.
- Effective permissions are the union of all enabled roles assigned to a user.
  For non-`super_admin` users this is now extended by direct user permission
  overrides: enabled role permissions plus direct allows minus direct denies.
- Start with database-backed RBAC and Go policy helpers; keep room to adopt
  Casbin if permissions become substantially more complex.
- Keep resource-scoped ACL out of the first admin permissions release. Forum
  category/topic scoped rules should be added only when concrete forum
  workflows require them.
- Keep human verification disabled by default; use ALTCHA as the first
  supported self-hosted provider for deployments that enable registration and
  password-reset checks.
- Keep login hardening off by default. `human_verification.scenarios.login_risk`
  gates three optional triggers (`human_verification.login_risk.always`,
  `.new_device`, `.privileged`); with the gate off, only the Redis failure-lockout
  step-up can still ask for verification. Decision:
  `decisions/2026-10-22-login-human-verification-triggers.md`.
- Never use the account-wide failure counter as a hard lock. Pair/IP limits own
  brute-force blocking; the account dimension owns step-up verification so a
  distributed attacker cannot deny all legitimate login sources.
- Store challenge replay protection and rate-limit state in Redis.
- Do not introduce access/refresh JWT for first-party browser forum sessions.
  If SForum later ships mobile apps or third-party API access, use short-lived
  access tokens and persisted rotating refresh tokens with reuse detection.

## Implemented Tables

- `users`
- `user_credentials`
- `roles`
- `permissions`
- `role_permissions`
- `user_roles`
- `user_permission_overrides`
- `audit_events`
- `user_sessions` — 活跃会话/登录设备目录。`sid` 是 server 生成的稳定 opaque 标识
  （非 cookie 凭证），`session_hash` 是 cookie session id 的 HMAC（仅审计关联）。不存
  raw session id / token。支撑「活跃设备列表 / 登录历史 / 下线单个 / 下线其他」。
  见 `decisions/2026-07-10-account-security-sessions.md`。

## Current Boundaries

- Fiber API owns registration, login/logout, session loading, permission
  checks, human-verification enforcement, protected-user invariants, and audit
  writes.
- Nuxt owns login/register pages, the first admin user-group UI shell, route
  guards, and localized permission-denied messages.
- Nuxt route guards are user-experience helpers only. API policy checks remain
  authoritative.
- Auth return navigation is frontend-only and does not add or change an API or
  permission boundary.
- External auth providers are additive Identity Registry contributions, not a
  singleton Provider Slot. Plugins may verify an external subject; Core retains
  user creation, links, risk/session policy, browser sessions, permissions, and
  audit authority. Installing or enabling a plugin must not expose a login
  method without a separate Host-owned activation.

## Permission-Aware Development Rules

- Treat authorization as part of feature design. Before adding a route,
  mutation, admin screen, data export, moderation action, background action, or
  setting update, identify the actor, action, protected resource, and required
  permission boundary.
- Prefer existing permission keys and policy helpers. Add a new permission only
  when it maps to a distinct admin-grantable capability, then update seed data,
  permission catalog text, API contracts when relevant, and frontend permission
  labels.
- Keep permission checks on the API side for every core-owned protected operation. Nuxt
  middleware, hidden menu items, disabled buttons, and localized denial messages
  are helpful UI affordances, not security boundaries. Under V3, an explicitly
  trusted replacement handler or custom guard owns its declared authorization
  contract and must ship allowed/denied tests and trust disclosure.
- Cover both allowed and denied paths in tests for unsafe endpoints and admin
  operations. Include direct user allow/deny behavior when a feature depends on
  effective permissions.
- Continue to preserve `super_admin` invariants: active super administrators
  pass all policy checks, and direct permission overrides cannot be edited for
  current `super_admin` users.

## Implementation Notes

- `apps/api/app/Models/Identity/service.go` owns registration, login,
  registration status, current-user loading, actor loading, role-management
  checks, permission catalog/matrix reads, admin user detail reads, user role
  replacement, user direct permission override replacement, and configurable
  password policy enforcement for registration.
- `apps/api/app/Models/Identity/admin_user_sort.go` owns admin user-list
  authorization, filter normalization, and the public sort whitelist/defaults;
  the PostgreSQL store maps only those normalized values to SQL expressions.
- `apps/api/app/Models/Identity/password.go` owns bounded Argon2id hashing and
  verification plus the shared `PasswordPolicy` model used before password
  creation/update.
- `apps/api/app/Models/Identity/policy.go` keeps permission checks small:
  `super_admin` receives all permissions while active, and other users rely on
  enabled role permissions plus user direct allows minus direct denies.
- `apps/api/app/Http/Controllers/Identity/controller.go` maps stable API error
  codes such as `auth.required`, `permission.denied`, and
  `role.default_role_locked`; permission management adds stable reasons such as
  `permission.invalid`, `permission.override_conflict`, `role.invalid`,
  `role.invalid_input`, and `user.super_admin_permissions_locked`.
  Registration field errors use backend-localized messages in `data.fields`.
- `apps/api/app/Support/AuthSession` owns authenticated browser session
  lifecycle: login session reset, current-user lookup, idle TTL refresh,
  periodic session-id renewal, logout destruction, and salted session-id hashes
  for audit correlation. Its failed-write cleanup clears the local payload
  before delete/save compensation; this is required with Fiber 3.4, which
  preserves session data when a storage delete fails.
- `apps/api/app/Support/HumanVerify` owns the provider boundary, ALTCHA v2
  challenge/verification adapter, Redis-backed replay/rate-limit store, and
  in-memory test/local store.
- If correct credentials return the generic login-failed message after identity
  or permissions work, check `goose_db_version` and PostgreSQL logs first. A
  local schema missing `202607050002_user_permission_overrides` caused
  permission loading during login to fail before password verification results
  could be surfaced accurately.
- `apps/api/app/Providers/identity.go` wires the identity store, service, and
  controller into the ordered route-provider list.
- 账号安全 / 登录设备管理：`user_sessions` 表（migration
  `202607100002_user_sessions.sql`）、`identity/sessions.go`（store SQL）、
  `identity/session_service.go`（列表/revoke/enforce max 领域逻辑）、
  `Support/AuthSession/manager.go`（`SessionStore` 接口 + `sid` payload + revoke
  校验 + `CurrentSID`）、`Support/UserAgent`（UA/IP 解析与脱敏）。前端
  `useAccountSecurityApi` composable + `settings/security/index.vue` 用户页
  （账号安全合并子树的默认 tab，见 `SFSettingsSecurityTabNav`） + admin
  settings accountSecurity tab 的 `identity.sessions.max_devices`。用户页登录历史默认
  显示，并通过 `/auth/sessions?includeHistory=true&page&perPage=10` 分页读取。
  决策见 `decisions/2026-07-10-account-security-sessions.md`。
- `apps/api/bootstrap/app.go` wires a runtime human-verification service that
  reads provider, ALTCHA secret, TTL, and cost from Options on each
  challenge/verify request. Environment values remain first-run fallbacks for
  seeding missing options.
- Login hardening is decided in `identity.Service.loginRequiresVerification`
  (`app/Models/Identity/login_verification.go`): explicit PAT exemption → Redis
  failure lockout (independent of Options) → `login_risk` master gate → `always`
  → `new_device` (UA fingerprint via `HasKnownDevice`/`HasSessionFingerprint`) →
  `privileged` (`super_admin`, initial super admin, or `admin.access`). The check
  runs after password verification, so wrong passwords never consume a token.
  Options or fingerprint read failures fail open. `POST /auth/login` computes the
  fingerprint from `useragent.Parse(...).UserAgentRaw` — the same value sessions
  store — and exempts requests that also issue an API token (documented bypass).
  Only `always` is a public option, so the login island can render ALTCHA before
  the first 422; `new_device`/`privileged` stay server-driven to avoid account
  enumeration.
- The Host registration island
  (`apps/web/app/components/identity/SFRegisterFormPage.vue`, mounted by
  `pages/register.vue` through `SFPageOutlet page="auth.register"`)
  renders the ALTCHA widget client-side only when public option
  `human_verification.provider` is `altcha`, reads the public ALTCHA widget
  type/auto/display/worker/min-duration settings, and maps
  `human_verification.*`, `rate_limit.exceeded`, and `auth.session_unavailable`
  API error codes to localized messages. It also reads
  `/api/v1/auth/registration-status` for availability, announces
  `auth.initialSuperAdminGranted` after a registration whose response reports
  `isInitialSuperAdmin`, blocks repeated submit attempts while a request is in
  flight, and resets the ALTCHA widget after verification failures.
- The same response decides the post-registration landing: an
  `isInitialSuperAdmin` registration is sent straight to
  `useAdminRoutes().path('/')` (default `/control-panel`) through
  `returnFromAuth(landing)`, taking precedence over `?redirect=` and the
  referrer because a zero-user site has no configured plugin/OAuth return flow.
  Every other registration keeps the normal auth return. When email
  verification is required the detour still comes first, with `redirect`
  pointing at the admin panel so verification lands there.
- Registration builds and loads the returned current-user access inside the
  bootstrap transaction so response construction failures roll back account
  creation instead of leaving a created user behind a 500 response.
- Account security is one consolidated sidebar entry: `/settings/security`
  (device sessions + login history, default tab), `/settings/security/login-methods`
  (`forum.settings.login_methods`), and `/settings/security/password`
  (`forum.settings.password`) share `SFSettingsSecurityTabNav` route tabs and a
  single sidebar highlight (`active="security"`). Legacy flat paths 302-redirect
  in Nitro routeRules; external-identity callback redirects live at the new
  login-methods path. External identity linking uses `SFLinkedAccountsSection`
  with return path `/settings/security/login-methods`; personal access tokens
  remain on `/settings/tokens`.
- `contracts/openapi.yaml` documents the current auth and role endpoints.

## Open Questions

- Which exact username, email, and password rules should MVP registration use?
- What ALTCHA challenge expiration and work cost should be the production
  default?
- Which role-management screens are required in the first admin milestone?

## Next Steps

- Add CSRF protection for cookie-authenticated unsafe requests. *(Done —
  `decisions/2026-07-09-security-fixes.md`.)*
- Built-in role templates (`moderator` / `operator` / `tech_admin`) are
  implemented — see `decisions/2026-07-12-builtin-role-templates.md`. Deferred:
  category-scoped moderator ACL, optional restore-template-to-defaults action
  that hard-resets a template role's permission set.
- Tune production ALTCHA challenge cost, expiration, and per-IP limits after
  testing on expected low-end client devices.
- User-facing account security views for login history and active devices
  (revoke one / revoke others, max-device config) are now implemented — see
  `decisions/2026-07-10-account-security-sessions.md`. Follow-ups: consider
  caching `IsSessionRevoked` to reduce hot-path cost, and admin force-revoke
  of other users' devices behind a permission boundary.
- Add risk-based controls for new device/IP patterns, including optional
  reauthentication or human verification.
- Extend the same human-verification boundary to password-reset initiation and
  risk-based login/posting checks when those flows are implemented.
- Add account deletion/disable flows while preserving the initial
  `super_admin` invariant.
- Decide exact username, email, password, and email-verification policies.
