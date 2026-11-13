package extensionmanifest

// ManifestNotificationType is inert package data. The Host validates it at
// install time and publishes it only with the exact enabled artifact.
type ManifestNotificationType struct {
	ID                  string        `json:"id"`
	ContractVersion     string        `json:"contractVersion"`
	PayloadVersion      int           `json:"payloadVersion"`
	Category            string        `json:"category"`
	PayloadSchema       string        `json:"payloadSchema"`
	Label               LocalizedText `json:"label"`
	Body                LocalizedText `json:"body"`
	Icon                string        `json:"icon,omitempty"`
	TargetKind          string        `json:"targetKind"`
	TargetID            string        `json:"targetId,omitempty"`
	Channels            []string      `json:"channels"`
	RecommendedChannels []string      `json:"recommendedChannels,omitempty"`
	Required            bool          `json:"required,omitempty"`
	// Celebration 让插件声明「这条通知值得庆祝一次」（presentation.celebration）。
	//
	// 只是展示层开关：不新增 Host 命令、不改渠道/偏好/去重语义，
	// 也不能让插件自建庆祝读面（读面恒为 Core 的 GET /notifications/celebrations）。
	Celebration bool `json:"celebration,omitempty"`
}

type ManifestDatabase struct {
	ContractVersion string `json:"contractVersion"`
	// Authority is accepted only as a legacy manifest input. Normalize expands
	// it into the cumulative exact Grants set before runtime consumers use it.
	Authority         string                      `json:"authority,omitempty"`
	Grants            []string                    `json:"grants,omitempty"`
	Schema            string                      `json:"schema,omitempty"`
	Role              string                      `json:"role,omitempty"`
	CoreCompatibility string                      `json:"coreCompatibility,omitempty"`
	Backup            ManifestBackupPolicy        `json:"backup"`
	Retention         ManifestRetention           `json:"retention"`
	Operations        []ManifestDatabaseOperation `json:"operations,omitempty"`
}

// ManifestDatabaseOperation 将一个宿主管理的 SQL 文件绑定到有界运行时契约。
// SQL 字节不得内联到 manifest，也不会穿过 RPC 边界。
type ManifestDatabaseOperation struct {
	ID               string                      `json:"id"`
	StatementVersion string                      `json:"statementVersion"`
	Kind             string                      `json:"kind"`
	Path             string                      `json:"path"`
	Digest           string                      `json:"digest"`
	Parameters       []ManifestDatabaseParameter `json:"parameters"`
	ResultSchema     string                      `json:"resultSchema,omitempty"`
	Columns          []ManifestDatabaseColumn    `json:"columns"`
	MaxRows          int                         `json:"maxRows,omitempty"`
	MaxAffectedRows  uint64                      `json:"maxAffectedRows,omitempty"`
	// QueryInvalidationTags are fixed semantic cache tags rotated only after a
	// successful execute commits. Query operations cannot declare them.
	QueryInvalidationTags []string `json:"queryInvalidationTags,omitempty"`
	TimeoutMS             int      `json:"timeoutMs"`
}

type ManifestDatabaseParameter struct {
	Schema   string `json:"schema"`
	Field    string `json:"field"`
	Kind     string `json:"kind"`
	Nullable bool   `json:"nullable"`
	MaxBytes int    `json:"maxBytes"`
}

type ManifestDatabaseColumn struct {
	Name     string `json:"name"`
	Nullable bool   `json:"nullable"`
}

type ManifestBackupPolicy struct {
	Required bool   `json:"required"`
	Strategy string `json:"strategy,omitempty"`
}

type ManifestRetention struct {
	OnDisable   string `json:"onDisable"`
	OnUninstall string `json:"onUninstall"`
	Days        int    `json:"days,omitempty"`
}

type ManifestCache struct {
	ID              string   `json:"id"`
	ContractVersion string   `json:"contractVersion"`
	Namespace       string   `json:"namespace"`
	Policy          string   `json:"policy"`
	Tags            []string `json:"tags,omitempty"`
	Provider        string   `json:"provider,omitempty"`
	Invalidators    []string `json:"invalidators,omitempty"`
}

