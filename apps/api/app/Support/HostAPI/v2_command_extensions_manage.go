package hostapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	CommandExtensionPluginDisableID           = "sforum.extensions.plugin.disable"
	CommandExtensionPluginDisableVersion      = "1"
	CommandExtensionPluginDisableInputSchema  = "sforum.extensions.plugin.disable.input"
	CommandExtensionPluginDisableOutputSchema = "sforum.extensions.plugin.disable.result"
	CommandExtensionPluginDisableSchemaV1     = "1"

	CommandExtensionSettingsResetID           = "sforum.extensions.settings.reset"
	CommandExtensionSettingsResetVersion      = "1"
	CommandExtensionSettingsResetInputSchema  = "sforum.extensions.settings.reset.input"
	CommandExtensionSettingsResetOutputSchema = "sforum.extensions.settings.reset.result"
	CommandExtensionSettingsResetSchemaV1     = "1"

	CommandExtensionSettingsUpdateID           = "sforum.extensions.settings.update"
	CommandExtensionSettingsUpdateVersion      = "1"
	CommandExtensionSettingsUpdateInputSchema  = "sforum.extensions.settings.update.input"
	CommandExtensionSettingsUpdateOutputSchema = "sforum.extensions.settings.update.result"
	CommandExtensionSettingsUpdateSchemaV1     = "1"

	CommandExtensionSettingsActionID           = "sforum.extensions.settings.action"
	CommandExtensionSettingsActionVersion      = "1"
	CommandExtensionSettingsActionInputSchema  = "sforum.extensions.settings.action.input"
	CommandExtensionSettingsActionOutputSchema = "sforum.extensions.settings.action.result"
	CommandExtensionSettingsActionSchemaV1     = "1"

	// 自动化 settings.update 边界：仅合并明文键值，不触碰密文 secret。
	extensionsManageMaxSettingKeys       = 50
	extensionsManageMaxSettingKeyBytes   = 200
	extensionsManageMaxSettingValueBytes = 8 << 10
	extensionsManageMaxSettingTotalBytes = 64 << 10
	extensionsManageMaxActionIDBytes     = 120
	extensionsManageSecretCipherPrefix   = "enc::"
)

type protocolV2ExtensionPluginDisableInput struct {
	TargetExtensionID string `json:"targetExtensionId"`
}

type protocolV2ExtensionPluginDisableMutation struct {
	targetExtensionID string
}

