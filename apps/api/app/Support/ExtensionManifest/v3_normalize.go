package extensionmanifest

import "strings"

func normalizeV3Manifest(manifest *Manifest) {
	manifest.Backend.Digest = normalizeDigest(manifest.Backend.Digest)
	manifest.Backend.HostAPIVersion = strings.TrimSpace(manifest.Backend.HostAPIVersion)
	for index := range manifest.NotificationTypes {
		item := &manifest.NotificationTypes[index]
		item.ID = NormalizeID(item.ID)
		item.ContractVersion = strings.TrimSpace(item.ContractVersion)
		item.Category = NormalizeID(item.Category)
		item.PayloadSchema = strings.TrimSpace(item.PayloadSchema)
		item.Icon = strings.TrimSpace(item.Icon)
		item.TargetKind = strings.ToLower(strings.TrimSpace(item.TargetKind))
		item.TargetID = NormalizeID(item.TargetID)
		for channelIndex := range item.Channels {
			item.Channels[channelIndex] = strings.ToLower(strings.TrimSpace(item.Channels[channelIndex]))
		}
		for channelIndex := range item.RecommendedChannels {
			item.RecommendedChannels[channelIndex] = strings.ToLower(strings.TrimSpace(item.RecommendedChannels[channelIndex]))
		}
	}
	for index := range manifest.Migrations {
		item := &manifest.Migrations[index]
		item.ID = NormalizeID(item.ID)
		item.ContractVersion = strings.TrimSpace(item.ContractVersion)
		item.Path = strings.TrimSpace(item.Path)
		item.Digest = normalizeDigest(item.Digest)
		item.Transaction = strings.ToLower(strings.TrimSpace(item.Transaction))
	}
	for index := range manifest.AITools {
		item := &manifest.AITools[index]
		item.ID = strings.TrimSpace(item.ID)
		item.ContractVersion = strings.TrimSpace(item.ContractVersion)
		item.Name = strings.TrimSpace(item.Name)
		item.Description = strings.TrimSpace(item.Description)
		item.DescriptionI18n = normalizeAIToolDescriptionI18n(item.DescriptionI18n)
		item.Handler = strings.TrimSpace(item.Handler)
	}
	for index := range manifest.BadgeConditions {
		item := &manifest.BadgeConditions[index]
		item.Key = strings.TrimSpace(item.Key)
		item.ContractVersion = strings.TrimSpace(item.ContractVersion)
		for compareIndex := range item.Compare {
			item.Compare[compareIndex] = strings.ToLower(strings.TrimSpace(item.Compare[compareIndex]))
		}
		for variantIndex := range item.Variants {
			variant := &item.Variants[variantIndex]
			variant.Value = strings.TrimSpace(variant.Value)
			variant.Label = normalizeLocaleKeys(variant.Label)
		}
		item.Label = normalizeLocaleKeys(item.Label)
		item.Unit = normalizeLocaleKeys(item.Unit)
	}
	for index := range manifest.Routes {
		item := &manifest.Routes[index]
		item.ID = NormalizeID(item.ID)
		item.ContractVersion = strings.TrimSpace(item.ContractVersion)
		item.Action = strings.ToLower(strings.TrimSpace(item.Action))
		if item.Action == "" {
			item.Action = RouteActionAdd
		}
		item.TargetID = NormalizeID(item.TargetID)
		item.Guard = NormalizeID(item.Guard)
		if item.Guard == "" {
			if guard, ok := coreGuardForRouteAccess(item.Access); ok {
				item.Guard = guard
			} else if item.Access == "" && routeTargetsExisting(item.Action) {
				item.Guard = GuardCoreInherit
			}
		}
		item.Fallback = strings.ToLower(strings.TrimSpace(item.Fallback))
		item.Mode = strings.ToLower(strings.TrimSpace(item.Mode))
		if item.Mode == "" {
			item.Mode = RouteModeHTTP
		}
		if item.Fallback == "" {
			item.Fallback = "closed"
		}
		item.Destination = strings.TrimSpace(item.Destination)
		if item.Action == RouteActionRedirect && item.StatusCode == 0 {
			item.StatusCode = RouteRedirectStatusDefault
		}
		item.Handler = strings.TrimSpace(item.Handler)
		item.RequestSchema = strings.TrimSpace(item.RequestSchema)
		item.ResponseSchema = strings.TrimSpace(item.ResponseSchema)
		for fieldIndex := range item.MutableRequestFields {
			item.MutableRequestFields[fieldIndex] = strings.TrimSpace(item.MutableRequestFields[fieldIndex])
		}
		for fieldIndex := range item.MutableResponseFields {
			item.MutableResponseFields[fieldIndex] = strings.TrimSpace(item.MutableResponseFields[fieldIndex])
		}
	}
	for index := range manifest.Hooks {
		item := &manifest.Hooks[index]
		item.ID = NormalizeID(item.ID)
		item.ContractVersion = strings.TrimSpace(item.ContractVersion)
		item.Name = NormalizeID(item.Name)
		item.Kind = strings.ToLower(strings.TrimSpace(item.Kind))
		item.TargetID = NormalizeID(item.TargetID)
		item.Handler = strings.TrimSpace(item.Handler)
		item.InputSchema = strings.TrimSpace(item.InputSchema)
		item.ResultSchema = strings.TrimSpace(item.ResultSchema)
		item.Execution = strings.ToLower(strings.TrimSpace(item.Execution))
		if item.Execution == "" {
			if item.Kind == "observe" {
				item.Execution = "async"
			} else {
				item.Execution = "sync"
			}
		}
		item.FailurePolicy = strings.ToLower(strings.TrimSpace(item.FailurePolicy))
		if item.FailurePolicy == "" {
			if item.Execution == "async" {
				item.FailurePolicy = "fail_open"
			} else {
				item.FailurePolicy = "fail_closed"
			}
		}
		if item.TimeoutMS == 0 {
			if item.Execution == "async" {
				item.TimeoutMS = 5000
			} else {
				item.TimeoutMS = 2000
			}
		}
		for fieldIndex := range item.MutableFields {
			item.MutableFields[fieldIndex] = strings.TrimSpace(item.MutableFields[fieldIndex])
		}
	}
	for index := range manifest.Jobs {
		item := &manifest.Jobs[index]
		item.ID = NormalizeID(item.ID)
		item.ContractVersion = strings.TrimSpace(item.ContractVersion)
		item.Name = NormalizeID(item.Name)
		item.Handler = strings.TrimSpace(item.Handler)
		item.PayloadSchema = strings.TrimSpace(item.PayloadSchema)
		item.RetryPolicy = strings.ToLower(strings.TrimSpace(item.RetryPolicy))
		item.Queue = strings.ToLower(strings.TrimSpace(item.Queue))
		if item.ConcurrencyLimit == 0 {
			item.ConcurrencyLimit = PluginJobDefaultConcurrencyLimit
		}
		if item.MaxAttempts == 0 {
			switch item.RetryPolicy {
			case "none":
				item.MaxAttempts = 1
			case "bounded":
				item.MaxAttempts = PluginJobDefaultBoundedAttempts
			case "exponential":
				item.MaxAttempts = PluginJobDefaultExponentialAttempts
			}
		}
		if item.RetryPolicy == "bounded" && item.RetryDelaySeconds == 0 {
			item.RetryDelaySeconds = PluginJobDefaultRetryDelaySeconds
		}
	}
	for index := range manifest.Providers {
		item := &manifest.Providers[index]
		item.ID = NormalizeID(item.ID)
		item.ContractVersion = strings.TrimSpace(item.ContractVersion)
		item.Slot = NormalizeID(item.Slot)
		item.TargetID = NormalizeID(item.TargetID)
		item.Handler = strings.TrimSpace(item.Handler)
		item.RequestSchema = strings.TrimSpace(item.RequestSchema)
		item.ResponseSchema = strings.TrimSpace(item.ResponseSchema)
		item.Fallback = strings.ToLower(strings.TrimSpace(item.Fallback))
		if item.RequestSchema != "" || item.ResponseSchema != "" || item.TargetID != "" {
			if item.Fallback == "" {
				item.Fallback = "next"
			}
			if item.TimeoutMS == 0 {
				item.TimeoutMS = ProviderSlotMaximumTimeoutMS
			}
		}
	}

	for index := range manifest.Guards {
		item := &manifest.Guards[index]
		item.ID = NormalizeID(item.ID)
		item.ContractVersion = strings.TrimSpace(item.ContractVersion)
		item.Kind = strings.ToLower(strings.TrimSpace(item.Kind))
		item.Entry = strings.TrimSpace(item.Entry)
		item.Digest = normalizeDigest(item.Digest)
	}
	for index := range manifest.Schedules {
		item := &manifest.Schedules[index]
		item.ID = NormalizeID(item.ID)
		item.ContractVersion = strings.TrimSpace(item.ContractVersion)
		item.JobID = NormalizeID(item.JobID)
		item.Cron = strings.TrimSpace(item.Cron)
		item.Timezone = strings.TrimSpace(item.Timezone)
	}
	for index := range manifest.Components {
		item := &manifest.Components[index]
		item.ID = NormalizeID(item.ID)
		item.ContractVersion = strings.TrimSpace(item.ContractVersion)
		item.Action = strings.ToLower(strings.TrimSpace(item.Action))
		item.TargetID = NormalizeID(item.TargetID)
		item.TargetContractVersion = strings.TrimSpace(item.TargetContractVersion)
		item.Permission = NormalizeID(item.Permission)
		item.SSRTemplate = NormalizeID(item.SSRTemplate)
		item.L2Component = NormalizeID(item.L2Component)
		item.PropsSchema = strings.TrimSpace(item.PropsSchema)
		item.ResultSchema = strings.TrimSpace(item.ResultSchema)
		item.ThemeOverrideKey = NormalizeID(item.ThemeOverrideKey)
	}
	for index := range manifest.Templates {
		item := &manifest.Templates[index]
		item.ID = NormalizeID(item.ID)
		item.ContractVersion = strings.TrimSpace(item.ContractVersion)
		item.Action = strings.ToLower(strings.TrimSpace(item.Action))
		item.TargetID = NormalizeID(item.TargetID)
		item.Path = strings.TrimSpace(item.Path)
		item.Digest = normalizeDigest(item.Digest)
		item.ViewModelSchema = strings.TrimSpace(item.ViewModelSchema)
		item.ThemeOverrideKey = NormalizeID(item.ThemeOverrideKey)
	}
	for index := range manifest.Assets {
		item := &manifest.Assets[index]
		item.Handle = NormalizeID(item.Handle)
		item.ContractVersion = strings.TrimSpace(item.ContractVersion)
		item.Type = strings.ToLower(strings.TrimSpace(item.Type))
		item.Path = strings.TrimSpace(item.Path)
		item.Digest = normalizeDigest(item.Digest)
		item.Loading = strings.ToLower(strings.TrimSpace(item.Loading))
		item.Integrity = strings.TrimSpace(item.Integrity)
		for dependencyIndex := range item.Dependencies {
			item.Dependencies[dependencyIndex] = NormalizeID(item.Dependencies[dependencyIndex])
		}
		for scopeIndex := range item.Scope {
			item.Scope[scopeIndex] = NormalizeID(item.Scope[scopeIndex])
		}
		for cspIndex := range item.CSP {
			item.CSP[cspIndex] = strings.Join(strings.Fields(item.CSP[cspIndex]), " ")
		}
	}
	for index := range manifest.Content {
		item := &manifest.Content[index]
		item.ID = NormalizeID(item.ID)
		item.ContractVersion = strings.TrimSpace(item.ContractVersion)
		item.Kind = strings.ToLower(strings.TrimSpace(item.Kind))
		item.Handler = strings.TrimSpace(item.Handler)
		item.Schema = strings.TrimSpace(item.Schema)
		item.Renderer = NormalizeID(item.Renderer)
		item.Migration = NormalizeID(item.Migration)
	}
	for index := range manifest.Editor {
		item := &manifest.Editor[index]
		item.ID = NormalizeID(item.ID)
		item.ContractVersion = strings.TrimSpace(item.ContractVersion)
		item.Kind = strings.ToLower(strings.TrimSpace(item.Kind))
		item.Schema = strings.TrimSpace(item.Schema)
		item.ExtensionName = strings.TrimSpace(item.ExtensionName)
		item.L2Module = strings.TrimSpace(item.L2Module)
		item.L2Digest = normalizeDigest(item.L2Digest)
		item.CommandKey = strings.TrimSpace(item.CommandKey)
		item.CommandID = NormalizeID(item.CommandID)
		item.Label = strings.TrimSpace(item.Label)
		item.Icon = strings.TrimSpace(item.Icon)
		item.Group = NormalizeID(item.Group)
		item.Permission = strings.TrimSpace(item.Permission)
	}
	for index := range manifest.Entities {
		item := &manifest.Entities[index]
		item.ID = NormalizeID(item.ID)
		item.ContractVersion = strings.TrimSpace(item.ContractVersion)
		item.Kind = strings.ToLower(strings.TrimSpace(item.Kind))
		item.Label = strings.TrimSpace(item.Label)
		item.StorageKey = strings.ToLower(strings.TrimSpace(item.StorageKey))
		item.PermissionCreate = strings.TrimSpace(item.PermissionCreate)
		item.PermissionRead = strings.TrimSpace(item.PermissionRead)
		item.PermissionUpdate = strings.TrimSpace(item.PermissionUpdate)
		item.PermissionDelete = strings.TrimSpace(item.PermissionDelete)
		item.PermissionImport = strings.TrimSpace(item.PermissionImport)
		item.PermissionExport = strings.TrimSpace(item.PermissionExport)
		item.ImportExportPolicy = strings.ToLower(strings.TrimSpace(item.ImportExportPolicy))
		item.DeletionPolicy = strings.ToLower(strings.TrimSpace(item.DeletionPolicy))
		item.PermissionManage = strings.TrimSpace(item.PermissionManage)
		item.PermissionAssign = strings.TrimSpace(item.PermissionAssign)
		item.EntityID = NormalizeID(item.EntityID)
		item.Schema = strings.TrimSpace(item.Schema)
		item.UIComponent = strings.TrimSpace(item.UIComponent)
		item.UIModule = strings.TrimSpace(item.UIModule)
		item.UIDigest = normalizeDigest(item.UIDigest)
		item.IndexKind = strings.ToLower(strings.TrimSpace(item.IndexKind))
		item.PermissionFieldRead = strings.TrimSpace(item.PermissionFieldRead)
		item.PermissionFieldWrite = strings.TrimSpace(item.PermissionFieldWrite)
		item.Validation = strings.TrimSpace(item.Validation)
		for i := range item.TaxonomyIDs {
			item.TaxonomyIDs[i] = NormalizeID(item.TaxonomyIDs[i])
		}
		for i := range item.EntityIDs {
			item.EntityIDs[i] = NormalizeID(item.EntityIDs[i])
		}
	}
	normalizeV3Platform(manifest)
}

func coreGuardForRouteAccess(access string) (string, bool) {
	switch access {
	case RouteAccessPublic:
		return GuardCorePublic, true
	case RouteAccessLogin:
		return GuardCoreLogin, true
	case RouteAccessPermission:
		return GuardCorePermission, true
	default:
		return "", false
	}
}

func normalizeDigest(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.TrimSpace(strings.TrimPrefix(value, "sha256:"))
}

// normalizeLocaleKeys 把 locale map 归一成 `zh-CN` / `en-US` 这类规范形，并修剪值
// 两侧空白；键本身复用本包既有的 normalizeLocaleKey（manifest langs、localized
// text、平台的 locale 字段都走它），不另造大小写规则。
//
// 空键与空值会被丢弃：没有展示意义的条目留着只会让规范字节依赖 map 遍历顺序。
// 全空返回 nil，让「写了空 map」与「没写」序列化结果一致。
func normalizeLocaleKeys(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	normalized := make(map[string]string, len(values))
	for key, value := range values {
		code := normalizeLocaleKey(key)
		text := strings.TrimSpace(value)
		if code == "" || text == "" {
			continue
		}
		normalized[code] = text
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}
