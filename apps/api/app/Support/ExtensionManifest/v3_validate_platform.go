package extensionmanifest

import (
	"encoding/json"
	"fmt"
	"strings"
)

func knownProviderSlot(slot string) bool {
	switch slot {
	case "mail.provider", "search.provider", "attachment.storage.provider", "human_verification.provider", "auth.risk.provider", "editor.sanitizer.provider", "notification.channel.web_push":
		return true
	default:
		return false
	}
}

const (
	manifestDatabaseMaximumParameters         = 32
	manifestDatabaseMaximumColumns            = 64
	manifestDatabaseMaximumParameterSize      = 64 << 10
	manifestDatabaseMaximumRows               = 1000
	manifestDatabaseMaximumAffectedRows       = 10_000
	manifestDatabaseMaximumTimeoutMS          = 5000
	manifestIdentityMaximumRoleSuggestions    = 64
	manifestIdentityMaximumRiskHooks          = 128
	ManifestIdentityProviderMaximumOperations = 16
	ManifestIdentityProviderDefaultTimeoutMS  = 1000
	ManifestIdentityProviderMaximumTimeoutMS  = 5000
	ManifestSEOMaximumDeclarations            = 512
	ManifestSEODefaultTimeoutMS               = 500
	ManifestSEOMaximumTimeoutMS               = 5000
	ManifestSEOMaximumPriority                = 1_000_000
	ManifestQueryMaximumDeclarations          = 512
	ManifestQueryMaximumCacheTags             = 32
	ManifestQueryMaximumIdentityFields        = 8
	ManifestQueryMaximumSorts                 = 16
	ManifestQueryResultFilterMaximum          = 64
	ManifestQueryResultFilterDefaultTimeoutMS = 1000
	ManifestQueryResultFilterMaximumTimeoutMS = 5000
	ManifestQueryResultFilterMaximumPriority  = 1_000_000
)

const (
	QueryResultFilterFailureFailClosed = "fail_closed"
	QueryResultFilterFailureFailOpen   = "fail_open"
	IdentityProviderFailureFailClosed  = "fail_closed"
	IdentityProviderFailureOmit        = "omit"
)

func (v *v3Validator) validatePlatform() error {
	// kind 判据集中在清单里；各家族自己的 validator 仍然负责字段级规则。
	if err := v.validatePluginOnlyFamilies(); err != nil {
		return err
	}
	if err := v.validateNotificationTypes(); err != nil {
		return err
	}
	if err := v.validateDatabaseAndCache(); err != nil {
		return err
	}
	if err := v.validateSEO(); err != nil {
		return err
	}
	if err := v.validateServicesCommandsAdminAndQueries(); err != nil {
		return err
	}
	if err := v.validateIdentityAndPermissions(); err != nil {
		return err
	}
	if err := v.validateMediaNavigationAndRegions(); err != nil {
		return err
	}
	if err := v.validateDependenciesAndLifecycle(); err != nil {
		return err
	}
	if err := v.validateAITools(); err != nil {
		return err
	}
	if err := v.validateFactConditions(); err != nil {
		return err
	}
	return nil
}

// v3PluginOnlyDeclarationFamilies 是「只允许插件声明」家族的**唯一清单**。
//
// 它同时是三样东西的来源：运行期判据（validatePluginOnlyFamilies）、JSON schema
// 分支的对照表（v3_schema_kind_families_test.go 双向比对）、以及文档里「哪些家族
// 是 plugin-only」的答案。新增一个 plugin-only 家族时**只改这里**：
// 校验立即生效；忘了给 schema 补 if/then 分支，测试会指名报缺。
//
// Declared 只回答「清单声明了这个家族吗」，与各家族自己的结构校验正交——
// 这里只判 kind，不重复它们的字段级规则。
var v3PluginOnlyDeclarationFamilies = []struct {
	Family   string
	Declared func(Manifest) bool
}{
	{"aiTools", func(m Manifest) bool { return len(m.AITools) > 0 }},
	{"notificationTypes", func(m Manifest) bool { return len(m.NotificationTypes) > 0 }},
	{"seo", func(m Manifest) bool { return len(m.SEO) > 0 }},
	{"identity", func(m Manifest) bool { return m.Identity != nil }},
	{"badgeConditions", func(m Manifest) bool { return len(m.BadgeConditions) > 0 }},
	{"userFactConditions", func(m Manifest) bool { return len(m.UserFactConditions) > 0 }},
	{"permissionDefinitions", func(m Manifest) bool { return len(m.PermissionDefinitions) > 0 }},
}

// validatePluginOnlyFamilies 把「这些家族只对 plugin 合法」集中判一次。
//
// 各家族自己的校验仍然保留（它们还管字段级规则与 backend 需求），但 kind 这一条
// 从此由清单驱动：新增家族只加一行，不必再去每个 validator 里补同一个 if。
func (v *v3Validator) validatePluginOnlyFamilies() error {
	if v.manifest.Type == TypePlugin {
		return nil
	}
	for _, family := range v3PluginOnlyDeclarationFamilies {
		if family.Declared(v.manifest) {
			return fmt.Errorf("%w: v3 family %s is plugin-only", ErrInvalidManifest, family.Family)
		}
	}
	return nil
}

