package identity

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	localization "github.com/keeptrue/sforum/apps/api/app/Support/Localization"
)

func adminStrPtr(value string) *string { return &value }

func adminStatusPtr(value UserStatus) *UserStatus { return &value }

func adminKindPtr(value UserKind) *UserKind { return &value }

// 管理端保存用户必须逐字段报错：只回一句「用户信息不正确」会让管理员
// 在 10 个字段里猜。这里按字段逐一钉住键名与消息键。
func TestNormalizeAdminUpdateUserInputReportsEveryField(t *testing.T) {
	t.Parallel()
	service, _ := newTestService(t)
	ctx := testContext(t)

	cases := []struct {
		name    string
		input   AdminUpdateUserInput
		field   string
		message string
	}{
		{
			name:    "display name required",
			input:   AdminUpdateUserInput{DisplayName: adminStrPtr("   ")},
			field:   FieldDisplayName,
			message: MessageDisplayNameRequired,
		},
		{
			name:    "display name too long",
			input:   AdminUpdateUserInput{DisplayName: adminStrPtr(strings.Repeat("长", maxAdminDisplayNameLength+1))},
			field:   FieldDisplayName,
			message: MessageDisplayNameTooLong,
		},
		{
			name:    "locale unsupported",
			input:   AdminUpdateUserInput{Locale: adminStrPtr("fr-FR")},
			field:   FieldLocale,
			message: MessageLocaleInvalid,
		},
		{
			name:    "status unknown",
			input:   AdminUpdateUserInput{Status: adminStatusPtr(UserStatus("deleted"))},
			field:   FieldStatus,
			message: MessageStatusInvalid,
		},
		{
			name:    "kind unknown",
			input:   AdminUpdateUserInput{Kind: adminKindPtr(UserKind("robot"))},
			field:   FieldKind,
			message: MessageKindInvalid,
		},
		{
			name:    "bio too long",
			input:   AdminUpdateUserInput{Bio: adminStrPtr(strings.Repeat("b", maxAdminBioLength+1))},
			field:   FieldBio,
			message: MessageBioTooLong,
		},
		{
			name:    "signature too long",
			input:   AdminUpdateUserInput{Signature: adminStrPtr(strings.Repeat("s", maxAdminSignatureLength+1))},
			field:   FieldSignature,
			message: MessageSignatureTooLong,
		},
		{
			name:    "location too long",
			input:   AdminUpdateUserInput{Location: adminStrPtr(strings.Repeat("l", maxAdminLocationLength+1))},
			field:   FieldLocation,
			message: MessageLocationTooLong,
		},
		{
			name:    "website url without scheme",
			input:   AdminUpdateUserInput{WebsiteURL: adminStrPtr("example.com")},
			field:   FieldWebsiteURL,
			message: MessageWebsiteInvalid,
		},
		{
			name:    "website url too long",
			input:   AdminUpdateUserInput{WebsiteURL: adminStrPtr("https://example.com/" + strings.Repeat("a", maxAdminWebsiteLength))},
			field:   FieldWebsiteURL,
			message: MessageWebsiteTooLong,
		},
		{
			name:    "username required",
			input:   AdminUpdateUserInput{Username: adminStrPtr("  ")},
			field:   FieldUsername,
			message: MessageUsernameRequired,
		},
		{
			name:    "email invalid",
			input:   AdminUpdateUserInput{Email: adminStrPtr("not-an-email")},
			field:   FieldEmail,
			message: MessageEmailInvalid,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := service.normalizeAdminUpdateUserInput(ctx, test.input)
			var fieldsErr *AdminUserFieldsError
			if !errors.As(err, &fieldsErr) {
				t.Fatalf("expected AdminUserFieldsError, got %v", err)
			}
			if fieldsErr.Reason != CodeUserInvalidUpdate {
				t.Fatalf("reason = %q, want %q", fieldsErr.Reason, CodeUserInvalidUpdate)
			}
			if !fieldMessagesContain(fieldsErr.Fields, test.field, test.message) {
				t.Fatalf("fields = %+v, want %s -> %s", fieldsErr.Fields, test.field, test.message)
			}
			// 既有的 errors.Is 契约不能因为换了错误类型而失效。
			if !errors.Is(err, ErrInvalidUserUpdate) {
				t.Fatalf("AdminUserFieldsError must unwrap to ErrInvalidUserUpdate, got %v", err)
			}
		})
	}
}

