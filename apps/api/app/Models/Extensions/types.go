package extensions

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
)

const (
	ManifestFileName = extensionmanifest.ManifestFileName
	DefaultThemeID   = "sforum.default-theme"

	// MaxArchiveBytes is the hard ceiling for a stored extension archive.
	// URL downloads use the same ceiling so an oversize body fails before parsing.
	MaxArchiveBytes = maxArchiveBytes

	TypePlugin = extensionmanifest.TypePlugin
	TypeTheme  = extensionmanifest.TypeTheme

	StatusInstalled = "installed"
	StatusEnabled   = "enabled"
	StatusDisabled  = "disabled"

	RuntimeStopped  = "stopped"
	RuntimeStarting = "starting"
	RuntimeRunning  = "running"
	// RuntimeDegraded 进程仍在，但熔断打开或近期连续失败（F2.3）。
	RuntimeDegraded = "degraded"
	RuntimeFailed   = "failed"

	RouteAccessPublic            = extensionmanifest.RouteAccessPublic
	RouteAccessLogin             = extensionmanifest.RouteAccessLogin
	RouteAccessPermission        = extensionmanifest.RouteAccessPermission
	AdminSurfaceOperationQuery   = extensionmanifest.AdminSurfaceOperationQuery
	AdminSurfaceOperationCommand = extensionmanifest.AdminSurfaceOperationCommand

	EventInstalled             = "installed"
	EventUpgraded              = "upgraded"
	EventUninstalled           = "uninstalled"
	EventBuiltinSynced         = "builtin_synced"
	EventVerified              = "verified"
	EventEnabled               = "enabled"
	EventEnableFailed          = "enable_failed"
	EventDisabled              = "disabled"
	EventRolledBack            = "rolled_back"
	EventThemeActivated        = "theme_activated"
	EventThemeActivationQueued = "theme_activation_queued"
	EventMigrationsApplied     = "migrations_applied"

	// EventDevAutoSkipped 记录 dev 自动机主动放弃一次激活（冻结制品不可装载，
	// 或同一摘要已被自动恢复过上限次数）。它不是失败：不再重试才不再刷台账。
	EventDevAutoSkipped = "dev_auto_skipped"

	// EventBuiltinAutoUpdated 记录 Host 在启动阶段自动升级受保护内置插件。
	EventBuiltinAutoUpdated = "builtin_auto_updated"
	// EventBuiltinAutoUpdateSkipped 记录自动升级被安全闸跳过（provider 正在使用、
	// 版本不前进、制品不可用等）。它不是失败，下一轮启动会重新评估。
	EventBuiltinAutoUpdateSkipped = "builtin_auto_update_skipped"
	// EventBuiltinAutoUpdateFailed 记录自动升级执行失败；旧 active 继续服务或
	// 插件停在安全停用态，等待管理员重试/回滚。
	EventBuiltinAutoUpdateFailed = "builtin_auto_update_failed"

	DeliveryQueued    = "queued"
	DeliveryRunning   = "running"
	DeliverySucceeded = "succeeded"
	DeliveryFailed    = "failed"
	DeliverySkipped   = "skipped"

	CodeInvalidArchive          = "extension.archive_invalid"
	CodeInvalidManifest         = "extension.manifest_invalid"
	CodeArtifactMissing         = "extension.artifact_missing"
	CodeNotFound                = "extension.not_found"
	CodePreflightFailed         = "extension.preflight_failed"
	CodeBuildFailed             = "extension.build_failed"
	CodeThemeActivationRequired = "extension.theme_activation_required"
	CodeThemeRuntimeUnavailable = "extension.theme_runtime_unavailable"
	CodeThemePreviewStale       = "extension.theme_preview_stale"
	CodeRouteNotFound           = "extension.route_not_found"
	CodeRouteMethodNotAllowed   = "extension.route_method_not_allowed"
	CodeRuntimeUnavailable      = "extension.runtime_unavailable"
	CodeRuntimeFailed           = "extension.runtime_failed"
	// desired-set / plugin runtime publication 冲突或历史投影不可读。
	CodePluginRuntimeConflict      = "extension.plugin_runtime_conflict"
	CodeFrontendRuntimeUnavailable = "extension.frontend_runtime_unavailable"
	CodeFrontendDigestInvalid      = "extension.frontend_digest_invalid"
	CodeFrontendPackageChanged     = "extension.frontend_package_changed"
	CodeFrontendTrustNotFound      = "extension.frontend_trust_not_found"
	// 插件已禁用：设置读写、自定义管理页等功能性能力不可用。
	CodeExtensionDisabled = "extension.disabled"
	// 设置重启预检失败时不得写入；写入后的重启失败必须明确告知设置已持久化。
	CodeSettingsRevisionConflict   = "extension.settings_revision_conflict"
	CodeSettingsRestartUnavailable = "extension.settings_restart_unavailable"
	CodeSettingsRestartFailed      = "extension.settings_restart_failed"
	CodeSettingsRollbackFailed     = "extension.settings_rollback_failed"
	CodeSettingsActionInvalid      = "extension.settings_action_invalid"
	CodeSettingsActionUnavailable  = "extension.settings_action_unavailable"
	// 启用前需运营确认 capability 授权（F2.1）。
	CodeCapabilityConfirmationRequired = "extension.capability_confirmation_required"
	// CodeFeaturesRequired F4.5：站点产品开关未满足 requiresFeatures。
	CodeFeaturesRequired = "extension.features_required"
	// 运行时缺少已授权 capability。
	CodeCapabilityDenied = "extension.capability_denied"
	// 系统/内置扩展不可卸载。
	CodeNotDeletable = "extension.not_deletable"
	// 卸载前必须先禁用（或 drain 失败）。
	CodeMustDisableFirst = "extension.must_disable_first"
	// 插件迁移执行失败。
	CodeMigrationFailed        = "extension.migration_failed"
	CodeSafeModeActive         = "extension.safe_mode_active"
	CodeLifecycleInvalid       = "extension.lifecycle_invalid"
	CodeLifecycleUnavailable   = "extension.lifecycle_unavailable"
	CodeLifecycleConflict      = "extension.lifecycle_conflict"
	CodeLifecycleActionFailed  = "extension.lifecycle_action_failed"
	CodeLifecycleAuthorityGone = "extension.lifecycle_authority_not_found"
	CodeLifecycleCleanupFailed = "extension.lifecycle_cleanup_failed"
	// CodeExpectedMismatch 表示下载包的清单身份与调用方声明不一致。
	CodeExpectedMismatch      = "extension.expected_mismatch"
	CodeStagedVersionNotFound = "extension.staged_version_not_found"
	CodeVersionNotFound       = "extension.version_not_found"
	// CodeCoreIncompatible 表示清单 sforumVersion 不覆盖运行中的 coreVersion。
	CodeCoreIncompatible = "extension.core_incompatible"
	// CodePlatformIncompatible 表示后端可执行文件不是为当前宿主平台构建的。
	// 安装阶段是 422（这个包本身不可用），运行阶段是 409（当前状态冲突）。
	CodePlatformIncompatible = "extension.platform_incompatible"
	// CodeUntrustedBackendRestricted 见 backend_trust.go（非 super_admin 执行非内置后端）。

	SourceBuiltin  = "builtin"
	SourceUploaded = "uploaded"

	ArtifactAvailable = "available"
	ArtifactMissing   = "missing"
)

