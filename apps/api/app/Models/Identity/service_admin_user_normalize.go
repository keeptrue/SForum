package identity

import (
	"context"
	"strings"
)

// 管理端用户更新的字段校验。它体积不小且与其余账户逻辑耦合很少，拆到独立文件
// 是为了让 service.go 保持在架构门禁的行数上限内，而不是给它加新职责。
//
// 逐字段累积错误而不是遇到第一个就返回：管理员一次改多个字段时，应当一次看到
// 全部问题，而不是修一个再撞下一个。纯归一化在 admin_user_update_validation.go。
func (s *Service) normalizeAdminUpdateUserInput(ctx context.Context, input AdminUpdateUserInput) (AdminUpdateUserInput, error) {
	fields := FieldMessages{}
	// 必须写进 out：调用方用的是返回值，写回 input 会被静默丢弃——
	// 那个错误的表现是「保存成功但值没变」，不会有任何报错。
	out := normalizeAdminUpdateFields(input, fields)

	if input.Username != nil {
		username := strings.TrimSpace(*input.Username)
		usernamePolicy, err := s.resolveUsernamePolicy(ctx)
		if err != nil {
			return AdminUpdateUserInput{}, err
		}
		if username == "" {
			addFieldMessage(fields, FieldUsername, MessageUsernameRequired)
		} else if usernamePolicy.MinLength > 0 || usernamePolicy.MaxLength > 0 || usernamePolicy.Charset != "" || len(usernamePolicy.Reserved) > 0 {
			if ok, reason := usernamePolicy.Validate(username); !ok {
				addFieldMessage(fields, FieldUsername, reason)
			}
		}
		out.Username = &username
	}
	if input.Email != nil {
		email := strings.TrimSpace(*input.Email)
		if email == "" {
			addFieldMessage(fields, FieldEmail, MessageEmailRequired)
		} else if !isValidEmail(email) {
			addFieldMessage(fields, FieldEmail, MessageEmailInvalid)
		}
		out.Email = &email
	}

	if len(fields) > 0 {
		return AdminUpdateUserInput{}, NewAdminUserFieldsError(fields)
	}
	if adminUpdateIsEmpty(out) {
		return AdminUpdateUserInput{}, NewAdminUserUpdateEmptyError()
	}
	return out, nil
}