// newProtocolV2ExtensionPluginDisableCommandDefinition 实现 extensions.manage
// 允许清单中的“禁用已信任非系统非自身插件”。状态写入在 Host Command 事务内完成；
// 运行时 drain 由后续 lifecycle reconcile 收敛。
func newProtocolV2ExtensionPluginDisableCommandDefinition() protocolV2CommandDefinition {
	definition := protocolV2CommandDefinition{
		ID: CommandExtensionPluginDisableID, Version: CommandExtensionPluginDisableVersion,
		InputSchemaID: CommandExtensionPluginDisableInputSchema, InputSchemaVersion: CommandExtensionPluginDisableSchemaV1,
		OutputSchemaID: CommandExtensionPluginDisableOutputSchema, OutputSchemaVersion: CommandExtensionPluginDisableSchemaV1,
		ActorMode:           protocolV2CommandActorDelegated,
		RequiredPermissions: []string{identity.PermissionExtensionPluginManage},
		RequiredCapability:  capabilities.ExtensionsManage,
	}
	definition.Preview = func(_ context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		mutation, err := protocolV2ExtensionPluginDisableMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		return protocolV2ExtensionPluginDisablePreparation(mutation, "enabled")
	}
	definition.Prepare = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		mutation, err := protocolV2ExtensionPluginDisableMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		runtime := ProtocolV2RuntimeIdentityFromContext(ctx)
		if runtime == nil || strings.TrimSpace(runtime.GetExtensionId()) == "" {
			return nil, invalidProtocolV2CommandActorDelegation()
		}
		callerID := strings.TrimSpace(runtime.GetExtensionId())
		if mutation.targetExtensionID == callerID {
			return nil, newProtocolV2CommandError(
				protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
				"host.extensions_manage_self_denied",
				"A plugin cannot disable itself through extensions.manage.",
				false,
			)
		}
		var (
			extensionType string
			status        string
			isSystem      bool
			source        string
		)
		err = tx.QueryRow(ctx, `
			SELECT type, status, is_system, source
			FROM extensions
			WHERE id = $1
			FOR UPDATE
		`, mutation.targetExtensionID).Scan(&extensionType, &status, &isSystem, &source)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, newProtocolV2CommandError(
				protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND,
				"host.extension_not_found",
				"The target extension does not exist.",
				false,
			)
		}
		if err != nil {
			return nil, fmt.Errorf("lock target extension for disable: %w", err)
		}
		if extensionType != "plugin" {
			return nil, newProtocolV2CommandError(
				protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
				"host.extensions_manage_theme_denied",
				"extensions.manage can only disable plugins.",
				false,
			)
		}
		if isSystem {
			return nil, newProtocolV2CommandError(
				protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
				"host.extensions_manage_system_denied",
				"System plugins cannot be disabled through extensions.manage.",
				false,
			)
		}
		if status != "enabled" {
			return nil, newProtocolV2CommandError(
				protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
				"host.extensions_manage_not_enabled",
				"Only an already-enabled trusted plugin can be disabled through extensions.manage.",
				false,
			)
		}
		// 已信任：builtin 非系统，或 uploaded 存在未撤销 enable grant。
		if source == "uploaded" {
			var trusted bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
				  SELECT 1 FROM extension_trust_grants
				  WHERE extension_id = $1 AND action = 'enable' AND revoked_at IS NULL
				)
			`, mutation.targetExtensionID).Scan(&trusted); err != nil {
				return nil, fmt.Errorf("check target trust grant: %w", err)
			}
			if !trusted {
				return nil, newProtocolV2CommandError(
					protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
					"host.extensions_manage_untrusted",
					"Only already-trusted plugins can be disabled through extensions.manage.",
					false,
				)
			}
		}
		return protocolV2ExtensionPluginDisablePreparation(mutation, status)
	}
	definition.Execute = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest, _ *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		mutation, err := protocolV2ExtensionPluginDisableMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		actorUserID, ok := ProtocolV2CommandActorUserID(ctx)
		if !ok {
			return nil, invalidProtocolV2CommandActorDelegation()
		}
		command, err := tx.Exec(ctx, `
			UPDATE extensions
			SET status = 'disabled', updated_at = now()
			WHERE id = $1 AND type = 'plugin' AND is_system = FALSE AND status = 'enabled'
		`, mutation.targetExtensionID)
		if err != nil {
			return nil, fmt.Errorf("disable target extension: %w", err)
		}
		if command.RowsAffected() != 1 {
			return nil, newProtocolV2CommandError(
				protocolv2.ErrorCode_ERROR_CODE_CONFLICT,
				"host.extensions_manage_race",
				"The target extension changed before disable committed.",
				false,
			)
		}
		// Keep the mail provider preference across disable; it is a preference,
		// not a runtime registration. Uninstall clears it.
		if _, err := tx.Exec(ctx, `
			INSERT INTO extension_events (extension_id, actor_user_id, action, message)
			VALUES ($1, $2, 'disabled', 'Extension disabled via extensions.manage Host Command.')
		`, mutation.targetExtensionID, actorUserID); err != nil {
			return nil, fmt.Errorf("append disable event: %w", err)
		}
		var revision time.Time
		if err := tx.QueryRow(ctx, `SELECT updated_at FROM extensions WHERE id = $1`, mutation.targetExtensionID).Scan(&revision); err != nil {
			return nil, fmt.Errorf("read disable revision: %w", err)
		}
		output, err := protocolV2Document(CommandExtensionPluginDisableOutputSchema, CommandExtensionPluginDisableSchemaV1, map[string]any{
			"targetExtensionId": mutation.targetExtensionID,
			"status":            "disabled",
			"revision":          revision.UTC().Format(time.RFC3339Nano),
		})
		if err != nil {
			return nil, fmt.Errorf("encode disable command result: %w", err)
		}
		return &protocolV2CommandExecution{
			Output: output, CommittedRevision: revision.UTC().Format(time.RFC3339Nano),
		}, nil
	}
	return definition
}

func protocolV2ExtensionPluginDisableMutationFromRequest(request *hostv2.CommandRequest) (protocolV2ExtensionPluginDisableMutation, error) {
	input, err := decodeProtocolV2CommandInput[protocolV2ExtensionPluginDisableInput](request)
	if err != nil || strings.TrimSpace(request.GetExpectedRevision()) != "" {
		return protocolV2ExtensionPluginDisableMutation{}, invalidProtocolV2DomainCommandInput()
	}
	target := strings.TrimSpace(input.TargetExtensionID)
	if target == "" || len(target) > 200 {
		return protocolV2ExtensionPluginDisableMutation{}, invalidProtocolV2DomainCommandInput()
	}
	return protocolV2ExtensionPluginDisableMutation{targetExtensionID: target}, nil
}

func protocolV2ExtensionPluginDisablePreparation(
	mutation protocolV2ExtensionPluginDisableMutation,
	currentStatus string,
) (*protocolV2CommandPreparation, error) {
	output, err := protocolV2Document(CommandExtensionPluginDisableOutputSchema, CommandExtensionPluginDisableSchemaV1, map[string]any{
		"targetExtensionId": mutation.targetExtensionID,
		"status":            "disabled",
		"previousStatus":    currentStatus,
	})
	if err != nil {
		return nil, err
	}
	return &protocolV2CommandPreparation{
		Policy: []*hostv2.PolicyDecision{{
			PolicyId: "sforum.extensions.manage@1", Allowed: true,
			Reason: "Delegated actor holds extension.plugin.manage and caller holds extensions.manage.",
		}},
		Impact: []*hostv2.ImpactItem{{
			Module: "extensions", Action: "disable", ResourceType: "plugin",
			ResourceId: mutation.targetExtensionID, Summary: "Disable an already-trusted non-system non-self plugin.",
		}},
		ProjectedResult: output,
	}, nil
}

type protocolV2ExtensionSettingsResetInput struct {
	TargetExtensionID string `json:"targetExtensionId"`
}

type protocolV2ExtensionSettingsResetMutation struct {
	targetExtensionID string
}

// newProtocolV2ExtensionSettingsResetCommandDefinition 重置已信任非系统非自身
// 插件的设置。仅删除 extension_settings 行（回到 Manifest 默认），不执行上传/
// 启用/信任变更。运行时热重载由后续 reconcile 收敛。
func newProtocolV2ExtensionSettingsResetCommandDefinition() protocolV2CommandDefinition {
	definition := protocolV2CommandDefinition{
		ID: CommandExtensionSettingsResetID, Version: CommandExtensionSettingsResetVersion,
		InputSchemaID: CommandExtensionSettingsResetInputSchema, InputSchemaVersion: CommandExtensionSettingsResetSchemaV1,
		OutputSchemaID: CommandExtensionSettingsResetOutputSchema, OutputSchemaVersion: CommandExtensionSettingsResetSchemaV1,
		ActorMode:           protocolV2CommandActorDelegated,
		RequiredPermissions: []string{identity.PermissionExtensionPluginManage},
		RequiredCapability:  capabilities.ExtensionsManage,
	}
	definition.Preview = func(_ context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		mutation, err := protocolV2ExtensionSettingsResetMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		return protocolV2ExtensionSettingsResetPreparation(mutation)
	}
	definition.Prepare = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		mutation, err := protocolV2ExtensionSettingsResetMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		if err := protocolV2ExtensionsManageLockTarget(ctx, tx, request, mutation.targetExtensionID, false); err != nil {
			return nil, err
		}
		return protocolV2ExtensionSettingsResetPreparation(mutation)
	}
	definition.Execute = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest, _ *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		mutation, err := protocolV2ExtensionSettingsResetMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		actorUserID, ok := ProtocolV2CommandActorUserID(ctx)
		if !ok {
			return nil, invalidProtocolV2CommandActorDelegation()
		}
		if _, err := tx.Exec(ctx, `DELETE FROM extension_settings WHERE extension_id = $1`, mutation.targetExtensionID); err != nil {
			return nil, fmt.Errorf("reset target extension settings: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO extension_events (extension_id, actor_user_id, action, message)
			VALUES ($1, $2, 'settings_reset', 'Extension settings reset via extensions.manage Host Command.')
		`, mutation.targetExtensionID, actorUserID); err != nil {
			return nil, fmt.Errorf("append settings reset event: %w", err)
		}
		var revision time.Time
		if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&revision); err != nil {
			return nil, fmt.Errorf("read settings reset revision: %w", err)
		}
		output, err := protocolV2Document(CommandExtensionSettingsResetOutputSchema, CommandExtensionSettingsResetSchemaV1, map[string]any{
			"targetExtensionId": mutation.targetExtensionID,
			"status":            "reset",
			"revision":          revision.UTC().Format(time.RFC3339Nano),
		})
		if err != nil {
			return nil, fmt.Errorf("encode settings reset result: %w", err)
		}
		return &protocolV2CommandExecution{
			Output: output, CommittedRevision: revision.UTC().Format(time.RFC3339Nano),
		}, nil
	}
	return definition
}