var (
	ErrInvalidArchive             = errors.New("extensions: invalid archive")
	ErrInvalidManifest            = extensionmanifest.ErrInvalidManifest
	ErrArtifactMissing            = errors.New("extensions: artifact missing")
	ErrExtensionNotFound          = errors.New("extensions: not found")
	ErrExtensionDisabled          = errors.New("extensions: disabled")
	ErrSettingsRestartUnavailable = errors.New("extensions: settings restart is unavailable")
	ErrSettingsRestartFailed      = errors.New("extensions: settings persisted but restart failed")
	ErrSettingsRollbackFailed     = errors.New("extensions: settings rollback failed")
	ErrSettingsCommitUnknown      = errors.New("extensions: settings commit outcome is unknown")
	ErrSettingsActionInvalid      = errors.New("extensions: settings action invalid")
	ErrSettingsActionUnavailable  = errors.New("extensions: settings action unavailable")
	ErrPreflightFailed            = errors.New("extensions: preflight failed")
	ErrBuildFailed                = errors.New("extensions: build failed")
	ErrThemeActivationRequired    = errors.New("extensions: themes must be activated")
	ErrThemeRuntimeUnavailable    = errors.New("extensions: theme activation runtime unavailable")
	ErrThemePreviewStale          = errors.New("extensions: theme activation preview is stale")
	ErrRuntimeFailed              = errors.New("extensions: runtime failed")
	ErrRouteNotFound              = errors.New("extensions: route not found")
	ErrRouteMethodNotAllowed      = errors.New("extensions: route method not allowed")
	ErrRuntimeUnavailable         = errors.New("extensions: runtime unavailable")
	// ErrCapabilityConfirmationRequired 启用插件前需 confirmCapabilities=true。
	ErrCapabilityConfirmationRequired = errors.New("extensions: capability confirmation required")
	ErrCapabilityDenied               = errors.New("extensions: capability denied")
	// ErrFeaturesRequired 站点产品开关未满足 manifest requiresFeatures（F4.5）。
	ErrFeaturesRequired = errors.New("extensions: required features disabled")
	ErrNotDeletable     = errors.New("extensions: not deletable")
	ErrMustDisableFirst = errors.New("extensions: must disable before uninstall")
	ErrMigrationFailed  = errors.New("extensions: migration failed")
	ErrSafeModeActive   = errors.New("extensions: safe mode active")
	// ErrExecutablePlatformMismatch 表示后端可执行文件的目标平台与宿主不符。
	ErrExecutablePlatformMismatch = errors.New("extensions: backend executable targets another platform")
)