// validateAITools 校验插件贡献的 AI 工具声明。清单侧与登记表（运行期）共用
// 同一份边界：能通过校验的清单必须能注册，声明被拒的清单不能被执行。
func (v *v3Validator) validateAITools() error {
	if len(v.manifest.AITools) == 0 {
		return nil
	}
	if v.manifest.Type != TypePlugin {
		return fmt.Errorf("%w: v3 aitools rule #1", ErrInvalidManifest)
	}
	if v.manifest.Backend.Entry == "" || v.manifest.Backend.ProtocolVersion != 2 {
		return fmt.Errorf("%w: v3 aitools rule #2", ErrInvalidManifest)
	}
	if len(v.manifest.AITools) > AIToolsMaximum {
		return fmt.Errorf("%w: v3 aitools rule #3", ErrInvalidManifest)
	}
	names := make(map[string]struct{}, len(v.manifest.AITools))
	for _, declaration := range v.manifest.AITools {
		if declaration.ID == "" || declaration.ID != strings.TrimSpace(declaration.ID) ||
			declaration.ContractVersion == "" || declaration.ContractVersion != strings.TrimSpace(declaration.ContractVersion) {
			return fmt.Errorf("%w: v3 aitools rule #4", ErrInvalidManifest)
		}
		// 契约版本必须严格为 `<ID>@1`：声明粒度的首个契约就是 1，形状变更
		// 通过新 ID 表达，而不是在同一声明上叠加版本。
		if declaration.ContractVersion != declaration.ID+"@1" {
			return fmt.Errorf("%w: v3 aitools rule #5", ErrInvalidManifest)
		}
		if err := v.versionedID(declaration.ID, declaration.ContractVersion, "ai_tool"); err != nil {
			return err
		}
		if !IsOwnedID(v.manifest.ID, declaration.ID) {
			return fmt.Errorf("%w: v3 aitools rule #6", ErrInvalidManifest)
		}
		if declaration.Name == "" || declaration.Name != strings.TrimSpace(declaration.Name) ||
			len(declaration.Name) > AIToolNameMaximumLength || !validAIToolName(declaration.Name) {
			return fmt.Errorf("%w: v3 aitools rule #7", ErrInvalidManifest)
		}
		if _, duplicate := names[declaration.Name]; duplicate {
			// 模型无法区分同名工具：重复名在清单侧就拒绝。
			return fmt.Errorf("%w: v3 aitools rule #8", ErrInvalidManifest)
		}
		names[declaration.Name] = struct{}{}
		if declaration.Description == "" || declaration.Description != strings.TrimSpace(declaration.Description) ||
			len(declaration.Description) > AIToolDescriptionMaximumBytes {
			return fmt.Errorf("%w: v3 aitools rule #9", ErrInvalidManifest)
		}
		if !validAIToolParameters(declaration.Parameters) {
			return fmt.Errorf("%w: v3 aitools rule #10", ErrInvalidManifest)
		}
		if declaration.TimeoutMS <= 0 || declaration.TimeoutMS > AIToolMaximumTimeoutMS {
			return fmt.Errorf("%w: v3 aitools rule #11", ErrInvalidManifest)
		}
		if !IsOwnedID(v.manifest.ID, declaration.Handler) {
			return fmt.Errorf("%w: v3 aitools rule #12", ErrInvalidManifest)
		}
		if err := ValidateAIToolContext(declaration.Context); err != nil {
			return err
		}
		// rule #14：rule #13 已由 ValidateAIToolContext 占用（2026-10-19 的会话事实
		// 声明），新增规则追加在末尾，不重编号既有规则。
		if err := validateAIToolDescriptionI18n(declaration.DescriptionI18n); err != nil {
			return err
		}
	}
	return nil
}

// validAIToolName 只接受供应商实际接受的模式：[a-z0-9_-]，不含点号。
func validAIToolName(name string) bool {
	if name == "" {
		return false
	}
	for _, char := range name {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '-', char == '_':
		default:
			return false
		}
	}
	return true
}