func protocolV2ExtensionSettingsResetMutationFromRequest(request *hostv2.CommandRequest) (protocolV2ExtensionSettingsResetMutation, error) {
	input, err := decodeProtocolV2CommandInput[protocolV2ExtensionSettingsResetInput](request)
	if err != nil || strings.TrimSpace(request.GetExpectedRevision()) != "" {
		return protocolV2ExtensionSettingsResetMutation{}, invalidProtocolV2DomainCommandInput()
	}
	target := strings.TrimSpace(input.TargetExtensionID)
	if target == "" || len(target) > 200 {
		return protocolV2ExtensionSettingsResetMutation{}, invalidProtocolV2DomainCommandInput()
	}
	return protocolV2ExtensionSettingsResetMutation{targetExtensionID: target}, nil
}

func protocolV2ExtensionSettingsResetPreparation(
	mutation protocolV2ExtensionSettingsResetMutation,
) (*protocolV2CommandPreparation, error) {
	output, err := protocolV2Document(CommandExtensionSettingsResetOutputSchema, CommandExtensionSettingsResetSchemaV1, map[string]any{
		"targetExtensionId": mutation.targetExtensionID,
		"status":            "reset",
	})
	if err != nil {
		return nil, err
	}
	return &protocolV2CommandPreparation{
		Policy: []*hostv2.PolicyDecision{{
			PolicyId: "sforum.extensions.manage@1", Allowed: true,
			Reason: "Delegated actor holds extension.plugin.manage and caller holds extensions.manage.",
		}},
		Impact: []*hostv2.ImpactItem{{
			Module: "extensions", Action: "settings_reset", ResourceType: "plugin_settings",
			ResourceId: mutation.targetExtensionID, Summary: "Reset settings of an already-trusted non-system non-self plugin.",
		}},
		ProjectedResult: output,
	}, nil
}

