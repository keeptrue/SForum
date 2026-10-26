package identity

import (
	"errors"
	"time"

	avatar "github.com/keeptrue/sforum/apps/api/app/Support/Avatar"
)

type UserStatus string

const (
	UserStatusActive   UserStatus = "active"
	UserStatusDisabled UserStatus = "disabled"
	UserStatusBanned   UserStatus = "banned"
)

const (
	CodeRegisterInvalid     = "auth.register_invalid"
	CodeRegisterDisabled    = "auth.register_disabled"
	CodeSessionUnavailable  = "auth.session_unavailable"
	CodeSessionPolicyDenied = "auth.session_policy_denied"
	AuditActionLogin        = "auth.login.success"
	AuditActionRegister     = "auth.register.success"
	// 外部认证审计动作（见 plans/2026-07-27-github-social-login-builtin-plugin.md）。
	// 审计 payload 不得包含 raw subject/digest/token/state/verifier/secret。
	AuditActionExternalLogin    = "auth.external_login.success"
	AuditActionExternalRegister = "auth.external_register.success"
	AuditActionExternalLink     = "auth.external_link.success"
	AuditActionExternalUnlink   = "auth.external_unlink.success"
	// Host 激活目录 mutation（actor-bound；无 raw secret/subject）。
	AuditActionProviderActivationUpdate = "identity.provider.activation.update"
	AuditActionProviderActivationReset  = "identity.provider.activation.reset"
	AuditActionProviderProbe            = "identity.provider.probe"
	// 自助改名审计。旧值/新值都记，历史表另有产品语义的一份记录。
	AuditActionNicknameChange = "identity.nickname.change"

	// 字段键必须等于管理端请求体的 JSON tag：前端用它把消息对到表单控件。
	// displayName 的键由昵称域（nickname.go）声明，这里不重复定义。
	FieldUsername          = "username"
	FieldEmail             = "email"
	FieldPassword          = "password"
	FieldHumanVerification = "humanVerification"
	FieldLocale            = "locale"
	FieldStatus            = "status"
	FieldKind              = "kind"
	FieldBio               = "bio"
	FieldSignature         = "signature"
	FieldLocation          = "location"
	FieldWebsiteURL        = "websiteUrl"
	FieldRoleKeys          = "roleKeys"

	MessageUsernameRequired = "auth.username_required"
	MessageEmailRequired    = "auth.email_required"
	MessageEmailInvalid     = "auth.email_invalid"
	// 管理端创建用户：密码模式必须二选一。
	MessagePasswordRequired     = "auth.password_required"
	MessagePasswordModeConflict = "auth.password_mode_conflict"
	MessagePasswordMin          = "auth.password_min_length"
	MessagePasswordMax          = "auth.password_max_length"
	MessagePasswordLowercase    = "auth.password_lowercase"
	MessagePasswordUppercase    = "auth.password_uppercase"
	MessagePasswordNumber       = "auth.password_number"
	MessagePasswordSymbol       = "auth.password_symbol"
	MessageUsernameTaken        = "auth.username_taken"
	MessageEmailTaken           = "auth.email_taken"
	MessageUsernameTooShort     = "auth.username_too_short"
	MessageUsernameTooLong      = "auth.username_too_long"
	MessageUsernameReserved     = "auth.username_reserved"
	MessageUsernameCharset      = "auth.username_invalid_charset"
	// 管理端用户更新/创建的字段级消息（后台语境用 user.*，不引入 admin.*）。
	// 冲突话术单独一套：auth.*_taken 面向注册者（「请直接登录」），管理员在改别人的
	// 资料时这句话是错的，所以后台三条路径一律用下面的 user.*_taken。
	MessageUserUsernameTaken   = "user.username_taken"
	MessageUserEmailTaken      = "user.email_taken"
	MessageDisplayNameRequired = "user.display_name_required"
	MessageDisplayNameTooLong  = "user.display_name_too_long"
	MessageLocaleInvalid       = "user.locale_invalid"
	MessageStatusInvalid       = "user.status_invalid"
	MessageKindInvalid         = "user.kind_invalid"
	MessageBioTooLong          = "user.bio_too_long"
	MessageSignatureTooLong    = "user.signature_too_long"
	MessageLocationTooLong     = "user.location_too_long"
	MessageWebsiteInvalid      = "user.website_url_invalid"
	MessageWebsiteTooLong      = "user.website_url_too_long"
	MessageRoleUnknown         = "user.role_unknown"
	CodeLoginLocked            = "auth.login_locked"
)