// 一次提交多个非法字段时必须一次全部报出，而不是修一个撞下一个。
func TestNormalizeAdminUpdateUserInputAccumulatesFields(t *testing.T) {
	t.Parallel()
	service, _ := newTestService(t)
	ctx := testContext(t)

	_, err := service.normalizeAdminUpdateUserInput(ctx, AdminUpdateUserInput{
		DisplayName: adminStrPtr(""),
		Locale:      adminStrPtr("de-DE"),
		Status:      adminStatusPtr(UserStatus("deleted")),
		Bio:         adminStrPtr(strings.Repeat("b", maxAdminBioLength+1)),
		WebsiteURL:  adminStrPtr("javascript:alert(1)"),
	})
	var fieldsErr *AdminUserFieldsError
	if !errors.As(err, &fieldsErr) {
		t.Fatalf("expected AdminUserFieldsError, got %v", err)
	}
	for _, field := range []string{FieldDisplayName, FieldLocale, FieldStatus, FieldBio, FieldWebsiteURL} {
		if len(fieldsErr.Fields[field]) == 0 {
			t.Fatalf("field %s missing from %+v", field, fieldsErr.Fields)
		}
	}
}

// 什么都没提交是「空更新」，不是「字段不合法」：前端据此决定是提示「没有改动」
// 还是标红表单。
func TestNormalizeAdminUpdateUserInputReportsEmptyUpdate(t *testing.T) {
	t.Parallel()
	service, _ := newTestService(t)
	ctx := testContext(t)

	_, err := service.normalizeAdminUpdateUserInput(ctx, AdminUpdateUserInput{})
	var fieldsErr *AdminUserFieldsError
	if !errors.As(err, &fieldsErr) {
		t.Fatalf("expected AdminUserFieldsError, got %v", err)
	}
	if fieldsErr.Reason != CodeUserUpdateEmpty {
		t.Fatalf("reason = %q, want %q", fieldsErr.Reason, CodeUserUpdateEmpty)
	}
	if len(fieldsErr.Fields) != 0 {
		t.Fatalf("empty update must not carry field messages, got %+v", fieldsErr.Fields)
	}
}

func TestNormalizeAdminUpdateUserInputAcceptsValidFields(t *testing.T) {
	t.Parallel()
	service, _ := newTestService(t)
	ctx := testContext(t)

	out, err := service.normalizeAdminUpdateUserInput(ctx, AdminUpdateUserInput{
		DisplayName: adminStrPtr("  新昵称  "),
		Locale:      adminStrPtr(""),
		Status:      adminStatusPtr(UserStatusBanned),
		Kind:        adminKindPtr(UserKind("  BOT  ")),
		Bio:         adminStrPtr(" 简介 "),
		Signature:   adminStrPtr(" 签名 "),
		Location:    adminStrPtr(" 上海 "),
		WebsiteURL:  adminStrPtr(" https://example.com "),
	})
	if err != nil {
		t.Fatalf("valid input must normalize, got %v", err)
	}
	if out.DisplayName == nil || *out.DisplayName != "新昵称" {
		t.Fatalf("displayName not normalized: %+v", out.DisplayName)
	}
	// 空语言回落到站点默认语言，而不是报错。
	if out.Locale == nil || *out.Locale != "zh-CN" {
		t.Fatalf("empty locale must fall back to zh-CN, got %+v", out.Locale)
	}
	if out.Kind == nil || *out.Kind != UserKindBot {
		t.Fatalf("kind not normalized: %+v", out.Kind)
	}
	if out.WebsiteURL == nil || *out.WebsiteURL != "https://example.com" {
		t.Fatalf("websiteUrl not trimmed: %+v", out.WebsiteURL)
	}
}