// validAIToolParameters 只校验形状：合法 JSON、且顶层是对象。Core 不解释
// schema 的语义；供应商会在请求层拒绝其它形状。
func validAIToolParameters(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	if len(raw) > AIToolSchemaMaximumBytes {
		return false
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	_, ok := value.(map[string]any)
	return ok
}

// contractVersionOf 提取 @version 段，用于严格校验「契约版本 = ID@version」。
func contractVersionOf(contractVersion string) string {
	if index := strings.LastIndex(contractVersion, "@"); index >= 0 {
		return contractVersion[index+1:]
	}
	return ""
}

func (v *v3Validator) validateNotificationTypes() error {
	if len(v.manifest.NotificationTypes) > ContentDeclarationsMaximum ||
		len(v.manifest.NotificationTypes) > 0 && v.manifest.Type != TypePlugin {
		return ErrInvalidManifest
	}
	for _, declaration := range v.manifest.NotificationTypes {
		if err := v.versionedID(declaration.ID, declaration.ContractVersion, "notification_type"); err != nil {
			return err
		}
		if !strings.HasPrefix(declaration.ID, v.manifest.ID+".") || declaration.ContractVersion != declaration.ID+"@1" ||
			declaration.PayloadVersion <= 0 || !manifestIDPattern.MatchString(declaration.Category) ||
			!validContractVersion(declaration.PayloadSchema) || declaration.Required ||
			!validLocalizedNotificationText(declaration.Label) || !validLocalizedNotificationText(declaration.Body) ||
			!validNotificationIcon(declaration.Icon) || !validNotificationTarget(declaration.TargetKind, declaration.TargetID) ||
			!validNotificationChannels(declaration.Channels, declaration.RecommendedChannels) {
			return ErrInvalidManifest
		}
	}
	return nil
}

func validLocalizedNotificationText(value LocalizedText) bool {
	if strings.TrimSpace(value.Default) == "" {
		return false
	}
	values := make([]string, 0, len(value.ByLocale)+1)
	values = append(values, value.Default)
	for _, text := range value.ByLocale {
		values = append(values, text)
	}
	for _, text := range values {
		if text == "" || text != strings.TrimSpace(text) || len(text) > 512 || strings.ContainsAny(text, "<>") {
			return false
		}
	}
	return true
}

func validNotificationIcon(icon string) bool {
	return icon == "" || len(icon) <= 128 && (strings.HasPrefix(icon, "i-lucide-") || strings.HasPrefix(icon, "i-tabler-"))
}

func validNotificationTarget(kind, id string) bool {
	switch kind {
	case "none":
		return id == ""
	case "host_route", "extension_route", "entity":
		return manifestIDPattern.MatchString(id)
	default:
		return false
	}
}

func validNotificationChannels(channels, recommended []string) bool {
	if len(channels) == 0 || len(channels) > 8 || len(recommended) > len(channels) {
		return false
	}
	available := make(map[string]struct{}, len(channels))
	for _, channel := range channels {
		if channel != "in_app" && channel != "email" && channel != "web_push" {
			return false
		}
		if _, duplicate := available[channel]; duplicate {
			return false
		}
		available[channel] = struct{}{}
	}
	seen := make(map[string]struct{}, len(recommended))
	for _, channel := range recommended {
		if _, ok := available[channel]; !ok {
			return false
		}
		if _, duplicate := seen[channel]; duplicate {
			return false
		}
		seen[channel] = struct{}{}
	}
	return true
}

func (v *v3Validator) validateSEO() error {
	if len(v.manifest.SEO) == 0 {
		return nil
	}
	// Every SEO declaration names an executable provider. Themes and inert/V1
	// packages cannot publish a declaration that the Host cannot call exactly.
	if v.manifest.Type != TypePlugin || strings.TrimSpace(v.manifest.Backend.Entry) == "" ||
		v.manifest.Backend.ProtocolVersion != 2 || len(v.manifest.SEO) > ManifestSEOMaximumDeclarations {
		return ErrInvalidManifest
	}
	for _, declaration := range v.manifest.SEO {
		if err := v.versionedID(declaration.ID, declaration.ContractVersion, "seo"); err != nil {
			return err
		}
		if declaration.ContractVersion != declaration.ID+"@1" ||
			(declaration.Scope != "global" && !manifestIDPattern.MatchString(declaration.Scope)) ||
			!manifestIDPattern.MatchString(declaration.Handler) ||
			!strings.HasPrefix(declaration.Handler, v.manifest.ID+".") ||
			declaration.Priority < -ManifestSEOMaximumPriority || declaration.Priority > ManifestSEOMaximumPriority ||
			declaration.TimeoutMS <= 0 || declaration.TimeoutMS > ManifestSEOMaximumTimeoutMS {
			return ErrInvalidManifest
		}
		switch declaration.Kind {
		case "title", "meta", "canonical", "robots", "hreflang", "sitemap", "jsonld":
		default:
			return ErrInvalidManifest
		}
		switch declaration.Action {
		case "add", "filter", "replace":
		default:
			return ErrInvalidManifest
		}
		switch declaration.FailurePolicy {
		case "fail_closed", "fallback":
		default:
			return ErrInvalidManifest
		}
	}
	return nil
}

func (v *v3Validator) validateDatabaseAndCache() error {
	if database := v.manifest.Database; database != nil {
		if !validContractVersion(database.ContractVersion) {
			return ErrInvalidManifest
		}
		grants := DatabaseGrants(database)
		if database.Authority != "" || len(grants) == 0 || len(grants) != len(database.Grants) {
			return ErrInvalidManifest
		}
		seenGrants := make(map[string]struct{}, len(grants))
		for _, grant := range grants {
			switch grant {
			case DatabaseGrantOwnSchema, DatabaseGrantCoreViews, DatabaseGrantHostCommands, DatabaseGrantRawCore, DatabaseGrantKernel:
			default:
				return ErrInvalidManifest
			}
			if _, duplicate := seenGrants[grant]; duplicate {
				return ErrInvalidManifest
			}
			seenGrants[grant] = struct{}{}
		}
		hasOwnSchema := HasDatabaseGrant(database, DatabaseGrantOwnSchema)
		if hasOwnSchema {
			// Host owns physical names. Logical schema/role remain required for
			// the historical own_schema-only contract and optional for cumulative
			// legacy tiers, but they must always be supplied as a valid pair.
			if (database.Schema == "") != (database.Role == "") ||
				(database.Schema != "" && (!databaseNamePattern.MatchString(database.Schema) || !databaseNamePattern.MatchString(database.Role))) ||
				len(grants) == 1 && database.Schema == "" {
				return ErrInvalidManifest
			}
		} else if database.Schema != "" || database.Role != "" {
			return ErrInvalidManifest
		}
		if (HasDatabaseGrant(database, DatabaseGrantRawCore) || HasDatabaseGrant(database, DatabaseGrantKernel)) && !validSemverConstraint(database.CoreCompatibility) {
			return ErrInvalidManifest
		}
		if database.Backup.Required && database.Backup.Strategy == "" || database.Retention.Days < 0 {
			return ErrInvalidManifest
		}
		if database.Retention.OnDisable != "retain" {
			return ErrInvalidManifest
		}
		switch database.Retention.OnUninstall {
		case "retain", "delete", "export":
		default:
			return ErrInvalidManifest
		}
		if (len(database.Operations) > 0 || len(v.manifest.Migrations) > 0) && !hasOwnSchema {
			return ErrInvalidManifest
		}
		for _, operation := range database.Operations {
			if err := v.validateDatabaseOperation(operation); err != nil {
				return err
			}
		}
	}
	for _, cache := range v.manifest.Cache {
		if err := v.versionedID(cache.ID, cache.ContractVersion, "cache"); err != nil {
			return err
		}
		if !strings.HasPrefix(cache.Namespace, v.manifest.ID+".") {
			return ErrInvalidManifest
		}
		switch cache.Policy {
		case "private", "actor", "permission", "public":
		default:
			return ErrInvalidManifest
		}
		for _, tag := range append(append([]string{}, cache.Tags...), cache.Invalidators...) {
			if !manifestIDPattern.MatchString(tag) {
				return ErrInvalidManifest
			}
		}
	}
	return nil
}

func (v *v3Validator) validateDatabaseOperation(operation ManifestDatabaseOperation) error {
	if !manifestIDPattern.MatchString(operation.ID) || !strings.HasPrefix(operation.ID, v.manifest.ID+".") ||
		!positiveIntegerPattern.MatchString(operation.StatementVersion) ||
		!validPackagePath(operation.Path) || !validDigest(operation.Digest) ||
		operation.Parameters == nil || operation.Columns == nil ||
		len(operation.Parameters) > manifestDatabaseMaximumParameters ||
		len(operation.Columns) > manifestDatabaseMaximumColumns ||
		!validManifestQueryCacheTags(v.manifest.ID, operation.QueryInvalidationTags) ||
		operation.TimeoutMS < 0 || operation.TimeoutMS > manifestDatabaseMaximumTimeoutMS {
		return ErrInvalidManifest
	}
	if _, duplicate := v.ids[operation.ID]; duplicate {
		return ErrInvalidManifest
	}
	v.ids[operation.ID] = "database_operation"

	for _, parameter := range operation.Parameters {
		if !validSchemaRef(parameter.Schema) || !databaseOperationNamePattern.MatchString(parameter.Field) ||
			parameter.MaxBytes < 0 || parameter.MaxBytes > manifestDatabaseMaximumParameterSize {
			return ErrInvalidManifest
		}
		switch parameter.Kind {
		case "string", "int64", "number", "bool":
		default:
			return ErrInvalidManifest
		}
	}
	seenColumns := make(map[string]struct{}, len(operation.Columns))
	for _, column := range operation.Columns {
		if !databaseOperationNamePattern.MatchString(column.Name) {
			return ErrInvalidManifest
		}
		if _, duplicate := seenColumns[column.Name]; duplicate {
			return ErrInvalidManifest
		}
		seenColumns[column.Name] = struct{}{}
	}

	switch operation.Kind {
	case "query":
		if !validSchemaRef(operation.ResultSchema) || len(operation.Columns) == 0 ||
			operation.MaxRows <= 0 || operation.MaxRows > manifestDatabaseMaximumRows ||
			operation.MaxAffectedRows != 0 || len(operation.QueryInvalidationTags) != 0 {
			return ErrInvalidManifest
		}
	case "execute":
		if operation.MaxRows != 0 || operation.MaxAffectedRows == 0 || operation.MaxAffectedRows > manifestDatabaseMaximumAffectedRows {
			return ErrInvalidManifest
		}
		if len(operation.Columns) == 0 && operation.ResultSchema != "" || len(operation.Columns) > 0 && !validSchemaRef(operation.ResultSchema) {
			return ErrInvalidManifest
		}
	default:
		return ErrInvalidManifest
	}
	return nil
}

func (v *v3Validator) validateServicesCommandsAdminAndQueries() error {
	for _, service := range v.manifest.Services {
		if err := v.versionedID(service.ID, service.ContractVersion, "service"); err != nil {
			return err
		}
		if !validComposableAction(service.Action) || service.Action != "add" && service.TargetID == "" || !validHandler(service.Handler) || !validSchemaRef(service.RequestSchema) || !validSchemaRef(service.ResponseSchema) {
			return ErrInvalidManifest
		}
	}
	for _, command := range v.manifest.Commands {
		if err := v.versionedID(command.ID, command.ContractVersion, "command"); err != nil {
			return err
		}
		if !validHandler(command.Handler) || !validSchemaRef(command.InputSchema) || !validSchemaRef(command.ResultSchema) ||
			command.TimeoutMS <= 0 || command.TimeoutMS > PluginCommandMaximumTimeoutMS {
			return ErrInvalidManifest
		}
		if command.Permission != "" && !manifestHasPermission(v.manifest, command.Permission) {
			return ErrInvalidManifest
		}
	}
	for _, surface := range v.manifest.AdminSurfaces {
		if err := v.versionedID(surface.ID, surface.ContractVersion, "admin_surface"); err != nil {
			return err
		}
		if !validAdminSurfaceKind(surface.Kind) || !validSurfaceAction(surface.Action) || surface.Action != "add" && surface.TargetID == "" || surface.Label == "" {
			return ErrInvalidManifest
		}
		if surface.Handler == "" && surface.Schema == "" {
			return ErrInvalidManifest
		}
		if surface.Operation != AdminSurfaceOperationQuery && surface.Operation != AdminSurfaceOperationCommand {
			return ErrInvalidManifest
		}
		if surface.Schema != "" && (!validSchemaRef(surface.Schema) || surface.PropsSchema != surface.Schema || surface.ResultSchema != surface.Schema) {
			return ErrInvalidManifest
		}
		typedContract := surface.PlacementID != "" || surface.PlacementContractVersion != "" ||
			surface.Schema == "" && (surface.PropsSchema != "" || surface.ResultSchema != "")
		if typedContract && (surface.Handler == "" || !validSchemaRef(surface.PropsSchema) || !validSchemaRef(surface.ResultSchema) ||
			!validAdminSurfacePlacement(surface.PlacementID, surface.PlacementContractVersion)) {
			return ErrInvalidManifest
		}
		if surface.Handler != "" && !validHandler(surface.Handler) {
			return ErrInvalidManifest
		}
		if surface.Permission != "" && !manifestHasPermission(v.manifest, surface.Permission) {
			return ErrInvalidManifest
		}
	}
	if len(v.manifest.Queries) > ManifestQueryMaximumDeclarations ||
		len(v.manifest.QueryResultFilters) > ManifestQueryResultFilterMaximum {
		return ErrInvalidManifest
	}
	hasExecutableQuery := false
	queries := make(map[string]ManifestQuery, len(v.manifest.Queries))
	for _, query := range v.manifest.Queries {
		if err := v.versionedID(query.ID, query.ContractVersion, "query"); err != nil {
			return err
		}
		if query.Entity == "" || !validContractVersion(query.PlanVersion) || len(query.Fields) == 0 || !validSchemaRef(query.ResultSchema) || query.PermissionPolicy == "" {
			return ErrInvalidManifest
		}
		if !validManifestQueryCacheTags(v.manifest.ID, query.CacheTags) {
			return ErrInvalidManifest
		}
		switch query.Pagination {
		case "none", "offset", "cursor":
		default:
			return ErrInvalidManifest
		}
		if query.PermissionPolicy != "public" && query.PermissionPolicy != "login" && !manifestHasPermission(v.manifest, query.PermissionPolicy) {
			return ErrInvalidManifest
		}
		if len(query.IdentityFields) > ManifestQueryMaximumIdentityFields || len(query.DefaultSort) > ManifestQueryMaximumSorts {
			return ErrInvalidManifest
		}
		if query.Handler == "" {
			if len(query.IdentityFields) != 0 || len(query.DefaultSort) != 0 {
				return ErrInvalidManifest
			}
		} else {
			hasExecutableQuery = true
			if !validHandler(query.Handler) || !strings.HasPrefix(query.Handler, v.manifest.ID+".") ||
				!validExecutableQueryShape(query) {
				return ErrInvalidManifest
			}
		}
		queries[query.ID] = query
	}
	if (hasExecutableQuery || len(v.manifest.QueryResultFilters) > 0) &&
		(v.manifest.Type != TypePlugin || strings.TrimSpace(v.manifest.Backend.Entry) == "" || v.manifest.Backend.ProtocolVersion != 2) {
		return ErrInvalidManifest
	}
	for _, filter := range v.manifest.QueryResultFilters {
		if err := v.versionedID(filter.ID, filter.ContractVersion, "query_result_filter"); err != nil {
			return err
		}
		if !manifestIDPattern.MatchString(filter.QueryID) || !validContractVersion(filter.QueryContractVersion) ||
			!strings.HasPrefix(filter.QueryContractVersion, filter.QueryID+"@") ||
			!validContractVersion(filter.QueryPlanVersion) || !validHandler(filter.Handler) ||
			!strings.HasPrefix(filter.Handler, v.manifest.ID+".") ||
			filter.Priority < -ManifestQueryResultFilterMaximumPriority || filter.Priority > ManifestQueryResultFilterMaximumPriority ||
			filter.TimeoutMS <= 0 || filter.TimeoutMS > ManifestQueryResultFilterMaximumTimeoutMS ||
			(filter.FailurePolicy != QueryResultFilterFailureFailClosed && filter.FailurePolicy != QueryResultFilterFailureFailOpen) {
			return ErrInvalidManifest
		}
		target, self := queries[filter.QueryID]
		if self {
			if filter.Dependency != nil || target.Handler == "" || target.ContractVersion != filter.QueryContractVersion ||
				target.PlanVersion != filter.QueryPlanVersion || len(target.IdentityFields) == 0 {
				return ErrInvalidManifest
			}
			continue
		}
		if filter.Dependency == nil || filter.Dependency.ExtensionID == v.manifest.ID ||
			!manifestIDPattern.MatchString(filter.Dependency.ExtensionID) ||
			!strings.HasPrefix(filter.QueryID, filter.Dependency.ExtensionID+".") ||
			!validSemverConstraint(filter.Dependency.VersionConstraint) ||
			!manifestHasQueryResultFilterDependency(v.manifest, filter) {
			return ErrInvalidManifest
		}
	}
	return nil
}

func validExecutableQueryShape(query ManifestQuery) bool {
	fields := make(map[string]struct{}, len(query.Fields))
	for _, field := range query.Fields {
		fields[field] = struct{}{}
	}
	sorts := make(map[string]struct{}, len(query.Sort))
	for _, field := range query.Sort {
		sorts[field] = struct{}{}
	}
	identities := make(map[string]struct{}, len(query.IdentityFields))
	for _, field := range query.IdentityFields {
		if _, duplicate := identities[field]; field == "" || duplicate {
			return false
		}
		if _, ok := fields[field]; !ok {
			return false
		}
		if _, ok := sorts[field]; !ok {
			return false
		}
		identities[field] = struct{}{}
	}
	seenSorts := make(map[string]struct{}, len(query.DefaultSort))
	for _, item := range query.DefaultSort {
		if _, duplicate := seenSorts[item.Field]; item.Field == "" || duplicate {
			return false
		}
		if _, ok := sorts[item.Field]; !ok {
			return false
		}
		seenSorts[item.Field] = struct{}{}
	}
	if query.Pagination == "none" {
		return true
	}
	if len(query.IdentityFields) == 0 || len(query.DefaultSort) < len(query.IdentityFields) {
		return false
	}
	offset := len(query.DefaultSort) - len(query.IdentityFields)
	for index, identity := range query.IdentityFields {
		if query.DefaultSort[offset+index].Field != identity {
			return false
		}
	}
	return true
}

func validManifestQueryCacheTags(owner string, tags []string) bool {
	if len(tags) > ManifestQueryMaximumCacheTags {
		return false
	}
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		if !manifestIDPattern.MatchString(tag) || !strings.HasPrefix(tag, owner+".") {
			return false
		}
		if _, duplicate := seen[tag]; duplicate {
			return false
		}
		seen[tag] = struct{}{}
	}
	return true
}

