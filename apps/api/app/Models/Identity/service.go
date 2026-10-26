package identity

import (
	"context"
	"errors"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"time"

	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

type Service struct {
	store            Store
	events           appevents.Publisher
	passwordPolicies PasswordPolicyResolver
	// registrationPolicy 可选；缺省时视为始终开放注册（测试与旧装配路径）。
	registrationPolicy RegistrationPolicyResolver
	// usernamePolicy 可选；缺省时仅校验非空。
	usernamePolicy UsernamePolicyResolver
	// loginLockout 可选；缺省时不锁定。
	loginLockout       LoginLockoutStore
	loginLockoutPolicy LoginLockoutPolicyResolver
	loginVerification  LoginVerificationPolicyResolver
	identityRegistry   identityregistry.Store
}

func NewService(store Store) *Service {
	return NewServiceWithEvents(store, nil)
}

func NewServiceWithEvents(store Store, publisher appevents.Publisher) *Service {
	return NewServiceWithEventsAndPasswordPolicy(store, publisher, nil)
}

func NewServiceWithPasswordPolicy(store Store, resolver PasswordPolicyResolver) *Service {
	return NewServiceWithEventsAndPasswordPolicy(store, nil, resolver)
}

func NewServiceWithEventsAndPasswordPolicy(store Store, publisher appevents.Publisher, resolver PasswordPolicyResolver) *Service {
	return NewServiceWithPolicies(store, publisher, resolver, nil)
}

// NewServiceWithPolicies 注入密码策略与开放注册策略（生产 bootstrap 使用）。
func NewServiceWithPolicies(store Store, publisher appevents.Publisher, passwordResolver PasswordPolicyResolver, registrationResolver RegistrationPolicyResolver) *Service {
	if passwordResolver == nil {
		passwordResolver = staticRecommendedPasswordPolicy{}
	}
	if registrationResolver == nil {
		registrationResolver = staticOpenRegistrationPolicy{}
	}
	return &Service{
		store:              store,
		events:             appevents.EnsurePublisher(publisher),
		passwordPolicies:   passwordResolver,
		registrationPolicy: registrationResolver,
	}
}

// WithUsernamePolicy 注入用户名策略（可选）。
func (s *Service) WithUsernamePolicy(resolver UsernamePolicyResolver) *Service {
	s.usernamePolicy = resolver
	return s
}

// WithLoginLockout 注入登录失败锁定（可选，通常 Redis）。
func (s *Service) WithLoginLockout(lockout LoginLockoutStore, policy LoginLockoutPolicyResolver) *Service {
	s.loginLockout = lockout
	s.loginLockoutPolicy = policy
	return s
}

type PasswordPolicyResolver interface {
	PasswordPolicy(ctx context.Context) (PasswordPolicy, error)
}

// RegistrationPolicyResolver 读取运营配置的开放注册意图（不含 bootstrap 覆盖）。
type RegistrationPolicyResolver interface {
	RegistrationEnabled(ctx context.Context) (bool, error)
}

// UsernamePolicy 用户名长度/字符集/保留名。
type UsernamePolicy struct {
	MinLength int
	MaxLength int
	Charset   string
	Reserved  []string
}

type UsernamePolicyResolver interface {
	UsernamePolicy(ctx context.Context) (UsernamePolicy, error)
}

// LoginLockoutPolicy 连续失败锁定。
type LoginLockoutPolicy struct {
	MaxFailures    int
	LockoutMinutes int
}

type LoginLockoutPolicyResolver interface {
	LoginLockoutPolicy(ctx context.Context) (LoginLockoutPolicy, error)
}

// LoginLockoutStore 分层登录节流（通常 Redis；key 内应为哈希后的账号标识）。
// clientIP 参与 account+IP / IP 维度；Redis 故障时应 fail open。
type LoginLockoutStore interface {
	IsLocked(ctx context.Context, loginKey, clientIP string) (bool, error)
	RequiresVerification(ctx context.Context, loginKey string) (bool, error)
	RecordFailure(ctx context.Context, loginKey, clientIP string, maxFailures int, lockout time.Duration) error
	ClearFailures(ctx context.Context, loginKey, clientIP string) error
}

type staticRecommendedPasswordPolicy struct{}

func (staticRecommendedPasswordPolicy) PasswordPolicy(context.Context) (PasswordPolicy, error) {
	return RecommendedPasswordPolicy(), nil
}

// staticOpenRegistrationPolicy 测试默认：始终开放注册。
type staticOpenRegistrationPolicy struct{}

func (staticOpenRegistrationPolicy) RegistrationEnabled(context.Context) (bool, error) {
	return true, nil
}

type RegisterInput struct {
	Username    string
	Email       string
	Password    string
	DisplayName string
	Locale      string
}

// RegistrationStatus 返回注册相关状态。该端点公开可访问（注册页加载时调用），
// 因此不通过它暴露"系统处于首注册窗口、下个注册者成 super_admin"这一敏感的 bootstrap 状态——
// 首注册的 super_admin 提升由 Register 路径内的 advisory lock 保证，无需提前告知调用方。
// NextUserIsInitialSuperAdmin 恒为 false；RegistrationEnabled 会在无用户时强制 true。
func (s *Service) RegistrationStatus(ctx context.Context) (RegistrationStatus, error) {
	hasAnyUser, err := s.store.AnyUserExists(ctx)
	if err != nil {
		return RegistrationStatus{}, err
	}
	enabled, err := s.effectiveRegistrationEnabled(ctx, hasAnyUser)
	if err != nil {
		return RegistrationStatus{}, err
	}
	return RegistrationStatus{
		NextUserIsInitialSuperAdmin: false,
		RegistrationEnabled:         enabled,
	}, nil
}

func (s *Service) ValidateRegister(ctx context.Context, input RegisterInput) error {
	if err := s.ensureRegistrationAllowed(ctx); err != nil {
		return err
	}
	normalized := normalizeRegisterInput(input)
	policy, err := s.passwordPolicies.PasswordPolicy(ctx)
	if err != nil {
		return err
	}
	usernamePolicy, err := s.resolveUsernamePolicy(ctx)
	if err != nil {
		return err
	}
	fields := validateRegisterInputWithUsername(normalized.Username, normalized.Email, input.Password, policy, usernamePolicy)
	if len(fields) > 0 {
		return NewRegisterInvalid(fields)
	}
	// 字段合法后再走插件校验，避免人机验证被无效表单消耗。
	if err := s.applyUserBeforeRegister(ctx, normalized); err != nil {
		return err
	}

	conflicts, err := s.store.FindRegistrationConflicts(ctx, normalized.Username, normalized.Email)
	if err != nil {
		return err
	}
	if conflictFields := registrationConflictFields(conflicts); len(conflictFields) > 0 {
		return NewRegisterInvalid(conflictFields)
	}
	return nil
}

// ValidateExternalRegister 复用权威 username/email/reserved/hooks/registration-mode
// 校验，故意不校验密码（外部账号无凭据）。不得维护更弱的独立校验器。
// 实现 ExternalRegistrationValidator。
func (s *Service) ValidateExternalRegister(ctx context.Context, input ExternalRegistrationInput) error {
	if err := s.ensureRegistrationAllowed(ctx); err != nil {
		// 零用户站点：ensureRegistrationAllowed 会因 bootstrap 强制开放而通过；
		// 外部注册另由 CompleteRegistration 拒绝 bootstrap。
		return err
	}
	normalized := normalizeRegisterInput(RegisterInput{
		Username:    input.Username,
		Email:       input.Email,
		DisplayName: input.DisplayName,
		Locale:      input.Locale,
	})
	usernamePolicy, err := s.resolveUsernamePolicy(ctx)
	if err != nil {
		return err
	}
	fields := validateRegisterIdentityFields(normalized.Username, normalized.Email, usernamePolicy)
	if len(fields) > 0 {
		return NewRegisterInvalid(fields)
	}
	if err := s.applyUserBeforeRegister(ctx, normalized); err != nil {
		return err
	}
	conflicts, err := s.store.FindRegistrationConflicts(ctx, normalized.Username, normalized.Email)
	if err != nil {
		return err
	}
	if conflictFields := registrationConflictFields(conflicts); len(conflictFields) > 0 {
		return NewRegisterInvalid(conflictFields)
	}
	return nil
}

// SetupPassword 自助设置/更改本地密码。external-only 用户在缺失时创建 credential 行。
// 调用方负责 recent-auth 门控。
func (s *Service) SetupPassword(ctx context.Context, userID int64, newPassword string) error {
	if userID <= 0 {
		return ErrUserNotFound
	}
	if strings.TrimSpace(newPassword) == "" {
		return NewRegisterInvalid(FieldMessages{
			FieldPassword: {MessagePasswordMin},
		})
	}
	policy, err := s.passwordPolicies.PasswordPolicy(ctx)
	if err != nil {
		return err
	}
	if fields := policy.Validate(newPassword); len(fields) > 0 {
		return NewRegisterInvalid(fields)
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	// Upsert：无行则创建，有行则更新；password_hash 始终非 null。
	return s.store.UpdateUserPassword(ctx, userID, hash)
}

func (s *Service) Register(ctx context.Context, input RegisterInput) (CurrentUser, error) {
	normalized := normalizeRegisterInput(input)

	policy, err := s.passwordPolicies.PasswordPolicy(ctx)
	if err != nil {
		return CurrentUser{}, err
	}
	usernamePolicy, err := s.resolveUsernamePolicy(ctx)
	if err != nil {
		return CurrentUser{}, err
	}
	fields := validateRegisterInputWithUsername(normalized.Username, normalized.Email, input.Password, policy, usernamePolicy)
	if len(fields) > 0 {
		return CurrentUser{}, NewRegisterInvalid(fields)
	}
	// E1.3：落库与哈希密码前同步 validate（payload 不含 password）。
	if err := s.applyUserBeforeRegister(ctx, normalized); err != nil {
		return CurrentUser{}, err
	}

	passwordHash, err := HashPassword(input.Password)
	if err != nil {
		return CurrentUser{}, err
	}

	var current CurrentUser
	err = s.store.WithBootstrapTx(ctx, func(ctx context.Context, tx TxStore) error {
		hasAnyUser, err := tx.AnyUserExists(ctx)
		if err != nil {
			return err
		}
		// 事务内再次校验：关闭注册后仅 bootstrap（无用户）可继续。
		enabled, err := s.effectiveRegistrationEnabled(ctx, hasAnyUser)
		if err != nil {
			return err
		}
		if !enabled {
			return ErrRegistrationDisabled
		}
		conflicts, err := tx.FindRegistrationConflicts(ctx, normalized.Username, normalized.Email)
		if err != nil {
			return err
		}
		conflictFields := registrationConflictFields(conflicts)
		if len(conflictFields) > 0 {
			return NewRegisterInvalid(conflictFields)
		}

		current, err = tx.CreateUser(ctx, CreateUserInput{
			Username:            normalized.Username,
			Email:               normalized.Email,
			DisplayName:         normalized.DisplayName,
			Locale:              normalized.Locale,
			IsInitialSuperAdmin: !hasAnyUser,
		})
		if err != nil {
			return err
		}
		if err := tx.CreateCredential(ctx, current.ID, passwordHash); err != nil {
			return err
		}

		member, err := tx.GetDefaultRole(ctx)
		if err != nil {
			return err
		}
		if err := tx.AssignRole(ctx, current.ID, member.ID); err != nil {
			return err
		}
		current.RoleKeys = append(current.RoleKeys, member.Key)

		if !hasAnyUser {
			superAdmin, err := tx.GetRole(ctx, RoleSuperAdmin)
			if err != nil {
				return err
			}
			if err := tx.AssignRole(ctx, current.ID, superAdmin.ID); err != nil {
				return err
			}
			current.RoleKeys = append(current.RoleKeys, superAdmin.Key)
		}

		return tx.LoadCurrentUserAccess(ctx, &current)
	})
	if err != nil {
		return CurrentUser{}, err
	}

	s.events.Emit(ctx, appevents.Envelope{
		Name:          appevents.UserRegistered,
		Kind:          appevents.KindObserve,
		ActorUserID:   current.ID,
		ResourceType:  "user",
		ResourceID:    strconv.FormatInt(current.ID, 10),
		CorrelationID: appevents.NewID(),
		Payload: map[string]any{
			"userId":   current.ID,
			"username": current.Username,
			"email":    normalized.Email,
			"locale":   current.Locale,
		},
		OccurredAt: time.Now().UTC(),
	})

	return current, nil
}

// applyUserBeforeRegister 调用 user.before_register 同步 validate。
// 仅传 username/email/locale；密码永不进入 payload。v1 拒绝-only，不接受补丁。
func (s *Service) applyUserBeforeRegister(ctx context.Context, normalized normalizedRegisterInput) error {
	envelope := appevents.NewEnvelope(appevents.UserBeforeRegister, map[string]any{
		"username": normalized.Username,
		"email":    normalized.Email,
		"locale":   normalized.Locale,
	})
	envelope.ResourceType = "user"
	result := s.events.Emit(ctx, envelope)
	if !result.OK {
		return appevents.Reject(result)
	}
	return nil
}

// ensureRegistrationAllowed 在事务外做快速拒绝；权威校验仍在 WithBootstrapTx 内。
func (s *Service) ensureRegistrationAllowed(ctx context.Context) error {
	hasAnyUser, err := s.store.AnyUserExists(ctx)
	if err != nil {
		return err
	}
	enabled, err := s.effectiveRegistrationEnabled(ctx, hasAnyUser)
	if err != nil {
		return err
	}
	if !enabled {
		return ErrRegistrationDisabled
	}
	return nil
}

// effectiveRegistrationEnabled：无用户时强制 true（bootstrap）；否则读运营配置。
func (s *Service) effectiveRegistrationEnabled(ctx context.Context, hasAnyUser bool) (bool, error) {
	if !hasAnyUser {
		return true, nil
	}
	enabled, err := s.registrationPolicy.RegistrationEnabled(ctx)
	if err != nil {
		// 策略读取失败时保守拒绝新注册，避免在配置故障时意外开放。
		return false, err
	}
	return enabled, nil
}

type normalizedRegisterInput struct {
	Username    string
	Email       string
	DisplayName string
	Locale      string
}

func normalizeRegisterInput(input RegisterInput) normalizedRegisterInput {
	username := strings.TrimSpace(input.Username)
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = username
	}
	locale := strings.TrimSpace(input.Locale)
	if locale == "" {
		locale = "zh-CN"
	}

	return normalizedRegisterInput{
		Username:    username,
		Email:       strings.TrimSpace(input.Email),
		DisplayName: displayName,
		Locale:      locale,
	}
}

func validateRegisterInput(username string, email string, password string, policy PasswordPolicy) FieldMessages {
	return validateRegisterInputWithUsername(username, email, password, policy, UsernamePolicy{})
}

// validateRegisterIdentityFields 权威 username/email 校验（密码无关，供外部注册复用）。
func validateRegisterIdentityFields(username string, email string, usernamePolicy UsernamePolicy) FieldMessages {
	fields := FieldMessages{}
	if username == "" {
		addFieldMessage(fields, FieldUsername, MessageUsernameRequired)
	} else if usernamePolicy.MinLength > 0 || usernamePolicy.MaxLength > 0 || usernamePolicy.Charset != "" || len(usernamePolicy.Reserved) > 0 {
		if ok, reason := usernamePolicy.Validate(username); !ok {
			addFieldMessage(fields, FieldUsername, reason)
		}
	}
	if email == "" {
		addFieldMessage(fields, FieldEmail, MessageEmailRequired)
	} else if !isValidEmail(email) {
		addFieldMessage(fields, FieldEmail, MessageEmailInvalid)
	}
	return fields
}

func validateRegisterInputWithUsername(username string, email string, password string, policy PasswordPolicy, usernamePolicy UsernamePolicy) FieldMessages {
	fields := validateRegisterIdentityFields(username, email, usernamePolicy)
	for field, messages := range policy.Validate(password) {
		fields[field] = append(fields[field], messages...)
	}
	return fields
}

func isValidEmail(email string) bool {
	parsed, err := mail.ParseAddress(email)
	return err == nil && parsed.Address == email
}

func registrationConflictFields(conflicts RegistrationConflicts) FieldMessages {
	fields := FieldMessages{}
	if conflicts.UsernameTaken {
		addFieldMessage(fields, FieldUsername, MessageUsernameTaken)
	}
	if conflicts.EmailTaken {
		addFieldMessage(fields, FieldEmail, MessageEmailTaken)
	}
	return fields
}

func addFieldMessage(fields FieldMessages, field string, message string) {
	fields[field] = append(fields[field], message)
}

func (s *Service) Login(ctx context.Context, input LoginInput) (CurrentUser, error) {
	loginKey := strings.ToLower(strings.TrimSpace(input.Login))
	clientIP := strings.TrimSpace(input.ClientIP)
	if locked, err := s.isLoginLocked(ctx, loginKey, clientIP); err != nil {
		return CurrentUser{}, err
	} else if locked {
		return CurrentUser{}, ErrLoginLocked
	}

	credential, err := s.store.GetCredentialByLogin(ctx, strings.TrimSpace(input.Login))
	if err != nil {
		if !errors.Is(err, ErrCredentialNotFound) {
			return CurrentUser{}, err
		}
		// L1 时序对齐：用户不存在时也跑一次等价 argon2 验证，
		// 使两条失败路径耗时一致，消除用户名枚举的时序侧信道。
		if dummy, dErr := dummyPasswordHash(); dErr == nil {
			_, _ = VerifyPassword(input.Password, dummy)
		}
		_ = s.recordLoginFailure(ctx, loginKey, clientIP)
		return CurrentUser{}, ErrInvalidCredentials
	}

	ok, err := VerifyPassword(input.Password, credential.PasswordHash)
	if err != nil {
		return CurrentUser{}, err
	}
	if !ok {
		_ = s.recordLoginFailure(ctx, loginKey, clientIP)
		return CurrentUser{}, ErrInvalidCredentials
	}
	if credential.Status != UserStatusActive {
		return CurrentUser{}, ErrInvalidCredentials
	}
	// 机器人账号不允许交互式登录：它由系统驱动，可登录的机器人等于给所有人开了
	// 一个冒充 AI 的入口。恢复为人类后即可正常登录。
	if kind, kindErr := s.store.UserKindOf(ctx, credential.ID); kindErr == nil && kind.IsBot() {
		_ = s.recordLoginFailure(ctx, loginKey, clientIP)
		return CurrentUser{}, ErrInvalidCredentials
	}
	if required, err := loginRequiresVerification(s, ctx, loginKey, credential, input); err != nil {
		return CurrentUser{}, err
	} else if required && !input.HumanVerified {
		return CurrentUser{}, ErrLoginVerificationRequired
	}

	_ = s.clearLoginFailures(ctx, loginKey, clientIP)
	return credential.CurrentUser, nil
}

func (s *Service) resolveUsernamePolicy(ctx context.Context) (UsernamePolicy, error) {
	if s.usernamePolicy == nil {
		return UsernamePolicy{}, nil
	}
	return s.usernamePolicy.UsernamePolicy(ctx)
}

func (s *Service) isLoginLocked(ctx context.Context, loginKey, clientIP string) (bool, error) {
	if s.loginLockout == nil {
		return false, nil
	}
	return s.loginLockout.IsLocked(ctx, loginKey, clientIP)
}

func (s *Service) recordLoginFailure(ctx context.Context, loginKey, clientIP string) error {
	if s.loginLockout == nil || s.loginLockoutPolicy == nil {
		return nil
	}
	policy, err := s.loginLockoutPolicy.LoginLockoutPolicy(ctx)
	if err != nil || policy.MaxFailures <= 0 || policy.LockoutMinutes <= 0 {
		return nil
	}
	return s.loginLockout.RecordFailure(ctx, loginKey, clientIP, policy.MaxFailures, time.Duration(policy.LockoutMinutes)*time.Minute)
}

func (s *Service) clearLoginFailures(ctx context.Context, loginKey, clientIP string) error {
	if s.loginLockout == nil {
		return nil
	}
	return s.loginLockout.ClearFailures(ctx, loginKey, clientIP)
}

func (s *Service) RecordLoginAudit(ctx context.Context, input LoginAudit) error {
	return s.store.RecordLoginAudit(ctx, input)
}

func (s *Service) CurrentUser(ctx context.Context, userID int64) (CurrentUser, error) {
	return s.store.GetCurrentUser(ctx, userID)
}

func (s *Service) Actor(ctx context.Context, userID int64) (Actor, error) {
	return s.store.LoadActor(ctx, userID)
}

func (s *Service) ListPermissions(ctx context.Context, actor Actor, locale string) ([]Permission, error) {
	if !canManagePermissions(actor) {
		return nil, ErrPermissionDenied
	}
	permissions, err := s.store.ListPermissions(ctx)
	if err != nil {
		return nil, err
	}
	return localizePermissions(permissions, locale), nil
}

func (s *Service) ListPermissionMatrix(ctx context.Context, actor Actor, locale string) (PermissionMatrix, error) {
	if !canManagePermissions(actor) {
		return PermissionMatrix{}, ErrPermissionDenied
	}
	matrix, err := s.store.ListPermissionMatrix(ctx)
	if err != nil {
		return PermissionMatrix{}, err
	}
	matrix.Permissions = localizePermissions(matrix.Permissions, locale)
	return matrix, nil
}

func localizePermissions(permissions []Permission, locale string) []Permission {
	for index := range permissions {
		permission := &permissions[index]
		permission.Label = resolvePermissionText(permission.LabelLocales, locale, permission.Label)
		permission.Description = resolvePermissionText(permission.DescriptionLocales, locale, permission.Description)
	}
	return permissions
}

func resolvePermissionText(values map[string]string, locale, fallback string) string {
	locale = strings.TrimSpace(strings.ReplaceAll(locale, "_", "-"))
	if value := strings.TrimSpace(values[locale]); value != "" {
		return value
	}
	if language, _, ok := strings.Cut(locale, "-"); ok {
		if value := strings.TrimSpace(values[language]); value != "" {
			return value
		}
	}
	return strings.TrimSpace(fallback)
}

func (s *Service) ListRoles(ctx context.Context, actor Actor) ([]Role, error) {
	if !actor.Can(PermissionRoleManage) {
		return nil, ErrPermissionDenied
	}
	return s.store.ListRoles(ctx)
}

func (s *Service) GetAdminUser(ctx context.Context, actor Actor, userID int64) (AdminUserDetail, error) {
	if !actor.Can(PermissionUserView) {
		return AdminUserDetail{}, ErrPermissionDenied
	}
	return s.store.GetAdminUser(ctx, userID)
}

// UpdateAdminUser 管理员更新目标用户的账户字段与公开资料。
// 需要 user.manage；将状态改为 banned 时还需要 user.ban。
// 不允许通过此路径修改自己的 status（避免自锁），其余字段允许自助运营修正。
func (s *Service) UpdateAdminUser(ctx context.Context, actor Actor, targetUserID int64, input AdminUpdateUserInput) (AdminUserDetail, error) {
	if !actor.Can(PermissionUserManage) {
		return AdminUserDetail{}, ErrPermissionDenied
	}
	if targetUserID <= 0 {
		return AdminUserDetail{}, ErrUserNotFound
	}

	target, err := s.store.GetAdminUser(ctx, targetUserID)
	if err != nil {
		return AdminUserDetail{}, err
	}

	// 非超管不得修改超管账户的核心字段（与会话下线保护对称）。
	if containsString(target.RoleKeys, RoleSuperAdmin) && !actor.IsSuperAdmin() {
		return AdminUserDetail{}, ErrSuperAdminSessionLocked
	}
	// 初始超管用户名/邮箱/状态不可被降权式破坏：至少保持可登录身份（状态须 active）。
	if target.IsInitialSuperAdmin && input.Status != nil && *input.Status != UserStatusActive {
		return AdminUserDetail{}, ErrInitialSuperAdminLocked
	}

	normalized, err := s.normalizeAdminUpdateUserInput(ctx, input)
	if err != nil {
		return AdminUserDetail{}, err
	}

	if normalized.Status != nil {
		if actor.ID == targetUserID {
			return AdminUserDetail{}, ErrSelfStatusChange
		}
		if *normalized.Status == UserStatusBanned && !actor.Can(PermissionUserBan) {
			return AdminUserDetail{}, ErrPermissionDenied
		}
	}

	// 用户名/邮箱冲突（store 也会再查一次；此处提前给字段级错误）。
	if normalized.Username != nil || normalized.Email != nil {
		checkUsername := target.Username
		checkEmail := target.Email
		if normalized.Username != nil {
			checkUsername = *normalized.Username
		}
		if normalized.Email != nil {
			checkEmail = *normalized.Email
		}
		if checkUsername != target.Username || checkEmail != target.Email {
			conflicts, cErr := s.store.FindRegistrationConflicts(ctx, checkUsername, checkEmail)
			if cErr != nil {
				return AdminUserDetail{}, cErr
			}
			// 冲突结果包含自身时需排除：FindRegistrationConflicts 不支持 exclude id，
			// 因此仅当「新值」与当前不同且被占用时才报错。
			fields := FieldMessages{}
			if normalized.Username != nil && *normalized.Username != target.Username && conflicts.UsernameTaken {
				addFieldMessage(fields, FieldUsername, MessageUserUsernameTaken)
			}
			if normalized.Email != nil && *normalized.Email != target.Email && conflicts.EmailTaken {
				addFieldMessage(fields, FieldEmail, MessageUserEmailTaken)
			}
			if len(fields) > 0 {
				return AdminUserDetail{}, NewAdminUserConflictError(fields)
			}
		}
	}

	return s.store.UpdateAdminUser(ctx, actor.ID, targetUserID, normalized)
}

const (
	maxAdminDisplayNameLength = 80
	maxAdminBioLength         = 500
	maxAdminSignatureLength   = 200
	maxAdminLocationLength    = 100
	maxAdminWebsiteLength     = 200
)

func (s *Service) CreateRole(ctx context.Context, actor Actor, input RoleInput) (Role, error) {
	if !actor.Can(PermissionRoleManage) {
		return Role{}, ErrPermissionDenied
	}
	normalized := normalizeRoleInput(input)
	if !validRoleInput(normalized) {
		return Role{}, ErrInvalidRoleInput
	}
	return s.store.CreateRole(ctx, normalized)
}

func (s *Service) UpdateRole(ctx context.Context, actor Actor, roleKey string, input RoleInput) (Role, error) {
	if !actor.Can(PermissionRoleManage) {
		return Role{}, ErrPermissionDenied
	}
	normalized := normalizeRoleInput(RoleInput{
		Key:         roleKey,
		Alias:       input.Alias,
		Description: input.Description,
	})
	if !validRoleInput(normalized) {
		return Role{}, ErrInvalidRoleInput
	}
	return s.store.UpdateRole(ctx, normalized.Key, normalized)
}

func (s *Service) DeleteRole(ctx context.Context, actor Actor, roleKey string) error {
	if !actor.Can(PermissionRoleManage) {
		return ErrPermissionDenied
	}
	if roleKey == RoleMember {
		return ErrDefaultRoleLocked
	}
	// super_admin 与内置模板角色（moderator/operator/tech_admin）均不可删除。
	if IsBuiltInSystemRole(roleKey) {
		return ErrSystemRoleLocked
	}
	return s.store.DeleteRole(ctx, roleKey)
}

func (s *Service) ReplaceRolePermissions(ctx context.Context, actor Actor, roleKey string, permissions []string) error {
	if !actor.Can(PermissionRoleManage) {
		return ErrPermissionDenied
	}
	if roleKey == RoleSuperAdmin {
		return ErrSystemRoleLocked
	}
	normalized := normalizeKeyList(permissions)
	if err := s.validatePermissions(ctx, normalized); err != nil {
		return err
	}
	return s.store.ReplaceRolePermissions(ctx, actor.ID, roleKey, normalized)
}

func (s *Service) ReplaceUserRoles(ctx context.Context, actor Actor, targetUserID int64, roleKeys []string) (AdminUserDetail, error) {
	if !actor.Can(PermissionUserManage) {
		return AdminUserDetail{}, ErrPermissionDenied
	}
	// 防自我提权：禁止修改自己的角色，避免 user.manage 持有者给自己加 super_admin。
	if actor.ID == targetUserID {
		return AdminUserDetail{}, ErrSelfRoleChange
	}

	target, err := s.store.GetAdminUser(ctx, targetUserID)
	if err != nil {
		return AdminUserDetail{}, err
	}

	normalized := normalizeKeyList(roleKeys)
	if target.IsInitialSuperAdmin && !containsString(normalized, RoleSuperAdmin) {
		return AdminUserDetail{}, ErrInitialSuperAdminLocked
	}
	// 授予或撤销 super_admin 成员身份都要求操作者本身是 super_admin，
	// 防止 user.manage 持有者对非初始超管做 demote。
	targetIsSuperAdmin := containsString(target.RoleKeys, RoleSuperAdmin)
	nextIsSuperAdmin := containsString(normalized, RoleSuperAdmin)
	if targetIsSuperAdmin != nextIsSuperAdmin && !actor.IsSuperAdmin() {
		return AdminUserDetail{}, ErrSuperAdminGrantRestricted
	}
	if err := s.validateRoles(ctx, normalized); err != nil {
		return AdminUserDetail{}, err
	}

	return s.store.ReplaceUserRoles(ctx, actor.ID, targetUserID, normalized)
}

func (s *Service) ReplaceUserPermissionOverrides(ctx context.Context, actor Actor, targetUserID int64, overrides PermissionOverrides) (AdminUserDetail, error) {
	// 个人权限例外高危，独立于 user.manage。
	if !actor.Can(PermissionUserPermissionOverride) {
		return AdminUserDetail{}, ErrPermissionDenied
	}
	// 防自我提权：禁止修改自己的权限覆盖。
	if actor.ID == targetUserID {
		return AdminUserDetail{}, ErrSelfRoleChange
	}

	target, err := s.store.GetAdminUser(ctx, targetUserID)
	if err != nil {
		return AdminUserDetail{}, err
	}
	if containsString(target.RoleKeys, RoleSuperAdmin) {
		return AdminUserDetail{}, ErrSuperAdminOverridesLocked
	}

	normalized := PermissionOverrides{
		Allow: normalizeKeyList(overrides.Allow),
		Deny:  normalizeKeyList(overrides.Deny),
	}
	if hasOverrideConflict(normalized) {
		return AdminUserDetail{}, ErrPermissionOverrideConflict
	}
	if err := s.validatePermissions(ctx, append(append([]string{}, normalized.Allow...), normalized.Deny...)); err != nil {
		return AdminUserDetail{}, err
	}

	return s.store.ReplaceUserPermissionOverrides(ctx, actor.ID, targetUserID, normalized)
}

func canManagePermissions(actor Actor) bool {
	return actor.Can(PermissionRoleManage) || actor.Can(PermissionUserManage) || actor.Can(PermissionUserView) || actor.Can(PermissionUserPermissionOverride)
}

// maxAdminListPage 限制后台列表的最大页数（M6），避免深 OFFSET 全表扫描 DoS，与 Forum 对齐。
const maxAdminListPage = 200

func normalizePage(page int, perPage int) (int, int) {
	if page < 1 {
		page = 1
	}
	// M6：限制最大页数，避免管理员接口被滥用进行深分页 DoS。
	if page > maxAdminListPage {
		page = maxAdminListPage
	}
	if perPage < 1 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}
	return page, perPage
}

