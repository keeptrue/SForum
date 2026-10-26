package http

import (
	"context"
	"errors"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func requireExtensionsMutationAuthority(_ context.Context, evaluation routes.CoreGuardEvaluation) error {
	switch evaluation.Descriptor.RouteID {
	case "core.route.extensions.disable", "core.route.extensions.rollback":
		return requireCoreGuardPermission(evaluation, identity.PermissionExtensionPluginManage, identity.PermissionExtensionManage)
	case "core.route.extensions.activate":
		return requireCoreGuardPermission(evaluation, identity.PermissionExtensionThemeManage, identity.PermissionExtensionManage)
	case "core.route.extensions.recover_lifecycle_operation":
		if err := requireCoreGuardPermission(evaluation, identity.PermissionExtensionPluginManage, identity.PermissionExtensionManage); err != nil {
			return err
		}
		var input extensions.LifecycleRecoveryInput
		if err := decodeGuardJSON(evaluation.Request.Body, &input); err != nil {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		if input.EscalateForced {
			return requireCoreGuardPermission(evaluation, "*")
		}
		return nil
	case "core.route.extensions.revoke_executable_trust",
		"core.route.extensions.issue_executable_trust_challenge",
		// 开发期信任会话：创建/撤销/自动写入都是 super_admin only（"*"）。
		"core.route.extensions.create_dev_trust_session",
		"core.route.extensions.revoke_dev_trust_session",
		"core.route.extensions.dev_trust_refresh",
		"core.route.extensions.cleanup_missing_artifacts",
		// 卸载清理免键收尾：直接触发物理 purge，super_admin only。
		"core.route.extensions.finalize_uninstall_cleanup",
		"core.route.extensions.probe_provider_slot",
		"core.route.extensions.select_provider_slot",
		"core.route.extensions.reset_provider_slot",
		"core.route.extensions.select_route_provider",
		"core.route.extensions.reset_route_provider":
		return requireCoreGuardPermission(evaluation, "*")
	default:
		return routes.ErrCoreGuardEvaluatorUnavailable
	}
}

func extensionsMutationGuardEvaluator(policy ExtensionGuardPolicy) routes.CoreGuardEvaluatorFunc {
	return func(ctx context.Context, evaluation routes.CoreGuardEvaluation) error {
		if err := requireExtensionsMutationAuthority(ctx, evaluation); !errors.Is(err, routes.ErrCoreGuardEvaluatorUnavailable) {
			return err
		}
		lookup, ok := extensionGuardPolicyLookup(policy, evaluation)
		if !ok {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		if evaluation.Descriptor.RouteID == "core.route.extensions.install" {
			if !lookup.TrustChallengesEnabled {
				return requireCoreGuardPermission(evaluation, "*")
			}
			return requireCoreGuardPermission(evaluation,
				identity.PermissionExtensionPluginManage,
				identity.PermissionExtensionThemeManage,
				identity.PermissionExtensionManage,
			)
		}
		if !lookup.Found {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		entry := lookup.Entry
		switch evaluation.Descriptor.RouteID {
		case "core.route.extensions.uninstall":
			if entry.Source != extensions.SourceBuiltin && entry.HasExecutableBackend &&
				!(entry.LifecycleV2 && (entry.Status == extensions.StatusEnabled || entry.Status == extensions.StatusDisabled)) {
				return requireCoreGuardPermission(evaluation, "*")
			}
			return requireExtensionPluginAuthority(evaluation)
		case "core.route.extensions.enable":
			if lookup.SafeMode || entry.ExtensionType != extensions.TypePlugin {
				return routes.ErrCoreGuardEvaluatorUnavailable
			}
			if lookup.TrustChallengesEnabled {
				if entry.ReviewTrustRequired && !entry.ReviewArtifactTrusted {
					return requireCoreGuardPermission(evaluation, "*")
				}
			} else if entry.Source != extensions.SourceBuiltin && entry.HasExecutableBackend {
				return requireCoreGuardPermission(evaluation, "*")
			}
			return requireExtensionPluginAuthority(evaluation)
		case "core.route.extensions.apply_migrations", "core.route.extensions.verify":
			if lookup.SafeMode {
				return routes.ErrCoreGuardEvaluatorUnavailable
			}
			if entry.Source != extensions.SourceBuiltin && entry.HasExecutableBackend {
				return requireCoreGuardPermission(evaluation, "*")
			}
			return requireExtensionPluginAuthority(evaluation)
		case "core.route.extensions.update_settings", "core.route.extensions.reset_settings":
			return requireExtensionSettingsAuthority(evaluation, entry)
		case "core.route.extensions.execute_settings_action":
			if lookup.SafeMode {
				return routes.ErrCoreGuardEvaluatorUnavailable
			}
			return requireExtensionSettingsAuthority(evaluation, entry)
		case "core.route.extensions.upgrade":
			if lookup.SafeMode || entry.ExtensionType != extensions.TypePlugin || !entry.LifecycleV2 ||
				entry.Status != extensions.StatusEnabled || !entry.HasStagedArtifact {
				return routes.ErrCoreGuardEvaluatorUnavailable
			}
			if entry.StagedTrustRequired && !entry.StagedArtifactTrusted {
				return requireCoreGuardPermission(evaluation, "*")
			}
			return requireExtensionPluginAuthority(evaluation)
		case "core.route.extensions.restart":
			if lookup.SafeMode || entry.ExtensionType != extensions.TypePlugin ||
				(entry.Status != extensions.StatusEnabled && entry.Status != extensions.StatusDisabled) {
				return routes.ErrCoreGuardEvaluatorUnavailable
			}
			if entry.HasStagedArtifact {
				if entry.StagedTrustRequired && !entry.StagedArtifactTrusted {
					return requireCoreGuardPermission(evaluation, "*")
				}
			} else if entry.CurrentTrustRequired && !entry.CurrentArtifactTrusted {
				return requireCoreGuardPermission(evaluation, "*")
			}
			return requireExtensionPluginAuthority(evaluation)
		default:
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
	}
}

func extensionGuardPolicyLookup(policy ExtensionGuardPolicy, evaluation routes.CoreGuardEvaluation) (extensions.GuardPolicyLookup, bool) {
	if policy == nil {
		return extensions.GuardPolicyLookup{}, false
	}
	id := ""
	if evaluation.Descriptor.RouteID != "core.route.extensions.install" {
		id = evaluation.Request.Params["id"]
		if id == "" {
			return extensions.GuardPolicyLookup{}, false
		}
	}
	lookup, ok := policy.Lookup(id)
	if !ok || lookup.Revision == 0 || lookup.Found && lookup.Entry.ExtensionID != id {
		return extensions.GuardPolicyLookup{}, false
	}
	return lookup, true
}

func requireExtensionPluginAuthority(evaluation routes.CoreGuardEvaluation) error {
	return requireCoreGuardPermission(evaluation, identity.PermissionExtensionPluginManage, identity.PermissionExtensionManage)
}

func requireExtensionSettingsAuthority(evaluation routes.CoreGuardEvaluation, entry extensions.GuardPolicyEntry) error {
	switch entry.ExtensionType {
	case extensions.TypeTheme:
		return requireCoreGuardPermission(evaluation, identity.PermissionExtensionThemeManage, identity.PermissionExtensionManage)
	case extensions.TypePlugin:
		permissions := []string{identity.PermissionExtensionPluginManage, identity.PermissionExtensionManage}
		if entry.HasMailProvider {
			permissions = append(permissions, identity.PermissionSettingsMailManage)
		}
		return requireCoreGuardPermission(evaluation, permissions...)
	default:
		return routes.ErrCoreGuardEvaluatorUnavailable
	}
}