func manifestHasQueryResultFilterDependency(manifest Manifest, filter ManifestQueryResultFilter) bool {
	for _, dependency := range manifest.Dependencies {
		if dependency.ID != filter.Dependency.ExtensionID || dependency.Version != filter.Dependency.VersionConstraint {
			continue
		}
		if dependency.Kind == "required" || dependency.Kind == "optional" && filter.FailurePolicy == QueryResultFilterFailureFailOpen {
			return true
		}
	}
	return false
}

func (v *v3Validator) validateIdentityAndPermissions() error {
	for _, permission := range v.manifest.PermissionDefinitions {
		if err := v.versionedID(permission.Key, permission.ContractVersion, "permission"); err != nil {
			return err
		}
		// trustLevelEarnable 的三条约束**都不在这里**，原因是它们各有更合适的落点：
		//   - 「必须在 permissionDefinitions 内」由结构保证（字段就长在定义上），
		//     运行时只从这份定义里取，不存在第二个声明入口；
		//   - 「只允许插件」同样由结构保证：permissionDefinitions 只对 plugin 合法
		//     （主题带它会在类型校验就被拒），所以本字段在主题上根本到不了这里；
		//   - 「不得与核心权限键冲突」需要核心权限表，而本包是 Support 层，不能 import
		//     `Models/Identity`（依赖方向）。落点在 trustlevels 解析声明时过滤：核心键
		//     永远由核心说了算，插件的同名声明被忽略而不是覆盖。
		if permission.Label.IsEmpty() || permission.Description.IsEmpty() || permission.AssignmentPolicy != "host" {
			return ErrInvalidManifest
		}
		if len(permission.RecommendedRoles) > manifestIdentityMaximumRoleSuggestions {
			return ErrInvalidManifest
		}
		seenRoles := map[string]bool{}
		for _, role := range permission.RecommendedRoles {
			role = NormalizeID(role)
			// super_admin 在 Host policy 中始终拥有全部权限，不是插件可建议的
			// 普通角色映射目标；保留这个边界也避免 UI 把建议误读成授权。
			if !manifestIDPattern.MatchString(role) || role == "super_admin" || seenRoles[role] {
				return ErrInvalidManifest
			}
			seenRoles[role] = true
		}
	}
	identity := v.manifest.Identity
	if identity == nil {
		return nil
	}
	if !validContractVersion(identity.ContractVersion) {
		return ErrInvalidManifest
	}
	if identity.SessionPolicy != "" && identity.SessionPolicy != "core.session.default" &&
		!strings.HasPrefix(identity.SessionPolicy, v.manifest.ID+".") {
		return ErrInvalidManifest
	}
	if len(identity.RiskHooks) > manifestIdentityMaximumRiskHooks {
		return ErrInvalidManifest
	}
	seenRiskHooks := map[string]bool{}
	for _, hook := range identity.RiskHooks {
		if !manifestIDPattern.MatchString(hook) || !strings.HasPrefix(hook, v.manifest.ID+".") || seenRiskHooks[hook] {
			return ErrInvalidManifest
		}
		seenRiskHooks[hook] = true
	}
	for _, field := range identity.UserFields {
		if err := v.versionedID(field.ID, field.ContractVersion, "identity_field"); err != nil {
			return err
		}
		if !validSchemaRef(field.Schema) {
			return ErrInvalidManifest
		}
		switch field.Type {
		case "string", "number", "boolean", "object", "array":
		default:
			return ErrInvalidManifest
		}
		for _, permission := range []string{field.ReadPermission, field.WritePermission} {
			if permission != "" && !manifestHasPermission(v.manifest, permission) {
				return ErrInvalidManifest
			}
		}
	}
	hasExecutableProvider := false
	for _, provider := range identity.Providers {
		if err := v.versionedID(provider.ID, provider.ContractVersion, "identity_provider"); err != nil {
			return err
		}
		if !validHandler(provider.Handler) {
			return ErrInvalidManifest
		}
		switch provider.Kind {
		case "auth", "profile", "recovery", "session", "risk":
		default:
			return ErrInvalidManifest
		}
		if len(provider.Operations) > ManifestIdentityProviderMaximumOperations {
			return ErrInvalidManifest
		}
		seenOperations := make(map[string]struct{}, len(provider.Operations))
		for _, operation := range provider.Operations {
			expectedPolicy, known := identityProviderOperationPolicy(provider.Kind, operation.Name)
			if _, duplicate := seenOperations[operation.Name]; !known || duplicate ||
				!validSchemaRef(operation.InputSchema) || !validSchemaRef(operation.OutputSchema) ||
				operation.TimeoutMS <= 0 || operation.TimeoutMS > ManifestIdentityProviderMaximumTimeoutMS ||
				operation.FailurePolicy != expectedPolicy {
				return ErrInvalidManifest
			}
			seenOperations[operation.Name] = struct{}{}
			hasExecutableProvider = true
		}
	}
	if !validManifestIdentitySessionPolicy(*identity) {
		return ErrInvalidManifest
	}
	if hasExecutableProvider && (v.manifest.Type != TypePlugin ||
		strings.TrimSpace(v.manifest.Backend.Entry) == "" || v.manifest.Backend.ProtocolVersion != 2) {
		return ErrInvalidManifest
	}
	return nil
}

