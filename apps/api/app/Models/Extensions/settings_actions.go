package extensions

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

const maxSettingsActionInputBytes = 64 * 1024

func (s *SettingsService) ExecuteSettingsAction(
	ctx context.Context,
	actor identity.Actor,
	extensionID string,
	actionID string,
	input ExecuteSettingsActionInput,
) (result SettingsActionResult, err error) {
	if s.safeMode {
		return result, ErrSafeModeActive
	}
	extension, err := s.store.Get(ctx, normalizeID(extensionID))
	if err != nil {
		return result, err
	}
	if !canManageExtensionSettings(actor, extension) {
		return result, identity.ErrPermissionDenied
	}
	if err := requireArtifactAvailable(extension); err != nil {
		return result, err
	}
	action, ok := declaredSettingsAction(extension.Manifest, actionID)
	if !ok {
		return result, ErrSettingsActionInvalid
	}
	started := time.Now()
	defer func() {
		metadata := map[string]any{
			"extensionId": extension.ID,
			"actionId":    action.ID,
			"kind":        action.Kind,
			"success":     err == nil && result.Success,
			"durationMs":  time.Since(started).Milliseconds(),
		}
		s.appendAudit(ctx, actor, audit.ActionExtensionSettingsAction, metadata)
	}()
	if s.settingsActions == nil || extension.Manifest.Backend.Entry == "" {
		return result, ErrSettingsActionUnavailable
	}
	// 两条探测路径互斥：provider_probe 需要一个 provider slot，ai_tool_probe
	// 需要一个已声明的 AI 工具；其余 kind 一律不可执行。
	probeRequest := SettingsActionProbeRequest{}
	switch action.Kind {
	case extensionmanifest.SettingsActionProviderProbe:
		probeRequest.ProviderSlot = settingsActionProviderSlot(extension.Manifest)
		if probeRequest.ProviderSlot == "" {
			return result, ErrSettingsActionUnavailable
		}
	case extensionmanifest.SettingsActionAIToolProbe:
		if !settingsActionToolDeclared(extension.Manifest, action.Tool) {
			return result, ErrSettingsActionUnavailable
		}
		probeRequest.AIToolID = action.Tool
	default:
		return result, ErrSettingsActionUnavailable
	}
	current, err := s.listDecryptedSettings(ctx, extension)
	if err != nil {
		return result, err
	}
	values, err := settingsActionValues(extension.Manifest, action, input, current)
	if err != nil {
		return result, err
	}
	probeRequest.Values = values
	actionCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	probe, err := s.settingsActions.ProbeSettingsAction(actionCtx, extension, probeRequest)
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrSettingsActionUnavailable, err)
	}
	// AI 工具路径的 Details/Suggestions 恒为空：工具结果契约只有 content/isError，
	// 所有给人看的说明都由插件放进 message 文本里。
	return SettingsActionResult{
		Success: probe.OK, Reason: boundedActionText(probe.Reason, 256), Message: boundedActionText(probe.Message, 4096),
		Details: boundedActionDetails(probe.Details), Suggestions: boundedActionSuggestions(probe.Suggestions),
		DurationMS: time.Since(started).Milliseconds(),
	}, nil
}

// settingsActionToolDeclared 判断工具 id 是否为本插件声明的 AI 工具：必须落在
// 插件命名空间下，且逐字匹配 manifest.aiTools[].id。
func settingsActionToolDeclared(manifest Manifest, tool string) bool {
	tool = strings.TrimSpace(tool)
	if !extensionmanifest.IsOwnedID(manifest.ID, tool) {
		return false
	}
	for _, declaration := range manifest.AITools {
		if declaration.ID == tool {
			return true
		}
	}
	return false
}

func declaredSettingsAction(manifest Manifest, actionID string) (SettingsAction, bool) {
	actionID = normalizeID(actionID)
	for _, action := range manifest.SettingsDocument.Actions {
		if action.ID == actionID {
			return action, true
		}
	}
	return SettingsAction{}, false
}

func settingsActionValues(manifest Manifest, action SettingsAction, input ExecuteSettingsActionInput, current map[string]string) (map[string]string, error) {
	allowed := map[string]ManifestSetting{}
	for _, field := range manifest.Settings {
		allowed[field.Key] = field
	}
	if len(action.Fields) > 0 {
		restricted := map[string]ManifestSetting{}
		for _, key := range action.Fields {
			restricted[key] = allowed[key]
		}
		allowed = restricted
	}
	total := 0
	draft := map[string]string{}
	for key, value := range input.Values {
		field, ok := allowed[key]
		if !ok || field.Type == "secret" || !action.UseDraftValues {
			return nil, ErrSettingsActionInvalid
		}
		total += len(key) + len(value)
		draft[key] = value
	}
	for key, secret := range input.Secrets {
		field, ok := allowed[key]
		if !ok || field.Type != "secret" || !action.UseDraftValues {
			return nil, ErrSettingsActionInvalid
		}
		total += len(key) + len(secret.Mode) + len(secret.Value)
		switch secret.Mode {
		case "preserve":
		case "replace":
			if secret.Value == "" {
				return nil, ErrSettingsActionInvalid
			}
			draft[key] = secret.Value
		default:
			return nil, ErrSettingsActionInvalid
		}
	}
	if total > maxSettingsActionInputBytes {
		return nil, ErrSettingsActionInvalid
	}
	if action.UseDraftValues {
		for key, field := range allowed {
			if field.Type == "secret" {
				if _, ok := input.Secrets[key]; !ok {
					return nil, ErrSettingsActionInvalid
				}
			}
		}
	}
	return sanitizeSettingValues(manifest, draft, current)
}

func settingsActionProviderSlot(manifest Manifest) string {
	if len(manifest.Providers) == 0 {
		return ""
	}
	slots := make([]string, 0, len(manifest.Providers))
	for _, provider := range manifest.Providers {
		slots = append(slots, provider.Slot)
	}
	sort.Strings(slots)
	return slots[0]
}

func boundedActionText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

func boundedActionDetails(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := map[string]string{}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if len(result) == 20 {
			break
		}
		result[boundedActionText(key, 128)] = boundedActionText(values[key], 512)
	}
	return result
}

func boundedActionSuggestions(values []string) []string {
	if len(values) > 10 {
		values = values[:10]
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, boundedActionText(value, 512))
	}
	return result
}