// ExecutablePlatformMismatchError 携带入口路径、探测到的目标平台和宿主平台，
// 供 API 回传结构化字段（required/host/entry）。
//
// Stage 复用 CoreCompatibilityStageInstall/CoreCompatibilityStageRuntime：两者
// 是同一条「安装 vs 运行」判定轴，不需要第二套阶段常量。
type ExecutablePlatformMismatchError struct {
	Entry    string
	Required string
	Host     string
	Stage    string
}

func (e *ExecutablePlatformMismatchError) Error() string {
	return fmt.Sprintf("%v: entry=%q required=%q host=%q stage=%s",
		ErrExecutablePlatformMismatch, e.Entry, e.Required, e.Host, e.Stage)
}

func (e *ExecutablePlatformMismatchError) Unwrap() error {
	return ErrExecutablePlatformMismatch
}

type Manifest = extensionmanifest.Manifest
type ManifestAuthor = extensionmanifest.ManifestAuthor
type ManifestSetting = extensionmanifest.ManifestSetting
type ManifestSettingOption = extensionmanifest.ManifestSettingOption
type SettingsDocument = extensionmanifest.SettingsDocument
type SettingsUI = extensionmanifest.SettingsUI
type SettingsTab = extensionmanifest.SettingsTab
type SettingsGroup = extensionmanifest.SettingsGroup
type SettingsCallout = extensionmanifest.SettingsCallout
type AdminComponent = extensionmanifest.AdminComponent
type SettingsComponent = extensionmanifest.SettingsComponent
type SettingsAction = extensionmanifest.SettingsAction
type LocalizedText = extensionmanifest.LocalizedText
type ManifestMigration = extensionmanifest.ManifestMigration
type ManifestBackend = extensionmanifest.ManifestBackend
type ManifestAdmin = extensionmanifest.ManifestAdmin
type ManifestAdminPage = extensionmanifest.ManifestAdminPage
type ManifestRoute = extensionmanifest.ManifestRoute
type ManifestHook = extensionmanifest.ManifestHook
type ManifestEvent = extensionmanifest.ManifestEvent
type ManifestJob = extensionmanifest.ManifestJob
type ManifestProvider = extensionmanifest.ManifestProvider
type ManifestContribution = extensionmanifest.ManifestContribution
type ManifestGuard = extensionmanifest.ManifestGuard
type ManifestSchedule = extensionmanifest.ManifestSchedule
type ManifestComponent = extensionmanifest.ManifestComponent
type ManifestTemplate = extensionmanifest.ManifestTemplate
type ManifestAsset = extensionmanifest.ManifestAsset
type ManifestContent = extensionmanifest.ManifestContent
type ManifestDatabase = extensionmanifest.ManifestDatabase
type ManifestCache = extensionmanifest.ManifestCache
type ManifestSEO = extensionmanifest.ManifestSEO
type ManifestService = extensionmanifest.ManifestService
type ManifestCommand = extensionmanifest.ManifestCommand
type ManifestAdminSurface = extensionmanifest.ManifestAdminSurface
type ManifestQuery = extensionmanifest.ManifestQuery
type ManifestQuerySort = extensionmanifest.ManifestQuerySort
type ManifestQueryResultFilter = extensionmanifest.ManifestQueryResultFilter
type ManifestQueryResultFilterDependency = extensionmanifest.ManifestQueryResultFilterDependency
type ManifestIdentity = extensionmanifest.ManifestIdentity
type ManifestIdentityProvider = extensionmanifest.ManifestIdentityProvider
type ManifestIdentityProviderOperation = extensionmanifest.ManifestIdentityProviderOperation
type ManifestPermissionDefinition = extensionmanifest.ManifestPermissionDefinition
type ManifestNotificationType = extensionmanifest.ManifestNotificationType
type ManifestMediaPipeline = extensionmanifest.ManifestMediaPipeline
type ManifestNavigation = extensionmanifest.ManifestNavigation
type ManifestRegion = extensionmanifest.ManifestRegion
type ManifestDependency = extensionmanifest.ManifestDependency
type ManifestLifecycle = extensionmanifest.ManifestLifecycle
type ManifestOpenAPIFragment = extensionmanifest.ManifestOpenAPIFragment
type ManifestAITool = extensionmanifest.ManifestAITool
type ManifestPackageFile = extensionmanifest.ManifestPackageFile
type TopicActionContributionPayload = extensionmanifest.TopicActionContributionPayload
type ContributionPointDefinition = extensionmanifest.ContributionPointDefinition
type Dependency = extensionpackage.Dependency
type DependencySummary = extensionpackage.DependencySummary

