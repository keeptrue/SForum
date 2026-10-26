package options

import (
	"context"
	"errors"
	"strconv"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 昵称策略：昵称是展示标签而不是身份，唯一性只属于 username。这里只声明
// 「能不能改、多久改一次、允许多长、曾用名是否公开」。敏感词不另立一份，
// 直接复用 identity.username.reserved，避免两份列表漂移。
//
// 五个选项全部 public：资料设置页要据此决定字段是否可编辑、倒计时文案和
// 长度提示；服务端校验仍是权威，前端只是 UX。
const (
	nicknameCooldownMinutesDefault = 10080 // 7 天
	nicknameCooldownMinutesMax     = 525600
	nicknameMinLengthDefault       = 2
	nicknameMinLengthMin           = 1
	nicknameMinLengthMax           = 32
	nicknameMaxLengthDefault       = 24
	nicknameMaxLengthMin           = 2
	nicknameMaxLengthMax           = 64
)

func init() {
	optionDefinitions = append(optionDefinitions, nicknameOptionDefinitions()...)
}

func nicknameOptionDefinitions() []optionDefinition {
	site := identity.PermissionSettingsSiteManage
	return []optionDefinition{
		{name: NameProfileNicknameAllowUserChange, public: true, managePermission: site},
		{name: NameProfileNicknameCooldownMinutes, public: true, managePermission: site},
		{name: NameProfileNicknameMinLength, public: true, managePermission: site},
		{name: NameProfileNicknameMaxLength, public: true, managePermission: site},
		{name: NameProfileNicknameHistoryPublic, public: true, managePermission: site},
	}
}

// nicknameRecommendedDefaults 的开箱默认：允许改、7 天冷却、2–24 字符、
// 曾用名不公开。运营不改任何设置也能得到一个可用且不打扰他人的昵称体验。
func nicknameRecommendedDefaults() map[string]string {
	return map[string]string{
		NameProfileNicknameAllowUserChange: enabledOptionValue(true),
		NameProfileNicknameCooldownMinutes: strconv.Itoa(nicknameCooldownMinutesDefault),
		NameProfileNicknameMinLength:       strconv.Itoa(nicknameMinLengthDefault),
		NameProfileNicknameMaxLength:       strconv.Itoa(nicknameMaxLengthDefault),
		NameProfileNicknameHistoryPublic:   enabledOptionValue(false),
	}
}

// coercePolicyOptions 汇总同一批「运营策略」选项的脏数据回退：客户端版本与昵称。
// 放在这个文件而不是 service.go，是因为 Service 的行数是架构棘轮。
func coercePolicyOptions(values, defaults map[string]string) {
	coerceClientVersionOptions(values, defaults)
	coerceNicknameOptions(values, defaults)
}

// coerceNicknameOptions 读取路径：脏数据回退到推荐默认，避免一个坏值
// 让资料页整块读不出来。
func coerceNicknameOptions(values, _ map[string]string) {
	// 推荐默认是本组的唯一权威：它不依赖 Defaults 覆盖，缺键时也能落回可用值。
	recommended := nicknameRecommendedDefaults()
	for _, name := range []string{NameProfileNicknameAllowUserChange, NameProfileNicknameHistoryPublic} {
		if value, ok := normalizeEnabledOption(values[name]); ok {
			values[name] = value
			continue
		}
		values[name] = recommended[name]
	}
	cooldown, ok := parseBoundedInt(values[NameProfileNicknameCooldownMinutes], 0, nicknameCooldownMinutesMax)
	if !ok {
		cooldown = nicknameCooldownMinutesDefault
	}
	values[NameProfileNicknameCooldownMinutes] = strconv.Itoa(cooldown)

	minLength, minOK := parseBoundedInt(values[NameProfileNicknameMinLength], nicknameMinLengthMin, nicknameMinLengthMax)
	maxLength, maxOK := parseBoundedInt(values[NameProfileNicknameMaxLength], nicknameMaxLengthMin, nicknameMaxLengthMax)
	if !minOK {
		minLength = nicknameMinLengthDefault
	}
	if !maxOK {
		maxLength = nicknameMaxLengthDefault
	}
	// 交叉约束：上界低于下界时双双回退默认，而不是留下一个无人能通过的区间。
	if maxLength < minLength {
		minLength = nicknameMinLengthDefault
		maxLength = nicknameMaxLengthDefault
	}
	values[NameProfileNicknameMinLength] = strconv.Itoa(minLength)
	values[NameProfileNicknameMaxLength] = strconv.Itoa(maxLength)
}

// NicknamePolicy 复用 Identity 的策略类型：消费方是 Identity，类型就该由它
// 定义，与 UsernamePolicy 的做法一致，避免两套同名字段各写一份。
type NicknamePolicy = identity.NicknamePolicy

// NicknamePolicySource 是昵称策略的读取协作者。Service 的 receiver 方法数量是
// 架构棘轮，新能力通过独立协作者接入，不再往大对象上堆方法。
type NicknamePolicySource struct {
	service *Service
}

func NewNicknamePolicySource(service *Service) *NicknamePolicySource {
	return &NicknamePolicySource{service: service}
}

// NicknamePolicy 读取昵称运行时策略，缺失或非法项回退推荐默认。
func (s *NicknamePolicySource) NicknamePolicy(ctx context.Context) (identity.NicknamePolicy, error) {
	if s == nil || s.service == nil {
		return identity.NicknamePolicy{}, errors.New("options: nickname policy service unavailable")
	}
	values, err := s.service.loadMap(ctx)
	if err != nil {
		return identity.NicknamePolicy{}, err
	}
	return nicknamePolicyFromValues(values), nil
}

func nicknamePolicyFromValues(values map[string]string) identity.NicknamePolicy {
	cooldown, ok := parseBoundedInt(values[NameProfileNicknameCooldownMinutes], 0, nicknameCooldownMinutesMax)
	if !ok {
		cooldown = nicknameCooldownMinutesDefault
	}
	minLength, minOK := parseBoundedInt(values[NameProfileNicknameMinLength], nicknameMinLengthMin, nicknameMinLengthMax)
	if !minOK {
		minLength = nicknameMinLengthDefault
	}
	maxLength, maxOK := parseBoundedInt(values[NameProfileNicknameMaxLength], nicknameMaxLengthMin, nicknameMaxLengthMax)
	if !maxOK {
		maxLength = nicknameMaxLengthDefault
	}
	if maxLength < minLength {
		minLength = nicknameMinLengthDefault
		maxLength = nicknameMaxLengthDefault
	}
	return NicknamePolicy{
		AllowUserChange: isEnabledOption(values[NameProfileNicknameAllowUserChange]),
		CooldownMinutes: cooldown,
		MinLength:       minLength,
		MaxLength:       maxLength,
		HistoryPublic:   isEnabledOption(values[NameProfileNicknameHistoryPublic]),
	}
}
