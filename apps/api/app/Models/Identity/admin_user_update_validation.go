package identity

import "strings"

// 管理端用户写入（创建/更新）的稳定错误码。它们只属于后台账户管理这条链路，
// 与注册链路的 auth.* 分开：注册语境把失败提示成「注册失败」会让管理员困惑。
const (
	CodeUserInvalidUpdate    = "user.invalid_update"
	CodeUserUpdateEmpty      = "user.update_empty"
	CodeUsernameOrEmailTaken = "user.username_or_email_taken"
	CodeRoleInvalid          = "role.invalid"
)

// AdminUserFieldsError 是管理端用户写入的字段级校验失败。
//
// 刻意不复用 RegisterInvalidError：那条链路把错误码固定成 auth.register_invalid。
// Unwrap 指向 ErrInvalidUserUpdate，让既有的 errors.Is(err, ErrInvalidUserUpdate)
// 判断（自助语言设置、服务层测试）继续成立。
type AdminUserFieldsError struct {
	// Reason 是稳定错误码，由 transport 层原样透出。构造请走下面的构造函数，
	// 不要留空：空值会让响应缺少契约必填的 reason（transport 侧有兜底，但那是防线）。
	Reason string
	Fields FieldMessages
}

func (e *AdminUserFieldsError) Error() string {
	return "identity: admin user input is invalid: " + e.Reason
}

func (e *AdminUserFieldsError) Unwrap() error { return ErrInvalidUserUpdate }

// NewAdminUserFieldsError 构造字段级校验错误。
func NewAdminUserFieldsError(fields FieldMessages) *AdminUserFieldsError {
	return &AdminUserFieldsError{Reason: CodeUserInvalidUpdate, Fields: fields}
}

// NewAdminUserUpdateEmptyError 表示请求没有提交任何可更新字段。
func NewAdminUserUpdateEmptyError() *AdminUserFieldsError {
	return &AdminUserFieldsError{Reason: CodeUserUpdateEmpty}
}

// NewAdminUserRoleError 表示提交的用户组标识不存在。保留 role.invalid 语义，
// 同时把问题落到 roleKeys 字段上。
func NewAdminUserRoleError() *AdminUserFieldsError {
	return &AdminUserFieldsError{
		Reason: CodeRoleInvalid,
		Fields: FieldMessages{FieldRoleKeys: {MessageRoleUnknown}},
	}
}

// AdminUserConflictError 是用户名/邮箱唯一性冲突，带字段级提示。
// Unwrap 指向 ErrUsernameOrEmailNotUnique，保持既有的 409 判定。
type AdminUserConflictError struct {
	Fields FieldMessages
}

func (e *AdminUserConflictError) Error() string {
	return "identity: admin user username or email is already taken"
}

func (e *AdminUserConflictError) Unwrap() error { return ErrUsernameOrEmailNotUnique }

func NewAdminUserConflictError(fields FieldMessages) *AdminUserConflictError {
	return &AdminUserConflictError{Fields: fields}
}

// adminUserConflictFields 把唯一性冲突结果落成字段消息；两者都空时返回空集，
// 调用方据此决定是否真的报冲突。
//
// 用 user.*_taken 而不是 auth.*_taken：注册话术会要求「请直接登录」，
// 而管理员是在改别人的资料，那句话在这里是错的。
func adminUserConflictFields(usernameTaken bool, emailTaken bool) FieldMessages {
	fields := FieldMessages{}
	if usernameTaken {
		addFieldMessage(fields, FieldUsername, MessageUserUsernameTaken)
	}
	if emailTaken {
		addFieldMessage(fields, FieldEmail, MessageUserEmailTaken)
	}
	return fields
}