type RuntimeStatus struct {
	State         string     `json:"state"`
	LastError     string     `json:"lastError,omitempty"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	RouteCount    int        `json:"routeCount"`
	HookCount     int        `json:"hookCount"`
	EventCount    int        `json:"eventCount"`
	ProviderCount int        `json:"providerCount"`
	// F2.3 韧性观测字段。
	CircuitOpen         bool       `json:"circuitOpen,omitempty"`
	CircuitOpenUntil    *time.Time `json:"circuitOpenUntil,omitempty"`
	ConsecutiveFailures int        `json:"consecutiveFailures,omitempty"`
	LastFailureReason   string     `json:"lastFailureReason,omitempty"`
	LastFailureAt       *time.Time `json:"lastFailureAt,omitempty"`
	ActiveRPCCalls      int        `json:"activeRpcCalls,omitempty"`
	MaxConcurrentRPC    int        `json:"maxConcurrentRpc,omitempty"`
	// P3：协议迁移观测；v1 保持可用但必须明确标记 deprecated。
	ProtocolVersion    int        `json:"protocolVersion,omitempty"`
	ProtocolTransport  string     `json:"protocolTransport,omitempty"`
	ProtocolDeprecated bool       `json:"protocolDeprecated,omitempty"`
	ProtocolStartCount uint64     `json:"protocolStartCount,omitempty"`
	ProtocolCallCount  uint64     `json:"protocolCallCount,omitempty"`
	ProtocolLastCallAt *time.Time `json:"protocolLastCallAt,omitempty"`
	// MemoryBytes 是当前 API 拥有的 backend plugin 子进程 OS RSS（字节）。
	// 仅在能采样到进程时填充；主题/已停止/无 backend 进程时省略。
	MemoryBytes uint64 `json:"memoryBytes,omitempty"`
}

type MatchedRoute struct {
	Extension Extension
	Route     ManifestRoute
	Path      string
}

// CapabilityGrant 是启用审查用的能力条目（与 capabilities.Grant 对齐）。
type CapabilityGrant = capabilities.Grant

// ExtensionVersion 是不可变包快照。活动版本和待执行升级版本必须分开表示，
// 静态上传不得把候选制品伪装成已经生效的运行版本。
type ExtensionVersion struct {
	ID                  int64     `json:"-"`
	Version             string    `json:"version"`
	Manifest            Manifest  `json:"manifest"`
	ArtifactState       string    `json:"artifactState,omitempty"`
	PackageDigest       string    `json:"packageDigest"`
	AdminFrontendDigest string    `json:"adminFrontendDigest"`
	PackagePath         string    `json:"packagePath"`
	InstalledAt         time.Time `json:"installedAt"`
}

type Extension struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Type        string   `json:"type"`
	Status      string   `json:"status"`
	Source      string   `json:"source"`
	IsSystem    bool     `json:"isSystem"`
	IsDeletable bool     `json:"isDeletable"`
	Manifest    Manifest `json:"manifest"`
	// CapabilityGrants 有效 Host 能力（显式 + 推断），供启用审查 UI（F2.1）。
	CapabilityGrants []CapabilityGrant `json:"capabilityGrants,omitempty"`
	Runtime          *RuntimeStatus    `json:"runtime,omitempty"`
	ArtifactState    string            `json:"artifactState,omitempty"`
	// PendingUninstallCleanup 是管理端投影：该扩展存在「台账已 succeeded 但
	// 宿主物理清理未 finalized」的 pending tombstone。仅在 admin list/detail
	// 由 controller 装饰；不参与内部 List 路径。
	PendingUninstallCleanup bool              `json:"pendingUninstallCleanup,omitempty"`
	PackageDigest           string            `json:"packageDigest"`
	AdminFrontendDigest     string            `json:"adminFrontendDigest,omitempty"`
	PackagePath             string            `json:"packagePath"`
	ActiveVersionID         int64             `json:"-"`
	StagedVersion           *ExtensionVersion `json:"stagedVersion,omitempty"`
	InstalledAt             time.Time         `json:"installedAt"`
	UpdatedAt               time.Time         `json:"updatedAt"`
}

// EnableInput 启用插件的可选请求体。
type EnableInput struct {
	// ConfirmCapabilities 为 true 时表示运营已审阅并确认 capability 授权。
	ConfirmCapabilities bool `json:"confirmCapabilities"`
	// ConfirmationToken 是 V3 P1 服务端签发的一次性 exact-artifact token。
	ConfirmationToken string `json:"confirmationToken,omitempty"`
	// IdempotencyKey 来自 HTTP Idempotency-Key；不接受 body 覆盖。
	IdempotencyKey string `json:"-"`
}

// RestartInput 由 Host 编排 runtime 重启及 legacy -> Lifecycle V2 暂存制品迁移。
type RestartInput struct {
	ConfirmCapabilities bool   `json:"confirmCapabilities"`
	ConfirmationToken   string `json:"confirmationToken,omitempty"`
	IdempotencyKey      string `json:"-"`
}

type ThemeActivationInput struct {
	Version             string `json:"version"`
	PackageDigest       string `json:"packageDigest"`
	CurrentThemeID      string `json:"currentThemeId"`
	CurrentThemeVersion string `json:"currentThemeVersion"`
	CurrentThemeDigest  string `json:"currentThemeDigest"`
	// ConfirmationToken 是上传主题包含 L2/其他可执行声明时的一次性 exact-artifact token。
	ConfirmationToken string `json:"confirmationToken,omitempty"`
	// ApproveCoreReplacements is an explicit approval from the exact visible
	// preview. It is effective only for a super_admin actor.
	ApproveCoreReplacements bool  `json:"approveCoreReplacements"`
	ActorUserID             int64 `json:"-"`
}

type ThemeRuntimePublicationState string

const (
	ThemeRuntimePublicationActive ThemeRuntimePublicationState = "active"
	ThemeRuntimePublicationNone   ThemeRuntimePublicationState = "none"
)

type ThemeRuntimePublicationReason string

const (
	ThemeRuntimePublicationActivation    ThemeRuntimePublicationReason = "activation"
	ThemeRuntimePublicationCompensation  ThemeRuntimePublicationReason = "compensation"
	ThemeRuntimePublicationStartupRepair ThemeRuntimePublicationReason = "startup_repair"
)

// 核心页替换审批的来源。'admin' 必须携带真实 actor；'host' 只允许受保护内置
// 默认主题在首次安装/启动修复时使用，且不带 actor。
const (
	ThemeApprovalSourceAdmin = "admin"
	ThemeApprovalSourceHost  = "host"
)

// isProtectedBuiltinDefaultTheme 判定 Host 自带的受保护默认主题。
// 只有它可以被 Host 自动批准核心页替换：包来自发行包、is_system 且不可删除。
func isProtectedBuiltinDefaultTheme(extension Extension) bool {
	return extension.ID == DefaultThemeID && extension.Source == SourceBuiltin &&
		extension.IsSystem && !extension.IsDeletable
}

type ThemeRuntimePublication struct {
	Revision                       int64                         `json:"revision"`
	DesiredState                   ThemeRuntimePublicationState  `json:"desiredState"`
	ThemeID                        string                        `json:"themeId,omitempty"`
	ThemeVersion                   string                        `json:"themeVersion,omitempty"`
	PackageDigest                  string                        `json:"packageDigest,omitempty"`
	SourceThemeID                  string                        `json:"sourceThemeId,omitempty"`
	SourceThemeVersion             string                        `json:"sourceThemeVersion,omitempty"`
	SourcePackageDigest            string                        `json:"sourcePackageDigest,omitempty"`
	SourceCoreReplacementsApproved bool                          `json:"sourceCoreReplacementsApproved"`
	SourceActorUserID              int64                         `json:"sourceActorUserId,omitempty"`
	SourceApprovalSource           string                        `json:"sourceApprovalSource,omitempty"`
	CoreReplacementsApproved       bool                          `json:"coreReplacementsApproved"`
	ApprovalSource                 string                        `json:"approvalSource,omitempty"`
	ActorUserID                    int64                         `json:"actorUserId,omitempty"`
	Reason                         ThemeRuntimePublicationReason `json:"reason"`
	CreatedAt                      time.Time                     `json:"createdAt"`
}

type ThemeActivationResult struct {
	Extension   Extension               `json:"extension"`
	Publication ThemeRuntimePublication `json:"publication"`
}

// LifecycleRequestInput 是无业务 body 的 V2 lifecycle 请求元数据。
type LifecycleRequestInput struct {
	IdempotencyKey string `json:"-"`
}

// UpgradeInput 激活当前不可变 staged candidate。
type UpgradeInput struct {
	ConfirmationToken string `json:"confirmationToken,omitempty"`
	IdempotencyKey    string `json:"-"`
}

// RollbackInput 只接受 exact historical version + digest，禁止“最近版本”一类可变指针。
type RollbackInput struct {
	TargetVersion       string `json:"targetVersion"`
	TargetPackageDigest string `json:"targetPackageDigest"`
	IdempotencyKey      string `json:"-"`
}

// UninstallInput 卸载扩展的请求体（F2.4）。
type UninstallInput struct {
	// RemovalMode 是 Lifecycle V2 的数据去向。空值使用安全默认 preserve。
	RemovalMode string `json:"removalMode,omitempty"`
	// RetainSettings 为 true 时保留 extension_settings（便于同 id 重装恢复配置）。
	// 仅供 Lifecycle V1 兼容路径使用。
	RetainSettings bool `json:"retainSettings"`
	// RetainPackage 为 true 时保留磁盘上的包快照目录；默认删除。
	// 仅供 Lifecycle V1 兼容路径使用。
	RetainPackage bool `json:"retainPackage"`
	// IdempotencyKey 来自 HTTP Idempotency-Key；不接受 body 覆盖。
	IdempotencyKey string `json:"-"`
}

// LifecycleCleanupFinalization 是 Host exact-receipt finalizer 的最小跨层结果。
type LifecycleCleanupFinalization struct {
	OperationID int64  `json:"operationId"`
	Status      string `json:"status"`
	// PhysicalPurgeComplete 只描述"物理身份行被删了没有"，不参与逻辑收敛判断：
	// identityRetained 的卸载物理身份仍在（publication 是 append-only 证据），
	// 但 cleanup record 一样收敛到 finalized。收敛判据请只看 Status
	// （见 service_lifecycle_v2 的调用点）。
	PhysicalPurgeComplete bool `json:"physicalPurgeComplete"`
}

type UninstallResult struct {
	Uninstalled bool                          `json:"uninstalled"`
	ExtensionID string                        `json:"extensionId"`
	OperationID int64                         `json:"operationId,omitempty"`
	RemovalMode string                        `json:"removalMode,omitempty"`
	Replayed    bool                          `json:"replayed,omitempty"`
	Cleanup     *LifecycleCleanupFinalization `json:"cleanup,omitempty"`
}

// LifecycleRecoveryInput 精确恢复原 operation，不会创建新幂等域。
type LifecycleRecoveryInput struct {
	Decision       string `json:"decision"`
	Reason         string `json:"reason,omitempty"`
	EscalateForced bool   `json:"escalateForced,omitempty"`
}

// InstallResult 安装/升级结果。
type InstallResult struct {
	Extension Extension `json:"extension"`
	// Upgraded 为 true 表示同 id 覆盖安装（digest/version 变更路径）。
	Upgraded bool `json:"upgraded"`
	// PreviousVersion 升级前版本（若有）。
	PreviousVersion string `json:"previousVersion,omitempty"`
	// PreviousDigest 升级前包摘要。
	PreviousDigest string `json:"previousDigest,omitempty"`
	// TrustRevoked 表示升级使前端信任失效，需重新授权。
	TrustRevoked bool `json:"trustRevoked,omitempty"`
	// RequiredReEnable 升级后状态回到 installed，需重新启用。
	RequiredReEnable bool `json:"requiredReEnable,omitempty"`
	// ActivationPending 表示候选已惰性保存，活动制品仍在服务，尚未执行受信任升级事务。
	ActivationPending bool `json:"activationPending,omitempty"`
}

// MigrationRecord 账本中的一条迁移。
type MigrationRecord struct {
	Path      string    `json:"path"`
	Checksum  string    `json:"checksum"`
	Status    string    `json:"status"`
	AppliedAt time.Time `json:"appliedAt"`
	Message   string    `json:"message,omitempty"`
}

type ExtensionEvent struct {
	ID          int64     `json:"id"`
	ExtensionID string    `json:"extensionId"`
	ActorUserID int64     `json:"actorUserId"`
	Action      string    `json:"action"`
	Message     string    `json:"message"`
	CreatedAt   time.Time `json:"createdAt"`
}

type ExtensionAdminNavigationItem struct {
	ExtensionID     string `json:"extensionId"`
	ExtensionName   string `json:"extensionName"`
	ExtensionType   string `json:"extensionType"`
	ExtensionStatus string `json:"extensionStatus"`
	Path            string `json:"path"`
	Label           string `json:"label"`
	Description     string `json:"description"`
	Icon            string `json:"icon"`
	View            string `json:"view"`
	// Group 非空时，前端把同组页签渲染成一个独立的可展开一级菜单。
	Group string `json:"group,omitempty"`
	Order int    `json:"order"`
}

type EffectiveContribution struct {
	ExtensionID   string            `json:"extensionId"`
	ExtensionName string            `json:"extensionName"`
	ExtensionType string            `json:"extensionType"`
	Point         string            `json:"point"`
	ID            string            `json:"id"`
	Order         int               `json:"order"`
	Label         map[string]string `json:"label,omitempty"`
	Icon          string            `json:"icon,omitempty"`
	Payload       json.RawMessage   `json:"payload,omitempty"`
}

// ExtensionSettingOption 是按请求 locale 解析后的选项展示值。
type ExtensionSettingOption struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type ExtensionSettingValue struct {
	Key              string `json:"key"`
	Label            string `json:"label"`
	Description      string `json:"description"`
	Type             string `json:"type"`
	Default          string `json:"default"`
	Value            string `json:"value"`
	Placeholder      string `json:"placeholder,omitempty"`
	RecommendedValue string `json:"recommendedValue,omitempty"`
	Required         bool   `json:"required,omitempty"`
	// Width 为 Schema UI 控件宽度：default 或 full；省略时前端按 default。
	Width     string                   `json:"width,omitempty"`
	Group     string                   `json:"group,omitempty"`
	GroupID   string                   `json:"groupId,omitempty"`
	Column    int                      `json:"column,omitempty"`
	Options   []ExtensionSettingOption `json:"options,omitempty"`
	SecretSet bool                     `json:"secretSet,omitempty"`
}

type ExtensionSettingsRenderer struct {
	Mode      string                      `json:"mode"`
	Layout    string                      `json:"layout"`
	Source    string                      `json:"source"`
	Fallback  string                      `json:"fallback"`
	Component *ExtensionSettingsComponent `json:"component,omitempty"`
}

type ExtensionSettingsComponent struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	APIVersion int    `json:"apiVersion"`
	Entry      string `json:"entry,omitempty"`
	CSS        string `json:"css,omitempty"`
}

type ExtensionSettingsTab struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Groups      []string `json:"groups,omitempty"`
}

type ExtensionSettingsGroup struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Columns     int    `json:"columns,omitempty"`
}

type ExtensionSettingsCallout struct {
	ID        string `json:"id"`
	Tone      string `json:"tone"`
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	LinkLabel string `json:"linkLabel,omitempty"`
	LinkURL   string `json:"linkUrl,omitempty"`
	Tab       string `json:"tab,omitempty"`
	Group     string `json:"group,omitempty"`
}

type ExtensionSettingsAction struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	Label          string   `json:"label"`
	Description    string   `json:"description,omitempty"`
	Placement      string   `json:"placement"`
	UseDraftValues bool     `json:"useDraftValues"`
	Fields         []string `json:"fields,omitempty"`
	// Tool 是 ai_tool_probe 要调用的 AI 工具声明 id；其它 kind 为空。
	Tool              string `json:"tool,omitempty"`
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
}

type ExtensionSettings struct {
	ExtensionID      string                     `json:"extensionId"`
	ExtensionType    string                     `json:"extensionType"`
	ExtensionVersion string                     `json:"extensionVersion"`
	ExtensionStatus  string                     `json:"extensionStatus"`
	Renderer         ExtensionSettingsRenderer  `json:"renderer"`
	Tabs             []ExtensionSettingsTab     `json:"tabs,omitempty"`
	Groups           []ExtensionSettingsGroup   `json:"groups,omitempty"`
	Callouts         []ExtensionSettingsCallout `json:"callouts,omitempty"`
	Items            []ExtensionSettingValue    `json:"items"`
	Actions          []ExtensionSettingsAction  `json:"actions,omitempty"`
}

// AdminPageBootstrap 管理端扩展页一次请求所需的 extension + 匹配页 + 可选设置。
// page/settings 刻意不用 omitempty，缺失时 JSON 显式 null，供前端 not-found UI 使用。
type AdminPageBootstrap struct {
	Extension Extension          `json:"extension"`
	Page      *ManifestAdminPage `json:"page"`
	Settings  *ExtensionSettings `json:"settings"`
}

// PublicActiveThemeSettings 当前激活主题的非 secret 运行时设置（前台可读）。
type PublicActiveThemeSettings struct {
	ThemeID       string            `json:"themeId"`
	Version       string            `json:"version,omitempty"`
	PackageDigest string            `json:"packageDigest,omitempty"`
	NodeRevision  uint64            `json:"nodeRevision,omitempty"`
	Settings      map[string]string `json:"settings"`
}

type UpdateSettingsInput struct {
	Values map[string]string `json:"values"`
}

type SettingsActionSecretInput struct {
	Mode  string `json:"mode"`
	Value string `json:"value,omitempty"`
}

type ExecuteSettingsActionInput struct {
	Values  map[string]string                    `json:"values,omitempty"`
	Secrets map[string]SettingsActionSecretInput `json:"secrets,omitempty"`
}

type SettingsActionResult struct {
	Success     bool              `json:"success"`
	Reason      string            `json:"reason"`
	Message     string            `json:"message"`
	Details     map[string]string `json:"details,omitempty"`
	Suggestions []string          `json:"suggestions,omitempty"`
	DurationMS  int64             `json:"durationMs"`
}

type SettingsActionProbeResult struct {
	OK          bool
	Reason      string
	Message     string
	Details     map[string]string
	Suggestions []string
}

type ExtensionEventDelivery struct {
	ID            int64      `json:"id"`
	ExtensionID   string     `json:"extensionId"`
	EventName     string     `json:"eventName"`
	EventKind     string     `json:"eventKind"`
	Status        string     `json:"status"`
	Reason        string     `json:"reason"`
	Message       string     `json:"message"`
	CorrelationID string     `json:"correlationId"`
	AttemptCount  int        `json:"attemptCount"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	CompletedAt   *time.Time `json:"completedAt,omitempty"`
}