// ManifestSEO mirrors sforum.seo-registry@1. Scope is a stable Host page or
// route id (or "global"); executable output remains constrained by the typed
// SEO document and the non-overridable Host final policy.
type ManifestSEO struct {
	ID              string `json:"id"`
	ContractVersion string `json:"contractVersion"`
	Scope           string `json:"scope"`
	Kind            string `json:"kind"`
	Action          string `json:"action"`
	Handler         string `json:"handler"`
	Priority        int    `json:"priority,omitempty"`
	FailurePolicy   string `json:"failurePolicy"`
	TimeoutMS       int    `json:"timeoutMs,omitempty"`
}

type ManifestAdminSurface struct {
	ID                       string `json:"id"`
	ContractVersion          string `json:"contractVersion"`
	Kind                     string `json:"kind"`
	Action                   string `json:"action"`
	TargetID                 string `json:"targetId,omitempty"`
	PlacementID              string `json:"placementId,omitempty"`
	PlacementContractVersion string `json:"placementContractVersion,omitempty"`
	Label                    string `json:"label"`
	Handler                  string `json:"handler,omitempty"`
	PropsSchema              string `json:"propsSchema,omitempty"`
	ResultSchema             string `json:"resultSchema,omitempty"`
	Operation                string `json:"operation,omitempty"`
	// Schema is the legacy single-document contract. Normalize maps it to both
	// PropsSchema and ResultSchema so installed V3 packages remain readable.
	Schema     string `json:"schema,omitempty"`
	Permission string `json:"permission,omitempty"`
	Priority   int    `json:"priority,omitempty"`
}

const (
	AdminSurfaceOperationQuery   = "query"
	AdminSurfaceOperationCommand = "command"
)

type ManifestQuery struct {
	ID               string   `json:"id"`
	ContractVersion  string   `json:"contractVersion"`
	Entity           string   `json:"entity"`
	PlanVersion      string   `json:"planVersion"`
	Fields           []string `json:"fields"`
	Relations        []string `json:"relations,omitempty"`
	Filters          []string `json:"filters,omitempty"`
	Sort             []string `json:"sort,omitempty"`
	Pagination       string   `json:"pagination"`
	ResultSchema     string   `json:"resultSchema"`
	PermissionPolicy string   `json:"permissionPolicy"`
	CacheTags        []string `json:"cacheTags,omitempty"`
	// Handler explicitly opts this declaration into Host-to-plugin execution.
	// Empty remains the compatible inspect/plan-only contract.
	Handler        string              `json:"handler,omitempty"`
	IdentityFields []string            `json:"identityFields,omitempty"`
	DefaultSort    []ManifestQuerySort `json:"defaultSort,omitempty"`
}

type ManifestQuerySort struct {
	Field      string `json:"field"`
	Descending bool   `json:"descending,omitempty"`
}

type ManifestQueryResultFilter struct {
	ID                   string                               `json:"id"`
	ContractVersion      string                               `json:"contractVersion"`
	QueryID              string                               `json:"queryId"`
	QueryContractVersion string                               `json:"queryContractVersion"`
	QueryPlanVersion     string                               `json:"queryPlanVersion"`
	Handler              string                               `json:"handler"`
	Priority             int                                  `json:"priority,omitempty"`
	FailurePolicy        string                               `json:"failurePolicy,omitempty"`
	TimeoutMS            int                                  `json:"timeoutMs,omitempty"`
	Dependency           *ManifestQueryResultFilterDependency `json:"dependency,omitempty"`
}

type ManifestQueryResultFilterDependency struct {
	ExtensionID       string `json:"extensionId"`
	VersionConstraint string `json:"versionConstraint"`
}

type ManifestIdentity struct {
	ContractVersion string                      `json:"contractVersion"`
	UserFields      []ManifestIdentityUserField `json:"userFields,omitempty"`
	Providers       []ManifestIdentityProvider  `json:"providers,omitempty"`
	SessionPolicy   string                      `json:"sessionPolicy,omitempty"`
	RiskHooks       []string                    `json:"riskHooks,omitempty"`
}

type ManifestIdentityUserField struct {
	ID              string `json:"id"`
	ContractVersion string `json:"contractVersion"`
	Type            string `json:"type"`
	Schema          string `json:"schema"`
	ReadPermission  string `json:"readPermission,omitempty"`
	WritePermission string `json:"writePermission,omitempty"`
}

