package trustlevels

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ConfigSchemaVersion 是 trust.levels option JSON 的 schema 版本。
//
// 单键 JSON 的代价是没有逐键归一化兜底，所以解码必须严格：未知字段、未知等级、
// 缺档、越界数值一律拒绝。拒绝比静默丢弃安全——静默丢弃会让运营以为自己保存成功。
const ConfigSchemaVersion = 1

// Config 是 trust.levels option 的**唯一契约形状**。
//
// 前端表单必须逐字段对齐这里：DisallowUnknownFields 会拒绝任何幻影字段（例如
// 早期方案里的 `version` / 逐档 `account_age_days`），错误信息是
// `json: unknown field "..."`，运营看到无法自救——所以改形状必须同步前端与契约。
// 逐档账龄（account_age_days）已被 D-C7 作废：账龄只有 Policy.NewUserDays 一个
// 上限闸，`days_visited` 已经蕴含账龄。
//
// **没有 newUserDays 字段**（R6）：新人窗口的唯一来源是既有的
// `trust.new_user_days` option。JSON 里再存一份就是第二份真相，而且省略该字段时
// 会被静默当成 0 = 把新人闸关掉（D7 的反向）。装配层在组合策略时把
// `trust.new_user_days` 的值填进 Policy.NewUserDays。
type Config struct {
	SchemaVersion int           `json:"schemaVersion"`
	Levels        []LevelConfig `json:"levels"`
}

// LevelConfig 是一档等级的完整配置。
type LevelConfig struct {
	// LevelSlug 是等级的稳定标识（newcomer/member/resident/veteran/leader）。
	// 刻意不叫 slug：TL1 的 slug 是 member，与角色 key 同字符串，裸 slug 会被
	// 当成 roleKey 读错（D21）。
	LevelSlug    string       `json:"levelSlug"`
	Requirements Requirements `json:"requirements"`
	// Limits / Earnable 用 json.RawMessage 才能区分「省略」与「显式 null」：
	// 指针类型下两者都是 nil，而这两种语义必须分开——
	//   省略 → 422（否则运营改一个阈值，该档限额就被静默重置成「全继承」）；
	//   显式 null → 全继承 / 空集；显式 {} / [] → 空覆盖 / 空集。
	Limits   json.RawMessage `json:"limits"`
	Earnable json.RawMessage `json:"earnable"`
}

// ErrInvalidConfig 表示 trust.levels 的值不可用。
var ErrInvalidConfig = errors.New("trustlevels: invalid config")

