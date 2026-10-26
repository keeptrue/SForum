package identity

import (
	"context"
	"strconv"
	"strings"

	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

const (
	AdminPasswordModeManual    = "manual"
	AdminPasswordModeGenerated = "generated"
)

// AdminCreateUserInput 是管理端创建用户的领域输入。Password 与 GeneratePassword
// 必须二选一：自设密码走站点密码策略校验，随机密码由服务端生成并只在响应中返回一次。
type AdminCreateUserInput struct {
	Username         string
	Email            string
	DisplayName      string
	Password         string
	GeneratePassword bool
	RoleKeys         []string
	EmailVerified    bool
	Locale           string
}

// AdminCreateUserStoreInput 是落库输入：密码已哈希，passwordMode 只用于审计。
type AdminCreateUserStoreInput struct {
	Username      string
	Email         string
	DisplayName   string
	Locale        string
	PasswordHash  string
	EmailVerified bool
	RoleKeys      []string
	PasswordMode  string
}

type AdminCreateUserResult struct {
	User AdminUserDetail `json:"user"`
	// GeneratedPassword 只在 generatePassword=true 时出现；服务端不持久化明文。
	GeneratedPassword string `json:"generatedPassword,omitempty"`
}

// AdminUserCreator 是「管理端创建用户」的聚焦协作者。
//
// 它刻意不挂在 Service 上：Service 的接收者方法数已到架构上限，而这条路径
// 只依赖 store、事件发布器与两条策略解析器，拆出来比继续给 Service 加方法更清楚。
type AdminUserCreator struct {
	store            Store
	events           appevents.Publisher
	passwordPolicies PasswordPolicyResolver
	usernamePolicy   UsernamePolicyResolver
}

func NewAdminUserCreator(service *Service) *AdminUserCreator {
	if service == nil {
		return nil
	}
	return &AdminUserCreator{
		store:            service.store,
		events:           service.events,
		passwordPolicies: service.passwordPolicies,
		usernamePolicy:   service.usernamePolicy,
	}
}

// Create 管理员直接创建可登录账号。
//
// 与公开注册的区别（刻意为之）：
//   - 不受开放注册开关影响；
//   - 不触发 user.before_register（管理员路径不应被插件拒绝钩子阻塞）；
//   - 仍然完整执行 host 的用户名/邮箱/密码策略、唯一约束与角色保护；
//   - 提交后照常发出 user.registered，actor 记为管理员。
func (c *AdminUserCreator) Create(ctx context.Context, actor Actor, input AdminCreateUserInput) (AdminCreateUserResult, error) {
	if !actor.Can(PermissionUserManage) {
		return AdminCreateUserResult{}, ErrPermissionDenied
	}

	normalized := normalizeAdminCreateUserInput(input)
	policy, err := c.passwordPolicies.PasswordPolicy(ctx)
	if err != nil {
		return AdminCreateUserResult{}, err
	}
	usernamePolicy, err := c.resolveUsernamePolicy(ctx)
	if err != nil {
		return AdminCreateUserResult{}, err
	}

	fields := validateRegisterIdentityFields(normalized.Username, normalized.Email, usernamePolicy)
	if len([]rune(normalized.DisplayName)) > maxAdminDisplayNameLength {
		return AdminCreateUserResult{}, ErrInvalidUserUpdate
	}

	passwordMode := AdminPasswordModeManual
	generatedPassword := ""
	switch {
	case normalized.GeneratePassword && strings.TrimSpace(normalized.Password) != "":
		addFieldMessage(fields, FieldPassword, MessagePasswordModeConflict)
	case normalized.GeneratePassword:
		passwordMode = AdminPasswordModeGenerated
		generatedPassword, err = GenerateCompliantPassword(policy)
		if err != nil {
			return AdminCreateUserResult{}, err
		}
	case strings.TrimSpace(normalized.Password) == "":
		addFieldMessage(fields, FieldPassword, MessagePasswordRequired)
	default:
		for field, messages := range policy.Validate(normalized.Password) {
			fields[field] = append(fields[field], messages...)
		}
	}
	if len(fields) > 0 {
		return AdminCreateUserResult{}, NewRegisterInvalid(fields)
	}

	roleKeys := normalizeKeyList(normalized.RoleKeys)
	if len(roleKeys) == 0 {
		roleKeys = []string{RoleMember}
	}
	// 授予 super_admin 仍要求操作者本身是超管，与 ReplaceUserRoles 的保护对称。
	if containsString(roleKeys, RoleSuperAdmin) && !actor.IsSuperAdmin() {
		return AdminCreateUserResult{}, ErrSuperAdminGrantRestricted
	}
	if err := c.validateRoles(ctx, roleKeys); err != nil {
		return AdminCreateUserResult{}, err
	}

	// 预检冲突给字段级错误；并发竞态由 users 的唯一索引兜底。
	conflicts, err := c.store.FindRegistrationConflicts(ctx, normalized.Username, normalized.Email)
	if err != nil {
		return AdminCreateUserResult{}, err
	}
	if conflictFields := registrationConflictFields(conflicts); len(conflictFields) > 0 {
		return AdminCreateUserResult{}, NewRegisterInvalid(conflictFields)
	}

	plainPassword := normalized.Password
	if passwordMode == AdminPasswordModeGenerated {
		plainPassword = generatedPassword
	}
	passwordHash, err := HashPassword(plainPassword)
	if err != nil {
		return AdminCreateUserResult{}, err
	}

	user, err := c.store.CreateAdminUser(ctx, actor.ID, AdminCreateUserStoreInput{
		Username:      normalized.Username,
		Email:         normalized.Email,
		DisplayName:   normalized.DisplayName,
		Locale:        normalized.Locale,
		PasswordHash:  passwordHash,
		EmailVerified: normalized.EmailVerified,
		RoleKeys:      roleKeys,
		PasswordMode:  passwordMode,
	})
	if err != nil {
		return AdminCreateUserResult{}, err
	}

	envelope := appevents.NewEnvelope(appevents.UserRegistered, map[string]any{
		"userId":   user.ID,
		"username": user.Username,
		"email":    user.Email,
		"locale":   user.Locale,
	})
	envelope.ActorUserID = actor.ID
	envelope.ResourceType = "user"
	envelope.ResourceID = strconv.FormatInt(user.ID, 10)
	c.events.Emit(ctx, envelope)

	return AdminCreateUserResult{User: user, GeneratedPassword: generatedPassword}, nil
}

func (c *AdminUserCreator) resolveUsernamePolicy(ctx context.Context) (UsernamePolicy, error) {
	if c.usernamePolicy == nil {
		return UsernamePolicy{}, nil
	}
	return c.usernamePolicy.UsernamePolicy(ctx)
}

func (c *AdminUserCreator) validateRoles(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	roles, err := c.store.ListRoles(ctx)
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

func normalizeAdminCreateUserInput(input AdminCreateUserInput) AdminCreateUserInput {
	username := strings.TrimSpace(input.Username)
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = username
	}
	locale := strings.TrimSpace(input.Locale)
	if locale == "" {
		locale = "zh-CN"
	}
	roleKeys := make([]string, 0, len(input.RoleKeys))
	for _, key := range input.RoleKeys {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			roleKeys = append(roleKeys, trimmed)
		}
	}
	return AdminCreateUserInput{
		Username:         username,
		Email:            strings.TrimSpace(input.Email),
		DisplayName:      displayName,
		Password:         input.Password,
		GeneratePassword: input.GeneratePassword,
		RoleKeys:         roleKeys,
		EmailVerified:    input.EmailVerified,
		Locale:           locale,
	}
}