var (
	ErrInvalidCredentials = errors.New("identity: invalid credentials")
	// ErrLoginLocked 连续登录失败触发临时锁定。
	ErrLoginLocked = errors.New("identity: login temporarily locked")
	// ErrLoginVerificationRequired 密码正确，但账号累计风险要求额外人机验证。
	ErrLoginVerificationRequired = errors.New("identity: login verification required")
	// ErrRegistrationDisabled 表示运营已关闭开放注册，且当前不在首用户 bootstrap 窗口。
	ErrRegistrationDisabled           = errors.New("identity: registration disabled")
	ErrCredentialNotFound             = errors.New("identity: credential not found")
	ErrPermissionDenied               = errors.New("identity: permission denied")
	ErrInvalidPermission              = errors.New("identity: invalid permission")
	ErrInvalidRole                    = errors.New("identity: invalid role")
	ErrInvalidRoleInput               = errors.New("identity: invalid role input")
	ErrPermissionOverrideConflict     = errors.New("identity: permission override conflict")
	ErrSystemRoleLocked               = errors.New("identity: system role is locked")
	ErrDefaultRoleLocked              = errors.New("identity: default role is locked")
	ErrInitialSuperAdminLocked        = errors.New("identity: initial super admin is locked")
	ErrSuperAdminOverridesLocked      = errors.New("identity: super admin permission overrides are locked")
	ErrSelfRoleChange                 = errors.New("identity: actors cannot change their own roles or overrides")
	ErrSuperAdminGrantRestricted      = errors.New("identity: only super admin can grant or manage super_admin role")
	ErrUsernameOrEmailNotUnique       = errors.New("identity: username or email is not unique")
	ErrPasswordDoesNotMeetPolicy      = errors.New("identity: password does not meet policy")
	ErrPasswordResetTokenNotFound     = errors.New("identity: password reset token not found or expired")
	ErrEmailVerificationTokenNotFound = errors.New("identity: email verification token not found or expired")
	ErrEmailVerificationRequired      = errors.New("identity: email verification required")
	ErrEmailVerificationRateLimited   = errors.New("identity: email verification rate limited")
	// 密码重置请求过于频繁（按邮箱/IP 限流）。
	ErrPasswordResetRateLimited = errors.New("identity: password reset rate limited")
	// 会话目录：要操作的会话不存在或不属于当前用户（含越权访问别人的 sid）。
	ErrSessionNotFound = errors.New("identity: session not found")
	// 管理员试图强制下线自己的全部设备（应改用 logout）。
	ErrSelfSessionRevoke = errors.New("identity: cannot revoke own sessions via admin path")
	// 非超管管理员试图强制下线超管用户的设备，超管账户受保护（与 ReplaceUserRoles 的保护对称）。
	ErrSuperAdminSessionLocked = errors.New("identity: super admin sessions cannot be revoked by non-super-admin")
	// 目标用户不存在（管理路径）。
	ErrUserNotFound = errors.New("identity: user not found")
	// 管理员更新账户/资料时字段不合法。
	ErrInvalidUserUpdate = errors.New("identity: invalid user update")
	// 未接入语言偏好写入存储时，自服务语言更新不可用。
	ErrUserLocaleUpdateUnavailable = errors.New("identity: user locale update unavailable")
	ErrInvalidAppearancePreference = errors.New("identity: invalid appearance preference")
	ErrAppearanceUpdateUnavailable = errors.New("identity: appearance preference update unavailable")
	// 禁止通过管理路径修改自己的账号状态（避免自锁）。
	ErrSelfStatusChange = errors.New("identity: cannot change own status via admin path")
)

// 会话下线原因（写入 user_sessions.revoke_reason）。
const (
	RevokeReasonLogout          = "logout"
	RevokeReasonDevice          = "revoke_device"
	RevokeReasonOthers          = "revoke_others"
	RevokeReasonMaxExceeded     = "max_exceeded"
	RevokeReasonPasswordReset   = "password_reset"
	RevokeReasonSessionReplaced = "session_replaced"
)

// SessionRecord 是 user_sessions 一行的领域视图，用于设备列表/历史展示。
// 注意：不包含任何可用于劫持会话的凭证（无 cookie session id、无 raw token）；
// SID 是 server 生成的 opaque 标识，仅用于指定「下线哪一条」，无法用来登录。
type SessionRecord struct {
	ID           string     `json:"id"`           // opaque 会话标识，前端用来指定下线哪一条
	DeviceName   string     `json:"deviceName"`   // 由 UA 解析的展示名，如 "Chrome on macOS"
	Browser      string     `json:"browser"`      // 浏览器名
	OS           string     `json:"os"`           // 操作系统名
	IPPrefix     string     `json:"ipPrefix"`     // 脱敏 IP 前缀，如 "1.2.3.*"
	CreatedAt    time.Time  `json:"createdAt"`    // 登录时间
	LastSeenAt   time.Time  `json:"lastSeenAt"`   // 最后活跃时间
	IsCurrent    bool       `json:"isCurrent"`    // 是否当前请求所在设备
	RevokedAt    *time.Time `json:"revokedAt"`    // 历史记录里可见的下线时间
	RevokeReason string     `json:"revokeReason"` // 下线原因
}