func validManifestIdentitySessionPolicy(identity ManifestIdentity) bool {
	if identity.SessionPolicy == "" || identity.SessionPolicy == "core.session.default" {
		return true
	}
	for _, provider := range identity.Providers {
		if provider.ID != identity.SessionPolicy || provider.Kind != "session" {
			continue
		}
		for _, operation := range provider.Operations {
			if operation.Name == "session.evaluate" {
				return true
			}
		}
		return false
	}
	return false
}

func identityProviderOperationPolicy(kind, name string) (string, bool) {
	switch kind + ":" + name {
	case "profile:sections.list", "profile:section.read":
		return IdentityProviderFailureOmit, true
	case "auth:registration.start", "auth:registration.complete",
		"auth:login.start", "auth:login.complete", "auth:link.start", "auth:link.complete",
		// provider.probe：有界配置/可达性探测（T8B）；fail_closed，不得作为业务效应入口。
		"auth:provider.probe",
		"profile:section.update", "profile:account.read", "profile:account.update",
		"recovery:recovery.start", "recovery:recovery.complete",
		"session:session.evaluate", "risk:risk.evaluate":
		return IdentityProviderFailureFailClosed, true
	default:
		return "", false
	}
}

func (v *v3Validator) validateMediaNavigationAndRegions() error {
	for _, media := range v.manifest.Media {
		if err := v.versionedID(media.ID, media.ContractVersion, "media"); err != nil {
			return err
		}
		if !validComposableAction(media.Action) || media.Action != "add" && media.TargetID == "" || len(media.MIMEs) == 0 || !validHandler(media.Handler) {
			return ErrInvalidManifest
		}
		if media.Permission != "" && !manifestHasPermission(v.manifest, media.Permission) {
			return ErrInvalidManifest
		}
		seenTransforms := map[string]bool{}
		for _, transform := range media.Transforms {
			if transform.ID == "" || transform.Variant == "" || transform.Width < 0 || transform.Height < 0 || seenTransforms[transform.ID] {
				return ErrInvalidManifest
			}
			seenTransforms[transform.ID] = true
		}
	}
	for _, navigation := range v.manifest.Navigation {
		if err := v.versionedID(navigation.ID, navigation.ContractVersion, "navigation"); err != nil {
			return err
		}
		if !validSurfaceAction(navigation.Action) || navigation.Action != "add" && navigation.TargetID == "" || navigation.Label == "" || navigation.Order < 0 {
			return ErrInvalidManifest
		}
		switch navigation.Kind {
		// account-settings 与 NavigationRegistry 的 validNavigationKind 对齐：
		// 该 kind 只投影到个人设置侧栏，默认 visibility 由发布层抬升为 authenticated。
		case "menu", "item", "breadcrumb", "header", "footer", "sidebar", "account-settings":
		default:
			return ErrInvalidManifest
		}
		if navigation.Href != "" && !safeHostLinkPath(navigation.Href) {
			return ErrInvalidManifest
		}
		if navigation.Permission != "" && !manifestHasPermission(v.manifest, navigation.Permission) {
			return ErrInvalidManifest
		}
	}
	for _, region := range v.manifest.Regions {
		if err := v.versionedID(region.ID, region.ContractVersion, "region"); err != nil {
			return err
		}
		if !validSurfaceAction(region.Action) || region.Action != "add" && region.TargetID == "" || region.Label == "" {
			return ErrInvalidManifest
		}
		switch region.Kind {
		case "menu", "widget", "header", "footer", "sidebar", "content":
		default:
			return ErrInvalidManifest
		}
	}
	return nil
}