type protocolV2ExtensionSettingsUpdateInput struct {
	TargetExtensionID string            `json:"targetExtensionId"`
	Values            map[string]string `json:"values"`
}

type protocolV2ExtensionSettingsUpdateMutation struct {
	targetExtensionID string
	values            map[string]string
}

// newProtocolV2ExtensionSettingsUpdateCommandDefinition 合并已信任非系统非自身
// 插件的明文设置。不加密 secret、不覆盖 enc:: 密文、不执行上传/信任变更；
// 运行时热重载由后续 reconcile 收敛。
func newProtocolV2ExtensionSettingsUpdateCommandDefinition() protocolV2CommandDefinition {
	definition := protocolV2CommandDefinition{
		ID: CommandExtensionSettingsUpdateID, Version: CommandExtensionSettingsUpdateVersion,
		InputSchemaID: CommandExtensionSettingsUpdateInputSchema, InputSchemaVersion: CommandExtensionSettingsUpdateSchemaV1,
		OutputSchemaID: CommandExtensionSettingsUpdateOutputSchema, OutputSchemaVersion: CommandExtensionSettingsUpdateSchemaV1,
		ActorMode:           protocolV2CommandActorDelegated,
		RequiredPermissions: []string{identity.PermissionExtensionPluginManage},
		RequiredCapability:  capabilities.ExtensionsManage,
	}
	definition.Preview = func(_ context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		mutation, err := protocolV2ExtensionSettingsUpdateMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		return protocolV2ExtensionSettingsUpdatePreparation(mutation)
	}
	definition.Prepare = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		mutation, err := protocolV2ExtensionSettingsUpdateMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		if err := protocolV2ExtensionsManageLockTarget(ctx, tx, request, mutation.targetExtensionID, false); err != nil {
			return nil, err
		}
		// 拒绝覆盖已有密文 secret；自动化路径不得写入明文 secret。
		if err := protocolV2ExtensionsManageRejectSecretOverwrite(ctx, tx, mutation); err != nil {
			return nil, err
		}
		return protocolV2ExtensionSettingsUpdatePreparation(mutation)
	}
	definition.Execute = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest, _ *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		mutation, err := protocolV2ExtensionSettingsUpdateMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		actorUserID, ok := ProtocolV2CommandActorUserID(ctx)
		if !ok {
			return nil, invalidProtocolV2CommandActorDelegation()
		}
		if err := protocolV2ExtensionsManageRejectSecretOverwrite(ctx, tx, mutation); err != nil {
			return nil, err
		}
		for name, value := range mutation.values {
			if _, err := tx.Exec(ctx, `
				INSERT INTO extension_settings (extension_id, name, value)
				VALUES ($1, $2, $3)
				ON CONFLICT (extension_id, name) DO UPDATE
				SET value = EXCLUDED.value, updated_at = now()
			`, mutation.targetExtensionID, name, value); err != nil {
				return nil, fmt.Errorf("upsert target extension setting %s: %w", name, err)
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO extension_events (extension_id, actor_user_id, action, message)
			VALUES ($1, $2, 'settings_updated', 'Extension settings updated via extensions.manage Host Command.')
		`, mutation.targetExtensionID, actorUserID); err != nil {
			return nil, fmt.Errorf("append settings update event: %w", err)
		}
		var revision time.Time
		if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&revision); err != nil {
			return nil, fmt.Errorf("read settings update revision: %w", err)
		}
		keys := make([]string, 0, len(mutation.values))
		for name := range mutation.values {
			keys = append(keys, name)
		}
		output, err := protocolV2Document(CommandExtensionSettingsUpdateOutputSchema, CommandExtensionSettingsUpdateSchemaV1, map[string]any{
			"targetExtensionId": mutation.targetExtensionID,
			"status":            "updated",
			"updatedKeys":       keys,
			"revision":          revision.UTC().Format(time.RFC3339Nano),
		})
		if err != nil {
			return nil, fmt.Errorf("encode settings update result: %w", err)
		}
		return &protocolV2CommandExecution{
			Output: output, CommittedRevision: revision.UTC().Format(time.RFC3339Nano),
		}, nil
	}
	return definition
}

func protocolV2ExtensionSettingsUpdateMutationFromRequest(request *hostv2.CommandRequest) (protocolV2ExtensionSettingsUpdateMutation, error) {
	input, err := decodeProtocolV2CommandInput[protocolV2ExtensionSettingsUpdateInput](request)
	if err != nil || strings.TrimSpace(request.GetExpectedRevision()) != "" {
		return protocolV2ExtensionSettingsUpdateMutation{}, invalidProtocolV2DomainCommandInput()
	}
	target := strings.TrimSpace(input.TargetExtensionID)
	if target == "" || len(target) > 200 {
		return protocolV2ExtensionSettingsUpdateMutation{}, invalidProtocolV2DomainCommandInput()
	}
	if len(input.Values) == 0 || len(input.Values) > extensionsManageMaxSettingKeys {
		return protocolV2ExtensionSettingsUpdateMutation{}, invalidProtocolV2DomainCommandInput()
	}
	values := make(map[string]string, len(input.Values))
	total := 0
	for rawName, rawValue := range input.Values {
		name := strings.TrimSpace(rawName)
		if name == "" || len(name) > extensionsManageMaxSettingKeyBytes {
			return protocolV2ExtensionSettingsUpdateMutation{}, invalidProtocolV2DomainCommandInput()
		}
		if len(rawValue) > extensionsManageMaxSettingValueBytes {
			return protocolV2ExtensionSettingsUpdateMutation{}, invalidProtocolV2DomainCommandInput()
		}
		// 自动化不得直接写入密文前缀或伪造密文。
		if strings.HasPrefix(rawValue, extensionsManageSecretCipherPrefix) {
			return protocolV2ExtensionSettingsUpdateMutation{}, newProtocolV2CommandError(
				protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
				"host.extensions_manage_secret_denied",
				"extensions.manage settings.update cannot write encrypted secret values.",
				false,
			)
		}
		total += len(name) + len(rawValue)
		if total > extensionsManageMaxSettingTotalBytes {
			return protocolV2ExtensionSettingsUpdateMutation{}, invalidProtocolV2DomainCommandInput()
		}
		values[name] = rawValue
	}
	return protocolV2ExtensionSettingsUpdateMutation{targetExtensionID: target, values: values}, nil
}

func protocolV2ExtensionSettingsUpdatePreparation(
	mutation protocolV2ExtensionSettingsUpdateMutation,
) (*protocolV2CommandPreparation, error) {
	keys := make([]string, 0, len(mutation.values))
	for name := range mutation.values {
		keys = append(keys, name)
	}
	output, err := protocolV2Document(CommandExtensionSettingsUpdateOutputSchema, CommandExtensionSettingsUpdateSchemaV1, map[string]any{
		"targetExtensionId": mutation.targetExtensionID,
		"status":            "updated",
		"updatedKeys":       keys,
	})
	if err != nil {
		return nil, err
	}
	return &protocolV2CommandPreparation{
		Policy: []*hostv2.PolicyDecision{{
			PolicyId: "sforum.extensions.manage@1", Allowed: true,
			Reason: "Delegated actor holds extension.plugin.manage and caller holds extensions.manage.",
		}},
		Impact: []*hostv2.ImpactItem{{
			Module: "extensions", Action: "settings_update", ResourceType: "plugin_settings",
			ResourceId: mutation.targetExtensionID, Summary: "Update plain settings of an already-trusted non-system non-self plugin.",
		}},
		ProjectedResult: output,
	}, nil
}

func protocolV2ExtensionsManageRejectSecretOverwrite(
	ctx context.Context,
	tx pgx.Tx,
	mutation protocolV2ExtensionSettingsUpdateMutation,
) error {
	for name := range mutation.values {
		var existing string
		err := tx.QueryRow(ctx, `
			SELECT value FROM extension_settings
			WHERE extension_id = $1 AND name = $2
		`, mutation.targetExtensionID, name).Scan(&existing)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read existing extension setting %s: %w", name, err)
		}
		if strings.HasPrefix(existing, extensionsManageSecretCipherPrefix) {
			return newProtocolV2CommandError(
				protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
				"host.extensions_manage_secret_overwrite_denied",
				"extensions.manage settings.update cannot overwrite encrypted secret settings.",
				false,
			)
		}
	}
	return nil
}

type protocolV2ExtensionSettingsActionInput struct {
	TargetExtensionID string `json:"targetExtensionId"`
	ActionID          string `json:"actionId"`
}

type protocolV2ExtensionSettingsActionMutation struct {
	targetExtensionID string
	actionID          string
}

// newProtocolV2ExtensionSettingsActionCommandDefinition 记录已信任启用插件上的
// 安全设置动作请求。不转发 secret、不调用 provider_probe 运行时；仅持久化审计
// 事件，供自动化工作流与后续 reconcile/admin 路径消费。
func newProtocolV2ExtensionSettingsActionCommandDefinition() protocolV2CommandDefinition {
	definition := protocolV2CommandDefinition{
		ID: CommandExtensionSettingsActionID, Version: CommandExtensionSettingsActionVersion,
		InputSchemaID: CommandExtensionSettingsActionInputSchema, InputSchemaVersion: CommandExtensionSettingsActionSchemaV1,
		OutputSchemaID: CommandExtensionSettingsActionOutputSchema, OutputSchemaVersion: CommandExtensionSettingsActionSchemaV1,
		ActorMode:           protocolV2CommandActorDelegated,
		RequiredPermissions: []string{identity.PermissionExtensionPluginManage},
		RequiredCapability:  capabilities.ExtensionsManage,
	}
	definition.Preview = func(_ context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		mutation, err := protocolV2ExtensionSettingsActionMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		return protocolV2ExtensionSettingsActionPreparation(mutation)
	}
	definition.Prepare = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		mutation, err := protocolV2ExtensionSettingsActionMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		// 动作仅允许对已启用且已信任目标。
		if err := protocolV2ExtensionsManageLockTarget(ctx, tx, request, mutation.targetExtensionID, true); err != nil {
			return nil, err
		}
		return protocolV2ExtensionSettingsActionPreparation(mutation)
	}
	definition.Execute = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest, _ *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		mutation, err := protocolV2ExtensionSettingsActionMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		actorUserID, ok := ProtocolV2CommandActorUserID(ctx)
		if !ok {
			return nil, invalidProtocolV2CommandActorDelegation()
		}
		message := fmt.Sprintf(
			"Extension settings action %q requested via extensions.manage Host Command.",
			mutation.actionID,
		)
		if _, err := tx.Exec(ctx, `
			INSERT INTO extension_events (extension_id, actor_user_id, action, message)
			VALUES ($1, $2, 'settings_action', $3)
		`, mutation.targetExtensionID, actorUserID, message); err != nil {
			return nil, fmt.Errorf("append settings action event: %w", err)
		}
		var revision time.Time
		if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&revision); err != nil {
			return nil, fmt.Errorf("read settings action revision: %w", err)
		}
		output, err := protocolV2Document(CommandExtensionSettingsActionOutputSchema, CommandExtensionSettingsActionSchemaV1, map[string]any{
			"targetExtensionId": mutation.targetExtensionID,
			"actionId":          mutation.actionID,
			"status":            "accepted",
			"revision":          revision.UTC().Format(time.RFC3339Nano),
		})
		if err != nil {
			return nil, fmt.Errorf("encode settings action result: %w", err)
		}
		return &protocolV2CommandExecution{
			Output: output, CommittedRevision: revision.UTC().Format(time.RFC3339Nano),
		}, nil
	}
	return definition
}

func protocolV2ExtensionSettingsActionMutationFromRequest(request *hostv2.CommandRequest) (protocolV2ExtensionSettingsActionMutation, error) {
	input, err := decodeProtocolV2CommandInput[protocolV2ExtensionSettingsActionInput](request)
	if err != nil || strings.TrimSpace(request.GetExpectedRevision()) != "" {
		return protocolV2ExtensionSettingsActionMutation{}, invalidProtocolV2DomainCommandInput()
	}
	target := strings.TrimSpace(input.TargetExtensionID)
	actionID := strings.TrimSpace(input.ActionID)
	if target == "" || len(target) > 200 {
		return protocolV2ExtensionSettingsActionMutation{}, invalidProtocolV2DomainCommandInput()
	}
	if actionID == "" || len(actionID) > extensionsManageMaxActionIDBytes {
		return protocolV2ExtensionSettingsActionMutation{}, invalidProtocolV2DomainCommandInput()
	}
	// 动作 id 仅允许稳定标识符字符，防止消息注入。
	for _, r := range actionID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return protocolV2ExtensionSettingsActionMutation{}, invalidProtocolV2DomainCommandInput()
	}
	return protocolV2ExtensionSettingsActionMutation{targetExtensionID: target, actionID: actionID}, nil
}

func protocolV2ExtensionSettingsActionPreparation(
	mutation protocolV2ExtensionSettingsActionMutation,
) (*protocolV2CommandPreparation, error) {
	output, err := protocolV2Document(CommandExtensionSettingsActionOutputSchema, CommandExtensionSettingsActionSchemaV1, map[string]any{
		"targetExtensionId": mutation.targetExtensionID,
		"actionId":          mutation.actionID,
		"status":            "accepted",
	})
	if err != nil {
		return nil, err
	}
	return &protocolV2CommandPreparation{
		Policy: []*hostv2.PolicyDecision{{
			PolicyId: "sforum.extensions.manage@1", Allowed: true,
			Reason: "Delegated actor holds extension.plugin.manage and caller holds extensions.manage.",
		}},
		Impact: []*hostv2.ImpactItem{{
			Module: "extensions", Action: "settings_action", ResourceType: "plugin_settings_action",
			ResourceId: mutation.targetExtensionID + ":" + mutation.actionID,
			Summary:    "Record a settings action request on an already-trusted enabled non-self plugin.",
		}},
		ProjectedResult: output,
	}, nil
}

// protocolV2ExtensionsManageLockTarget 共享目标门禁：非自身、插件、非系统；
// requireEnabled 为 true 时还要求 enabled + 已信任。
func protocolV2ExtensionsManageLockTarget(
	ctx context.Context,
	tx pgx.Tx,
	request *hostv2.CommandRequest,
	targetExtensionID string,
	requireEnabled bool,
) error {
	runtime := ProtocolV2RuntimeIdentityFromContext(ctx)
	if runtime == nil || strings.TrimSpace(runtime.GetExtensionId()) == "" {
		return invalidProtocolV2CommandActorDelegation()
	}
	callerID := strings.TrimSpace(runtime.GetExtensionId())
	if targetExtensionID == callerID {
		return newProtocolV2CommandError(
			protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
			"host.extensions_manage_self_denied",
			"A plugin cannot manage itself through extensions.manage.",
			false,
		)
	}
	var (
		extensionType string
		status        string
		isSystem      bool
		source        string
	)
	err := tx.QueryRow(ctx, `
		SELECT type, status, is_system, source
		FROM extensions
		WHERE id = $1
		FOR UPDATE
	`, targetExtensionID).Scan(&extensionType, &status, &isSystem, &source)
	if errors.Is(err, pgx.ErrNoRows) {
		return newProtocolV2CommandError(
			protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND,
			"host.extension_not_found",
			"The target extension does not exist.",
			false,
		)
	}
	if err != nil {
		return fmt.Errorf("lock target extension for manage: %w", err)
	}
	if extensionType != "plugin" {
		return newProtocolV2CommandError(
			protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
			"host.extensions_manage_theme_denied",
			"extensions.manage can only target plugins.",
			false,
		)
	}
	if isSystem {
		return newProtocolV2CommandError(
			protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
			"host.extensions_manage_system_denied",
			"System plugins cannot be managed through extensions.manage.",
			false,
		)
	}
	if requireEnabled && status != "enabled" {
		return newProtocolV2CommandError(
			protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
			"host.extensions_manage_not_enabled",
			"Only an already-enabled trusted plugin can be disabled through extensions.manage.",
			false,
		)
	}
	if source == "uploaded" {
		var trusted bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
			  SELECT 1 FROM extension_trust_grants
			  WHERE extension_id = $1 AND action = 'enable' AND revoked_at IS NULL
			)
		`, targetExtensionID).Scan(&trusted); err != nil {
			return fmt.Errorf("check target trust grant: %w", err)
		}
		if !trusted {
			return newProtocolV2CommandError(
				protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
				"host.extensions_manage_untrusted",
				"Only already-trusted plugins can be managed through extensions.manage.",
				false,
			)
		}
	}
	_ = request
	return nil
}