// DecodeConfig 严格解码 trust.levels 的值。
//
// 严格性（照抄 seo.footer_links 的形态）：
//   - 未知字段拒绝（DisallowUnknownFields）；
//   - schemaVersion 必须等于 ConfigSchemaVersion；
//   - 必须给出 TL1..TL4 四档，且不得重复/越界；
//   - 阈值与限额必须非负；
//   - earnable 走 Validate（存在性 + 管理类禁令 + 父键禁令）。
func DecodeConfig(raw string) (Policy, error) {
	trimmed := bytes.TrimSpace([]byte(raw))
	if len(trimmed) == 0 {
		return DefaultPolicy(), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Policy{}, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	if err := decoder.Decode(new(struct{})); !errors.Is(err, io.EOF) {
		return Policy{}, fmt.Errorf("%w: 尾随内容", ErrInvalidConfig)
	}
	if config.SchemaVersion != ConfigSchemaVersion {
		return Policy{}, fmt.Errorf("%w: schemaVersion 必须为 %d", ErrInvalidConfig, ConfigSchemaVersion)
	}
	policy := DefaultPolicy()
	// NewUserDays 不来自本 JSON（见 Config 注释）：这里保留推荐默认，装配层会用
	// trust.new_user_days 的真实值覆盖它。
	policy.Earnable = nil

	seen := make(map[Level]bool, LevelCount)
	for _, levelConfig := range config.Levels {
		level, ok := parseLevelSlug(levelConfig.LevelSlug)
		if !ok {
			return Policy{}, fmt.Errorf("%w: 未知等级 %q", ErrInvalidConfig, levelConfig.LevelSlug)
		}
		if level == LevelNewcomer {
			return Policy{}, fmt.Errorf("%w: TL0 由 trust.new_user_* 决定，不能在 trust.levels 里配置", ErrInvalidConfig)
		}
		if seen[level] {
			return Policy{}, fmt.Errorf("%w: 等级 %q 重复", ErrInvalidConfig, levelConfig.LevelSlug)
		}
		seen[level] = true
		if err := validateRequirements(levelConfig.Requirements); err != nil {
			return Policy{}, err
		}
		// 省略 limits/earnable 一律拒绝：静默重置策略是保存成功、无人报错的失败模式。
		if len(levelConfig.Limits) == 0 {
			return Policy{}, fmt.Errorf("%w: 等级 %q 缺少 limits 字段；省略会让该档限额被静默重置为「全继承」", ErrInvalidConfig, levelConfig.LevelSlug)
		}
		if len(levelConfig.Earnable) == 0 {
			return Policy{}, fmt.Errorf("%w: 等级 %q 缺少 earnable 字段；省略会静默清空该档的已挣得映射", ErrInvalidConfig, levelConfig.LevelSlug)
		}
		limits, err := decodeLevelLimits(levelConfig.Limits)
		if err != nil {
			return Policy{}, fmt.Errorf("%w: 等级 %q 的 limits: %v", ErrInvalidConfig, levelConfig.LevelSlug, err)
		}
		if err := validateLimits(limits); err != nil {
			return Policy{}, err
		}
		earnable, err := decodeEarnableList(levelConfig.Earnable)
		if err != nil {
			return Policy{}, fmt.Errorf("%w: 等级 %q 的 earnable: %v", ErrInvalidConfig, levelConfig.LevelSlug, err)
		}
		policy.Levels[level] = levelConfig.Requirements
		policy.Limits[level] = limits
		if len(earnable) > 0 {
			if policy.Earnable == nil {
				policy.Earnable = make(Earnable, LevelCount)
			}
			policy.Earnable[level] = earnable
		}
	}
	for level := LevelMember; level < LevelCount; level++ {
		if !seen[level] {
			return Policy{}, fmt.Errorf("%w: 缺少等级 %q", ErrInvalidConfig, level.Slug())
		}
	}
	policy.Earnable = policy.Earnable.Normalize()
	if err := policy.Earnable.Validate(); err != nil {
		return Policy{}, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	return policy, nil
}

// EncodeConfig 把策略编码回 option 值（后台读写与恢复默认共用同一份形状）。
func EncodeConfig(policy Policy) (string, error) {
	config := Config{
		SchemaVersion: ConfigSchemaVersion,
		Levels:        make([]LevelConfig, 0, LevelCount-1),
	}
	for level := LevelMember; level < LevelCount; level++ {
		encodedLimits, err := json.Marshal(policy.Limits[level])
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
		earnable := policy.Earnable.Permissions(level)
		if earnable == nil {
			earnable = []string{}
		}
		encodedEarnable, err := json.Marshal(earnable)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
		config.Levels = append(config.Levels, LevelConfig{
			LevelSlug:    level.Slug(),
			Requirements: policy.Levels[level],
			Limits:       encodedLimits,
			Earnable:     encodedEarnable,
		})
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	return string(encoded), nil
}

func parseLevelSlug(slug string) (Level, bool) {
	for level := LevelNewcomer; level < LevelCount; level++ {
		if level.Slug() == slug {
			return level, true
		}
	}
	return LevelNewcomer, false
}

// decodeLevelLimits 处理三态：null → 全继承；{} → 空覆盖；其余严格解码。
func decodeLevelLimits(raw json.RawMessage) (LevelLimits, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return LevelLimits{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var limits LevelLimits
	if err := decoder.Decode(&limits); err != nil {
		return LevelLimits{}, err
	}
	return limits, nil
}

// decodeEarnableList 处理三态：null → 空集；[] → 空集；其余严格解码。
func decodeEarnableList(raw json.RawMessage) ([]string, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var keys []string
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, err
	}
	return keys, nil
}

func validateRequirements(requirement Requirements) error {
	for _, metric := range Metrics {
		value, ok := requirement.Value(metric)
		if !ok {
			return fmt.Errorf("%w: 未知指标 %q", ErrInvalidConfig, metric)
		}
		if value < 0 {
			return fmt.Errorf("%w: %s 不能为负", ErrInvalidConfig, metric)
		}
	}
	return nil
}

func validateLimits(limits LevelLimits) error {
	for name, value := range map[string]*int{
		"topicCooldownSeconds":   limits.TopicCooldownSeconds,
		"commentCooldownSeconds": limits.CommentCooldownSeconds,
		"dailyTopicLimit":        limits.DailyTopicLimit,
		"dailyCommentLimit":      limits.DailyCommentLimit,
	} {
		if value != nil && *value < 0 {
			return fmt.Errorf("%w: %s 不能为负", ErrInvalidConfig, name)
		}
	}
	return nil
}