func (v *v3Validator) validateDependenciesAndLifecycle() error {
	seen := map[string]bool{}
	for _, dependency := range v.manifest.Dependencies {
		switch dependency.Kind {
		case "required", "optional", "conflict":
			if !validSemverConstraint(dependency.Version) || (dependency.ID == "") == (dependency.Capability == "") || dependency.ID == v.manifest.ID {
				return ErrInvalidManifest
			}
		case "provides":
			if !validSemverVersion(dependency.Version) || dependency.ID != "" || !manifestIDPattern.MatchString(dependency.Capability) {
				return ErrInvalidManifest
			}
		default:
			return ErrInvalidManifest
		}
		key := dependency.Kind + "\x00" + dependency.ID + "\x00" + dependency.Capability
		if seen[key] {
			return ErrInvalidManifest
		}
		seen[key] = true
	}
	lifecycle := v.manifest.Lifecycle
	if lifecycle == nil {
		return nil
	}
	if v.manifest.Backend.Entry == "" || !validContractVersion(lifecycle.ContractVersion) {
		return ErrInvalidManifest
	}
	for _, operation := range []*ManifestLifecycleOperation{lifecycle.Install, lifecycle.Enable, lifecycle.Disable, lifecycle.Upgrade, lifecycle.Rollback, lifecycle.Uninstall} {
		if operation == nil {
			continue
		}
		if !validHandler(operation.Plan) || !validHandler(operation.Execute) || !validSchemaRef(operation.ProgressSchema) || !validSchemaRef(operation.CheckpointSchema) {
			return ErrInvalidManifest
		}
	}
	return nil
}

func validComposableAction(value string) bool {
	return value == "add" || value == "before" || value == "after" || value == "wrap" || value == "replace"
}

func validSurfaceAction(value string) bool {
	return validComposableAction(value) || value == "hide" || value == "filter"
}

func validAdminSurfaceKind(value string) bool {
	switch value {
	case "navigation", "dashboard", "list_column", "list_filter", "row_action", "bulk_action", "form", "notice", "editor_panel", "detail_region", "importer", "exporter":
		return true
	default:
		return false
	}
}

// manifestHasPermission 判断清单是否声明了某个权限键（直接权限或权限定义）。
// 它只服务校验路径，因此与被校验的声明族放在同一个文件里，不再占 manifest.go 的体量。
func manifestHasPermission(manifest Manifest, permission string) bool {
	for _, item := range manifest.Permissions {
		if strings.TrimSpace(item) == permission {
			return true
		}
	}
	for _, item := range manifest.PermissionDefinitions {
		if strings.TrimSpace(item.Key) == permission {
			return true
		}
	}
	return false
}