// escapeLike 转义 SQL LIKE 的元字符（\、%、_），配合查询中的 ESCAPE '\' 使用（M6/L4），
// 防止用户输入的通配符触发失控的全表模式匹配。
func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}

func normalizeKeyList(values []string) []string {
	seen := map[string]bool{}
	keys := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.TrimSpace(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func normalizeRoleInput(input RoleInput) RoleInput {
	return RoleInput{
		Key:         strings.TrimSpace(input.Key),
		Alias:       strings.TrimSpace(input.Alias),
		Description: strings.TrimSpace(input.Description),
	}
}

func validRoleInput(input RoleInput) bool {
	if input.Key == "" || input.Alias == "" {
		return false
	}
	// 用户组标识会出现在 URL 路径中，限制为稳定的 ASCII 标识，避免空路径段和转义问题。
	for _, value := range input.Key {
		if !isRoleKeyRune(value) {
			return false
		}
	}
	return true
}

func isRoleKeyRune(value rune) bool {
	return value >= 'a' && value <= 'z' ||
		value >= '0' && value <= '9' ||
		value == '_' ||
		value == '-' ||
		value == '.'
}

func (s *Service) validatePermissions(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	permissions, err := s.store.ListPermissions(ctx)
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(permissions))
	for _, permission := range permissions {
		known[permission.Key] = true
	}
	for _, key := range keys {
		if !known[key] {
			return ErrInvalidPermission
		}
	}
	return nil
}

func (s *Service) validateRoles(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	roles, err := s.store.ListRoles(ctx)
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(roles))
	for _, role := range roles {
		known[role.Key] = true
	}
	for _, key := range keys {
		if !known[key] {
			return ErrInvalidRole
		}
	}
	return nil
}

func hasOverrideConflict(overrides PermissionOverrides) bool {
	allow := make(map[string]bool, len(overrides.Allow))
	for _, key := range overrides.Allow {
		allow[key] = true
	}
	for _, key := range overrides.Deny {
		if allow[key] {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