// SessionListResult 是设备列表/历史的分页结果。
type SessionListResult struct {
	Items   []SessionRecord `json:"items"`
	Total   int64           `json:"total"`
	Page    int             `json:"page"`
	PerPage int             `json:"perPage"`
}

// ClearUserClientIPsResult 是清空用户相关真实 IP 后的影响行数（隐私合规）。
type ClearUserClientIPsResult struct {
	SessionsCleared int `json:"sessionsCleared"`
	TopicsCleared   int `json:"topicsCleared"`
	CommentsCleared int `json:"commentsCleared"`
}

type FieldMessages map[string][]string

type RegisterInvalidError struct {
	Fields FieldMessages
}

func (e *RegisterInvalidError) Error() string {
	return "identity: registration input is invalid"
}

func NewRegisterInvalid(fields FieldMessages) *RegisterInvalidError {
	return &RegisterInvalidError{Fields: fields}
}

// UserKind 区分人类账号与机器人账号。机器人占用真实用户名额并计入在线人数，
// 但它们不参与新人信任阶梯，也不应被授予管理权限。
type UserKind string

const (
	UserKindHuman UserKind = "human"
	UserKindBot   UserKind = "bot"
)

// ValidUserKind 报告账号类型是否为已知值。未知值按人类处理，避免一次读取失败
// 就把某个账号当成机器人。
func ValidUserKind(kind UserKind) bool {
	return kind == UserKindHuman || kind == UserKindBot
}

// IsBot 是机器人语义判断的唯一入口，避免各处直接比较字符串。
func (k UserKind) IsBot() bool { return k == UserKindBot }

type Actor struct {
	ID          int64
	Status      UserStatus
	RoleKeys    []string
	Permissions map[string]bool
	// CreatedAt 用于新人信任阶梯；零值表示未知（跳过新人限制）。
	CreatedAt time.Time
	// Kind 区分人类与机器人账号。零值按人类处理。
	Kind UserKind
}

type PostSummary struct {
	ID           int64
	AuthorUserID int64
}

type CurrentUser struct {
	ID                  int64                 `json:"id"`
	Username            string                `json:"username"`
	DisplayName         string                `json:"displayName"`
	Avatar              avatar.View           `json:"avatar"`
	Locale              string                `json:"locale"`
	Appearance          *AppearancePreference `json:"appearance"`
	Status              UserStatus            `json:"status"`
	EmailVerified       bool                  `json:"emailVerified"`
	IsInitialSuperAdmin bool                  `json:"isInitialSuperAdmin"`
	RoleKeys            []string              `json:"roleKeys"`
	Permissions         []string              `json:"permissions"`
	// CreatedAt 注册时间；不强制暴露给所有前端，但 Actor 构建需要。
	CreatedAt time.Time `json:"-"`
	// CurrentTokenVersion binds a successful credential proof to the authority
	// revision that existed when the credential row was read.
	CurrentTokenVersion int64 `json:"-"`
}

// AppearancePreference is a private current-user override. A nil preference
// means the account follows the operator-owned site appearance.
type AppearancePreference struct {
	Theme           string `json:"theme"`
	LightBackground string `json:"lightBackground"`
}

type RegistrationStatus struct {
	// 始终为 false：不向公开端点泄露 bootstrap 窗口信息。
	NextUserIsInitialSuperAdmin bool `json:"nextUserIsInitialSuperAdmin"`
	// 前端是否应展示注册入口/允许提交。已含 bootstrap 覆盖（无用户时恒为 true）。
	RegistrationEnabled bool `json:"registrationEnabled"`
}

type LoginAudit struct {
	UserID      int64
	Action      string
	IPAddress   string
	UserAgent   string
	SessionHash string
}

type Permission struct {
	Key                string            `json:"key"`
	Module             string            `json:"module"`
	Label              string            `json:"label"`
	Description        string            `json:"description"`
	LabelLocales       map[string]string `json:"-"`
	DescriptionLocales map[string]string `json:"-"`
}

type RolePermissionSet struct {
	RoleKey        string   `json:"roleKey"`
	PermissionKeys []string `json:"permissionKeys"`
}

type PermissionMatrix struct {
	Permissions []Permission        `json:"permissions"`
	Roles       []RolePermissionSet `json:"roles"`
}