// normalizeAdminUpdateFields 归一化不需要策略解析的账户/资料字段，并把不合法的值
// 记进 fields（键与 JSON tag 一致，前端可直接对到表单控件）。
//
// username/email 需要用户名策略（可能读站点设置），由调用方另行处理；两个函数
// 合起来才是一次完整的归一化。
func normalizeAdminUpdateFields(input AdminUpdateUserInput, fields FieldMessages) AdminUpdateUserInput {
	out := AdminUpdateUserInput{}

	if input.DisplayName != nil {
		displayName := strings.TrimSpace(*input.DisplayName)
		switch {
		case displayName == "":
			addFieldMessage(fields, FieldDisplayName, MessageDisplayNameRequired)
		case len([]rune(displayName)) > maxAdminDisplayNameLength:
			addFieldMessage(fields, FieldDisplayName, MessageDisplayNameTooLong)
		default:
			out.DisplayName = &displayName
		}
	}
	if input.Locale != nil {
		locale := strings.TrimSpace(*input.Locale)
		if locale == "" {
			locale = "zh-CN"
		}
		// 仅接受当前产品支持的语言码，避免写入任意字符串。
		if locale == "zh-CN" || locale == "en-US" {
			out.Locale = &locale
		} else {
			addFieldMessage(fields, FieldLocale, MessageLocaleInvalid)
		}
	}
	if input.Status != nil {
		status := *input.Status
		switch status {
		case UserStatusActive, UserStatusDisabled, UserStatusBanned:
			out.Status = &status
		default:
			addFieldMessage(fields, FieldStatus, MessageStatusInvalid)
		}
	}
	if input.Kind != nil {
		// 与用户名、邮箱一致：先归一化再校验。契约里 kind 是小写字面量，
		// 大小写差异是输入噪声而不是另一个值。
		kind := UserKind(strings.ToLower(strings.TrimSpace(string(*input.Kind))))
		if ValidUserKind(kind) {
			out.Kind = &kind
		} else {
			addFieldMessage(fields, FieldKind, MessageKindInvalid)
		}
	}
	if input.Bio != nil {
		bio := strings.TrimSpace(*input.Bio)
		if len([]rune(bio)) > maxAdminBioLength {
			addFieldMessage(fields, FieldBio, MessageBioTooLong)
		} else {
			out.Bio = &bio
		}
	}
	if input.Signature != nil {
		signature := strings.TrimSpace(*input.Signature)
		if len([]rune(signature)) > maxAdminSignatureLength {
			addFieldMessage(fields, FieldSignature, MessageSignatureTooLong)
		} else {
			out.Signature = &signature
		}
	}
	if input.Location != nil {
		location := strings.TrimSpace(*input.Location)
		if len([]rune(location)) > maxAdminLocationLength {
			addFieldMessage(fields, FieldLocation, MessageLocationTooLong)
		} else {
			out.Location = &location
		}
	}
	if input.WebsiteURL != nil {
		url := strings.TrimSpace(*input.WebsiteURL)
		// 按 rune 计数：与 bio/signature/location 一致，否则非 ASCII 网址下
		// 「最多 200 个字符」的提示会和实际判定对不上。
		switch {
		case len([]rune(url)) > maxAdminWebsiteLength:
			addFieldMessage(fields, FieldWebsiteURL, MessageWebsiteTooLong)
		case url != "" && !isHTTPURL(url):
			addFieldMessage(fields, FieldWebsiteURL, MessageWebsiteInvalid)
		default:
			out.WebsiteURL = &url
		}
	}

	return out
}

func isHTTPURL(url string) bool {
	lower := strings.ToLower(url)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

// adminUpdateIsEmpty 判断归一化结果是否一个可更新字段都没有。
// 新增可更新字段时必须同步加进这里，否则「只改这个字段」的请求会被判成
// 什么都没改而拒绝。
func adminUpdateIsEmpty(input AdminUpdateUserInput) bool {
	return input.Username == nil && input.Email == nil && input.DisplayName == nil && input.Locale == nil &&
		input.Status == nil && input.Kind == nil &&
		input.Bio == nil && input.Signature == nil && input.Location == nil && input.WebsiteURL == nil
}