// ValidExtensionID 报告扩展 ID 是否符合清单 ID 规则。
func ValidExtensionID(id string) bool {
	return extensionmanifest.ValidExtensionID(id)
}

type ArchiveInput struct {
	FileName string
	Data     []byte
	// SourceURL records where the archive came from. It is audit-only; install
	// behavior never reads it.
	SourceURL string
	// SourceDigest is the SHA-256 of the archive bytes when the caller computed
	// it. Audit-only, so operators can pin the exact download.
	SourceDigest string
	// ExpectedExtensionID/ExpectedVersion/ExpectedType 是调用方在下载前锁定的
	// 包身份；非空项必须在解析清单后完全一致，否则中止安装。手工 URL 安装可以
	// 留空，管理页 bridge 总是带上 id 与类型。
	ExpectedExtensionID string
	ExpectedVersion     string
	ExpectedType        string
	// SourceLabel 是审计用的来源标签（例如 admin-page:sforum.store）。
	SourceLabel string
}

type SaveInstalledInput struct {
	Manifest            Manifest
	PackagePath         string
	PackageDigest       string
	AdminFrontendDigest string
}

type SaveBuiltinInput struct {
	Manifest            Manifest
	PackagePath         string
	PackageDigest       string
	AdminFrontendDigest string
}

