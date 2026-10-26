/**
 * Host-action vocabulary shared by manifest disclosure, the admin-page bridge,
 * and the confirmation dialog. Keeping the constants and predicates in one file
 * means a page cannot "declare" an action with one spelling and have the bridge
 * look for another.
 */

import { apiErrorFields, apiErrorReason } from '~/composables/useApiClient'
import { extensionAdminPages } from '~/utils/admin/adminExtensions'
import type { AdminExtension, AdminExtensionAdminPage } from '~/utils/admin/adminExtensions'
import { normalizeRemoteArchiveUrl, normalizeSha256 } from './extensionInstall'

export const ADMIN_HOST_ACTION_EXTENSION_INSTALL = 'extension.install'

/**
 * Superset of {@link ADMIN_HOST_ACTION_EXTENSION_INSTALL}: the Host installs the
 * package and then runs its own enable flow. Declaring it never bypasses a
 * gate — an executable backend still needs the exact-artifact confirmation.
 */
export const ADMIN_HOST_ACTION_EXTENSION_INSTALL_ENABLE = 'extension.install.enable'

/**
 * The host-mediated install is a synchronous download+stage. `useApiClient`
 * defaults POST to 30 s, which would abort a slow but healthy download, so the
 * single call that performs the install passes this budget explicitly.
 */
export const ADMIN_HOST_INSTALL_TIMEOUT_MS = 120000

/**
 * Stacking fact for the host confirmation dialog, declared here so the number has
 * one name: the admin shell's toasts sit at 100 and a plugin's own page overlay at
 * 40, so the host dialog must land strictly between them.
 *
 * This is a *statement of fact*, not a template input. Tailwind has no safelist in
 * this repo and cannot see interpolated candidates, so the component keeps the
 * literal `z-[80]`; a unit test keeps the literal and this constant in sync.
 * It cannot detect a plugin overlay that picks a number above 80 — that stays a
 * cross-repository convention, not something this constant can enforce.
 */
export const ADMIN_HOST_DIALOG_Z_INDEX = 80

export type AdminHostInstallType = 'plugin' | 'theme'

/** Which promise the caller is holding: stage only, or install then enable. */
export type AdminPageHostInstallMode = 'stage' | 'enable'

/** Host lifecycle call the activation step is allowed to run. */
export type AdminPageHostActivateAction = 'enable' | 'upgrade' | 'restart'

/** Bridge-side state machine for one host-mediated install. */
export type AdminPageInstallState =
  | 'idle'
  | 'confirming'
  | 'installing'
  | 'trust-review'
  | 'activating'
  | 'success'
  | 'error'
  | 'cancelled'

/** What the enable step reached; `staged` is an honest stop, not a success. */
export type AdminHostInstallEnableOutcome = 'enabled' | 'upgraded' | 'staged'

/** Which phase failed, so retry never re-downloads a package it already has. */
export type AdminHostInstallPhase = 'install' | 'activate'

/** Why the Host stopped after installing instead of enabling. */
export type AdminHostInstallStagedReason = 'stage' | 'theme' | 'super_admin' | 'declined'

export type AdminHostInstallRequest = {
  url: string
  sha256: string
  expectedExtensionId: string
  expectedType: AdminHostInstallType
  expectedVersion?: string
}

export type AdminHostInstallResult = {
  extensionId: string
  version: string
  type: AdminHostInstallType
  packageDigest: string
  upgraded: boolean
  requiresTrust: boolean
}

/** Same identity block as an install, plus what the enable step reached. */
export type AdminHostInstallEnableResult = AdminHostInstallResult & {
  outcome: AdminHostInstallEnableOutcome
}

/** Host actions a page declares, normalized to a deduplicated list. */
export function adminPageHostActions(page: Pick<AdminExtensionAdminPage, 'hostActions'> | null | undefined) {
  if (!page?.hostActions?.length) return [] as string[]
  const seen = new Set<string>()
  for (const action of page.hostActions) {
    const value = String(action || '').trim().toLowerCase()
    if (value) seen.add(value)
  }
  return [...seen]
}

/**
 * Host actions are only ever attached to `view: component` pages; the API
 * rejects any other combination, and the client mirrors that rule so a stale
 * bootstrap payload cannot inject the bridge on a plain about/settings page.
 */
export function adminPageDeclaresHostAction(
  page: Pick<AdminExtensionAdminPage, 'view' | 'hostActions'> | null | undefined,
  action: string
) {
  if (!page || page.view !== 'component') return false
  return adminPageHostActions(page).includes(action)
}

export function adminPageCanInstallExtensions(page: Pick<AdminExtensionAdminPage, 'view' | 'hostActions'> | null | undefined) {
  return adminPageDeclaresHostAction(page, ADMIN_HOST_ACTION_EXTENSION_INSTALL)
}

/**
 * Install-and-enable is declared separately from install-only: the bridge
 * injects one method per declared action, so a page cannot get the stronger
 * capability by accident.
 */
export function adminPageCanInstallAndEnable(page: Pick<AdminExtensionAdminPage, 'view' | 'hostActions'> | null | undefined) {
  return adminPageDeclaresHostAction(page, ADMIN_HOST_ACTION_EXTENSION_INSTALL_ENABLE)
}

/**
 * True when any admin page of this extension declares the install action.
 * Disclosure UI and the bridge both read the manifest through this helper, so
 * they cannot disagree about what a plugin can ask for.
 */
export function extensionCanRequestInstall(item: AdminExtension | null | undefined) {
  if (!item) return false
  return extensionAdminPages(item).some(page => adminPageCanInstallExtensions(page))
}

