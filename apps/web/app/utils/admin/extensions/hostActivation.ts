/**
 * Pure decisions behind the host-mediated enable step.
 *
 * The composable owns the state machine; this file owns "which lifecycle call
 * and which confirmation body", so the answer can be unit-tested without a
 * dialog, a transport, or a Vue instance.
 */

import { extensionUpgradeLifecycleAction } from '~/utils/admin/adminExtensions'
import type { AdminExtension } from '~/utils/admin/adminExtensions'
import type {
  AdminHostInstallStagedReason,
  AdminPageHostActivateAction
} from './hostActions'

/**
 * A newly installed plugin is enabled; a staged replacement reuses the exact
 * upgrade path the extension page uses (`upgrade` for lifecycle-v2, else
 * `restart`) instead of inventing a second upgrade mechanism.
 */
export function hostActivationAction(item: AdminExtension, upgraded: boolean): AdminPageHostActivateAction {
  return upgraded ? extensionUpgradeLifecycleAction(item) : 'enable'
}

/**
 * Confirmation body for a lifecycle call the Host itself is authorising. A
 * one-use token replaces the legacy boolean: it is bound to the exact artifact.
 * Without a token, capability grants still need the operator's explicit click,
 * which is what "install and enable" already was.
 */
export function hostActivationBody(item: AdminExtension, confirmationToken?: string) {
  const token = confirmationToken?.trim()
  if (token) return { confirmationToken: token }
  return (item.capabilityGrants?.length ?? 0) > 0 ? { confirmCapabilities: true } : {}
}

/** Toast/panel copy explaining why the Host stopped after installing. */
export function hostStagedDescriptionKey(reason: AdminHostInstallStagedReason) {
  switch (reason) {
    case 'theme': return 'admin.extensionInstall.hostAction.stagedThemeBody'
    case 'super_admin': return 'admin.extensionInstall.hostAction.stagedSuperAdminBody'
    case 'declined': return 'admin.extensionInstall.hostAction.stagedDeclinedBody'
    default: return 'admin.extensionInstall.hostAction.stagedOnlyBody'
  }
}