// 上限是编译期常量，而文案把数字写给了用户（「最多 N 个字符」）。常量改了文案没改，
// 提示就会说谎，所以把两者钉在一起。
//
// Profile/types.go 另存了一份同样的上限，但两边的常量都不导出：要做「跨包相等」断言
// 必须导出或迁移常量，那是本轮明确排除的跨包重构。这里守住 Identity 自己的常量与
// user.* 文案的一致性；Profile 那份要改时，同样由它自己的文案断言兜住。
func TestAdminLengthLimitsMatchLocalizedCopy(t *testing.T) {
	t.Parallel()

	cases := []struct {
		key   string
		limit int
	}{
		{MessageDisplayNameTooLong, maxAdminDisplayNameLength},
		{MessageBioTooLong, maxAdminBioLength},
		{MessageSignatureTooLong, maxAdminSignatureLength},
		{MessageLocationTooLong, maxAdminLocationLength},
		{MessageWebsiteTooLong, maxAdminWebsiteLength},
	}

	for _, test := range cases {
		want := strconv.Itoa(test.limit)
		for _, locale := range []string{"zh-CN", "en-US"} {
			if message := localization.Message(locale, test.key); !strings.Contains(message, want) {
				t.Fatalf("%s (%s) must mention the %s-character limit, got %q", test.key, locale, want, message)
			}
		}
	}
}

// 长度按 rune 计数：非 ASCII 网址的字节数远大于字符数，用字节判定会把合法输入拒掉，
// 也会让「最多 200 个字符」的提示与实际判定对不上。
func TestNormalizeAdminUpdateUserInputCountsWebsiteByRune(t *testing.T) {
	t.Parallel()
	service, _ := newTestService(t)
	ctx := testContext(t)

	// 120 个字符、320 个字节：按字符合法，按字节会被误判成超长。
	url := "https://example.com/" + strings.Repeat("例", 100)
	out, err := service.normalizeAdminUpdateUserInput(ctx, AdminUpdateUserInput{WebsiteURL: adminStrPtr(url)})
	if err != nil {
		t.Fatalf("a 120-character URL must be accepted, got %v", err)
	}
	if out.WebsiteURL == nil || *out.WebsiteURL != url {
		t.Fatalf("websiteUrl was not preserved: %+v", out.WebsiteURL)
	}
}

// 后台三条冲突路径（更新预检、store 竞态、创建）共用这个 helper，话术必须是后台语境。
func TestAdminUserConflictFieldsUseAdminCopy(t *testing.T) {
	t.Parallel()

	fields := adminUserConflictFields(true, true)
	if !fieldMessagesContain(fields, FieldUsername, MessageUserUsernameTaken) {
		t.Fatalf("username must use the admin copy, got %+v", fields)
	}
	if !fieldMessagesContain(fields, FieldEmail, MessageUserEmailTaken) {
		t.Fatalf("email must use the admin copy, got %+v", fields)
	}
	if len(adminUserConflictFields(false, false)) != 0 {
		t.Fatal("no conflict must produce no fields")
	}
}

// 角色校验失败要转成带 roleKeys 的错误；其它错误必须原样透出，不能顺手改语义。
func TestMapAdminRoleValidationError(t *testing.T) {
	t.Parallel()

	var roleErr *AdminUserFieldsError
	if err := mapAdminRoleValidationError(ErrInvalidRole); !errors.As(err, &roleErr) {
		t.Fatalf("expected AdminUserFieldsError, got %v", err)
	} else if roleErr.Reason != CodeRoleInvalid {
		t.Fatalf("reason = %q, want %q", roleErr.Reason, CodeRoleInvalid)
	}

	storeErr := errors.New("identity: store unavailable")
	if err := mapAdminRoleValidationError(storeErr); !errors.Is(err, storeErr) {
		t.Fatalf("non-role errors must pass through unchanged, got %v", err)
	}
}