/**
 * True when any admin page can make the Host enable what it installed. This is
 * strictly more authority than install-only, so the disclosure notice has to
 * cover it separately.
 */
export function extensionCanRequestEnableInstall(item: AdminExtension | null | undefined) {
  if (!item) return false
  return extensionAdminPages(item).some(page => adminPageCanInstallAndEnable(page))
}

export type InstallRequestValidation =
  | { ok: true, request: AdminHostInstallRequest }
  | { ok: false, reason: 'url' | 'sha256' | 'extensionId' | 'type' | 'version' }

/**
 * Validates a plugin-supplied install request before the Host spends a network
 * round trip on it. The Host still re-validates and re-checks the manifest
 * identity server-side; this is a fast, honest rejection, not the authority.
 */
export function validateAdminHostInstallRequest(input: unknown): InstallRequestValidation {
  const value = (input || {}) as Partial<AdminHostInstallRequest>
  const url = normalizeRemoteArchiveUrl(String(value.url ?? ''))
  if (!url.ok) return { ok: false, reason: 'url' }

  const digest = normalizeSha256(String(value.sha256 ?? ''))
  // The bridge requires a pinned digest: an unpinned download cannot be tied to
  // the artifact the catalog advertised.
  if (!digest.ok || !digest.value) return { ok: false, reason: 'sha256' }

  const extensionId = String(value.expectedExtensionId ?? '').trim().toLowerCase()
  if (!/^[a-z0-9][a-z0-9._-]{1,80}$/.test(extensionId)) return { ok: false, reason: 'extensionId' }

  const expectedType = String(value.expectedType ?? '').trim().toLowerCase()
  if (expectedType !== 'plugin' && expectedType !== 'theme') return { ok: false, reason: 'type' }

  const version = String(value.expectedVersion ?? '').trim()
  if (version && !/^[0-9A-Za-z._+-]{1,64}$/.test(version)) return { ok: false, reason: 'version' }

  return {
    ok: true,
    request: {
      url: url.url,
      sha256: digest.value,
      expectedExtensionId: extensionId,
      expectedType,
      ...(version ? { expectedVersion: version } : {})
    }
  }
}

/** Stable bridge failure codes. A plugin can branch on `error.code` without
 * parsing text; the text is only for humans. */
export type AdminHostInstallFailureCode =
  | 'install_permission_denied'
  | 'install_cancelled'
  | 'install_in_progress'
  | 'install_invalid_request'
  | 'install_failed'

/**
 * `install_failed` covers two very different outcomes: nothing landed, or the
 * package is installed but the enable did not run. `phase` tells them apart so a
 * caller never offers a reinstall for a package that is already on disk.
 */
export class AdminHostInstallError extends Error {
  readonly code: AdminHostInstallFailureCode
  /** Present only on `install_failed`: which phase produced the failure. */
  readonly phase?: AdminHostInstallPhase

  constructor(code: AdminHostInstallFailureCode, message?: string, phase?: AdminHostInstallPhase) {
    super(message || code)
    this.name = 'AdminHostInstallError'
    this.code = code
    if (phase) this.phase = phase
  }
}

/** Audit label for a host-mediated install: `admin-page:<extension id>`. */
export function adminPageSourceLabel(extensionId: string) {
  const base = `admin-page:${String(extensionId || '').trim().toLowerCase()}`
  return base.length <= 64 ? base : base.slice(0, 64).replace(/[.:-]+$/, '')
}

/** Stable install failure reason → i18n key under `admin.extensionInstall.errors`. */
const INSTALL_ERROR_KEYS: Record<string, string> = {
  'extension.remote_url_invalid': 'remoteUrlInvalid',
  'extension.remote_fetch_failed': 'remoteFetchFailed',
  'extension.remote_archive_too_large': 'remoteArchiveTooLarge',
  'extension.remote_digest_mismatch': 'remoteDigestMismatch',
  'extension.remote_install_unavailable': 'installUnavailable',
  'extension.expected_mismatch': 'expectedMismatch',
  'extension.archive_invalid': 'archiveInvalid',
  'extension.manifest_invalid': 'manifestInvalid',
  'extension.platform_incompatible': 'platformIncompatible',
  'permission.denied': 'permissionDenied',
  'validation.invalid': 'invalidRequest'
}

/** First diagnostic value for `field`; the API sends one-element arrays. */
function firstErrorField(fields: Record<string, string[]>, field: string) {
  const value = fields[field]?.[0]
  return typeof value === 'string' ? value.trim() : ''
}

/**
 * Maps a transport failure to the shared install error message. The admin
 * install panel and the bridge dialog both call this, so one backend code can
 * never grow two different explanations.
 */
export function adminExtensionInstallErrorMessage(
  t: (key: string, params?: Record<string, unknown>) => string,
  error: unknown,
  fallbackKey: string
) {
  const reason = apiErrorReason(error)
  const mapped = reason ? INSTALL_ERROR_KEYS[reason] : ''
  if (!mapped) return t(fallbackKey)

  // The platform gate always reports `required`/`host`; a backend that omits
  // them must not print literal `{required}` at the operator.
  if (mapped === 'platformIncompatible') {
    const fields = apiErrorFields(error)
    const required = firstErrorField(fields, 'required')
    const host = firstErrorField(fields, 'host')
    if (!required || !host) return t('admin.extensionInstall.errors.platformIncompatibleGeneric')
    return t('admin.extensionInstall.errors.platformIncompatible', { required, host })
  }

  return t(`admin.extensionInstall.errors.${mapped}`)
}