type ManifestIdentityProvider struct {
	ID              string `json:"id"`
	ContractVersion string `json:"contractVersion"`
	Kind            string `json:"kind"`
	Handler         string `json:"handler"`
	Priority        int    `json:"priority,omitempty"`
	// Label 是插件声明的公共展示名（LocalizedText：字符串或 locale map）。
	// 使用指针以便 omitempty 在未声明时真正省略（struct 零值不会被 omitempty 跳过）。
	// Host 公共 catalog 按 Accept-Language 解析后注入前端；Core 不硬编码供应商品牌。
	Label *LocalizedText `json:"label,omitempty"`
	// Icon 为 Iconify / Nuxt Icon 名称（如 i-tabler-brand-github）；空则 Host 壳用通用图标。
	Icon string `json:"icon,omitempty"`
	// Operations 为空时保持既有只读目录语义；非空才允许 Host 调用。
	Operations []ManifestIdentityProviderOperation `json:"operations,omitempty"`
}

type ManifestIdentityProviderOperation struct {
	Name          string `json:"name"`
	InputSchema   string `json:"inputSchema"`
	OutputSchema  string `json:"outputSchema"`
	TimeoutMS     int    `json:"timeoutMs,omitempty"`
	FailurePolicy string `json:"failurePolicy,omitempty"`
}

type ManifestPermissionDefinition struct {
	Key              string        `json:"key"`
	ContractVersion  string        `json:"contractVersion"`
	Label            LocalizedText `json:"label"`
	Description      LocalizedText `json:"description"`
	RecommendedRoles []string      `json:"recommendedRoles,omitempty"`
	AssignmentPolicy string        `json:"assignmentPolicy"`
}

type ManifestMediaPipeline struct {
	ID              string                   `json:"id"`
	ContractVersion string                   `json:"contractVersion"`
	Action          string                   `json:"action"`
	TargetID        string                   `json:"targetId,omitempty"`
	MIMEs           []string                 `json:"mimes"`
	Transforms      []ManifestMediaTransform `json:"transforms,omitempty"`
	Handler         string                   `json:"handler"`
	Permission      string                   `json:"permission,omitempty"`
	Priority        int                      `json:"priority,omitempty"`
}

type ManifestMediaTransform struct {
	ID      string `json:"id"`
	Variant string `json:"variant"`
	Format  string `json:"format,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
}

type ManifestNavigation struct {
	ID              string `json:"id"`
	ContractVersion string `json:"contractVersion"`
	Kind            string `json:"kind"`
	Action          string `json:"action"`
	TargetID        string `json:"targetId,omitempty"`
	Label           string `json:"label"`
	Href            string `json:"href,omitempty"`
	Permission      string `json:"permission,omitempty"`
	Visibility      string `json:"visibility,omitempty"`
	OwnerResource   string `json:"ownerResource,omitempty"`
	Order           int    `json:"order,omitempty"`
}

type ManifestRegion struct {
	ID              string `json:"id"`
	ContractVersion string `json:"contractVersion"`
	Action          string `json:"action"`
	TargetID        string `json:"targetId,omitempty"`
	Kind            string `json:"kind"`
	Label           string `json:"label"`
	Multiple        bool   `json:"multiple,omitempty"`
}

type ManifestDependency struct {
	ID         string `json:"id,omitempty"`
	Capability string `json:"capability,omitempty"`
	Version    string `json:"version"`
	Kind       string `json:"kind"`
}

type ManifestLifecycle struct {
	ContractVersion string                      `json:"contractVersion"`
	Install         *ManifestLifecycleOperation `json:"install,omitempty"`
	Enable          *ManifestLifecycleOperation `json:"enable,omitempty"`
	Disable         *ManifestLifecycleOperation `json:"disable,omitempty"`
	Upgrade         *ManifestLifecycleOperation `json:"upgrade,omitempty"`
	Rollback        *ManifestLifecycleOperation `json:"rollback,omitempty"`
	Uninstall       *ManifestLifecycleOperation `json:"uninstall,omitempty"`
}

type ManifestLifecycleOperation struct {
	Plan             string `json:"plan"`
	Execute          string `json:"execute"`
	ProgressSchema   string `json:"progressSchema"`
	CheckpointSchema string `json:"checkpointSchema"`
}