type EventInput struct {
	ExtensionID string
	ActorUserID int64
	Action      string
	Message     string
}

type EventDeliveryInput struct {
	ExtensionID   string
	EventName     string
	EventKind     string
	Status        string
	Reason        string
	Message       string
	CorrelationID string
}

type EventDeliveryUpdateInput struct {
	ID           int64
	Status       string
	Reason       string
	Message      string
	AttemptCount int
	Completed    bool
}

type EventDeliveryListInput struct {
	ExtensionID string
	EventName   string
	Status      string
	Limit       int
}

// StagedArtifact 返回等待可信生命周期执行的不可变候选制品。
// 返回值不携带当前运行时或下一候选，避免调用方误把活动实例状态归到尚未启动的代码上。
func (e Extension) StagedArtifact() (Extension, bool) {
	if e.StagedVersion == nil {
		return Extension{}, false
	}
	staged := e.StagedVersion
	return Extension{
		ID: e.ID, Name: e.Name, Version: staged.Version, Type: e.Type, Status: e.Status,
		Source: e.Source, IsSystem: e.IsSystem, IsDeletable: e.IsDeletable,
		Manifest: staged.Manifest, PackageDigest: staged.PackageDigest,
		AdminFrontendDigest: staged.AdminFrontendDigest, PackagePath: staged.PackagePath,
		ActiveVersionID: staged.ID, InstalledAt: staged.InstalledAt, UpdatedAt: e.UpdatedAt,
	}, true
}

func trustReviewArtifact(extension Extension) Extension {
	// A disabled plugin restarts its current exact artifact. Its staged package
	// stays inert until an explicit upgrade, so the challenge must bind current.
	if staged, ok := extension.StagedArtifact(); ok && extension.Status != StatusDisabled {
		return staged
	}
	return extension
}