type PermissionOverrides struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

type AdminUserSummary struct {
	ID                  int64      `json:"id"`
	Username            string     `json:"username"`
	Email               string     `json:"email"`
	EmailVerified       bool       `json:"emailVerified"`
	DisplayName         string     `json:"displayName"`
	Locale              string     `json:"locale"`
	Status              UserStatus `json:"status"`
	IsInitialSuperAdmin bool       `json:"isInitialSuperAdmin"`
	RoleKeys            []string   `json:"roleKeys"`
	// CreatedAt / UpdatedAt 列表可选展示；旧客户端忽略未知字段。
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// AdminUserProfile 是后台可查看/编辑的公开资料字段（来自 user_profiles）。
type AdminUserProfile struct {
	Bio        string `json:"bio"`
	Signature  string `json:"signature"`
	Location   string `json:"location"`
	WebsiteURL string `json:"websiteUrl"`
}

type AdminUserDetail struct {
	AdminUserSummary
	// Kind 区分人类与机器人账号。只在详情上暴露——按类型筛选用户是另一个话题，
	// 不在这次改动范围内。
	Kind                UserKind            `json:"kind"`
	Permissions         []string            `json:"permissions"`
	PermissionOverrides PermissionOverrides `json:"permissionOverrides"`
	Profile             AdminUserProfile    `json:"profile"`
	// 以下字段供后台「用户信息预览」使用：含完整 IP / UA，仅 user.view 管理端可见。
	// 自服务设备列表仍只暴露脱敏 ipPrefix，不走本结构。
	Activity          AdminUserActivity     `json:"activity"`
	Sessions          []AdminSessionInspect `json:"sessions"`
	RecentAuthEvents  []AdminAuthEvent      `json:"recentAuthEvents"`
	PasswordChangedAt *time.Time            `json:"passwordChangedAt,omitempty"`
	// NicknameHistory 是最近若干次改名。历史表永久保留，这里只截取详情面板需要的条数。
	NicknameHistory []NicknameChange `json:"nicknameHistory,omitempty"`
}

// AdminUserActivity 汇总活跃度与最近登录线索。
type AdminUserActivity struct {
	TopicCount         int64      `json:"topicCount"`
	CommentCount       int64      `json:"commentCount"`
	ActiveSessionCount int64      `json:"activeSessionCount"`
	TotalSessionCount  int64      `json:"totalSessionCount"`
	LastLoginAt        *time.Time `json:"lastLoginAt,omitempty"`
	LastLoginIP        string     `json:"lastLoginIP"`
	LastLoginUserAgent string     `json:"lastLoginUserAgent"`
	LastSeenAt         *time.Time `json:"lastSeenAt,omitempty"`
}

// AdminSessionInspect 是管理端会话排查视图：暴露完整 IP 与原始 User-Agent。
// 不包含任何可用于劫持会话的凭证（无 cookie id / raw token）。
type AdminSessionInspect struct {
	ID           string     `json:"id"`
	DeviceName   string     `json:"deviceName"`
	Browser      string     `json:"browser"`
	OS           string     `json:"os"`
	IPPrefix     string     `json:"ipPrefix"`
	IPAddress    string     `json:"ipAddress"`
	UserAgent    string     `json:"userAgent"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastSeenAt   time.Time  `json:"lastSeenAt"`
	IsActive     bool       `json:"isActive"`
	RevokedAt    *time.Time `json:"revokedAt,omitempty"`
	RevokeReason string     `json:"revokeReason"`
}

// AdminAuthEvent 是登录/注册等身份审计事件的管理端视图。
type AdminAuthEvent struct {
	ID          int64     `json:"id"`
	Action      string    `json:"action"`
	IPAddress   string    `json:"ipAddress"`
	UserAgent   string    `json:"userAgent"`
	SessionHash string    `json:"sessionHash"`
	CreatedAt   time.Time `json:"createdAt"`
}

// AdminUpdateUserInput 管理员更新账户/资料。指针字段为 nil 表示不改。
type AdminUpdateUserInput struct {
	Username    *string
	Email       *string
	DisplayName *string
	Locale      *string
	Status      *UserStatus
	Bio         *string
	Signature   *string
	Location    *string
	WebsiteURL  *string
	// Kind 允许管理员把一个账号标记为机器人，或把它恢复为人类。
	Kind *UserKind
}

type UserListInput struct {
	Page      int
	PerPage   int
	Query     string
	Status    string
	RoleKey   string
	SortBy    string
	SortOrder string
}

type AdminUserList struct {
	Items   []AdminUserSummary `json:"items"`
	Total   int64              `json:"total"`
	Page    int                `json:"page"`
	PerPage int                `json:"perPage"`
}
