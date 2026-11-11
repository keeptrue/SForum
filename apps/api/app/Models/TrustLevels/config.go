package trustlevels

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// ConfigSchemaVersion 是 trust.levels option JSON 的当前 schema 版本。
//
// v2 的形状（D-N2）：
//
//	{"schemaVersion":2,"tiers":[
//	  {"id":"member","name":{"zh":"正式用户","en":"Member"},"icon":"leaf",
//	   "color":"teal",
//	   "requirements":{...},"limits":null,"earnable":[],"retired":false}],
//	 "deletedTierIds":["tier_9"]}
//
// 数组顺序 = 判定与展示顺序；TL0 不在 tiers 里。rank 由「active 档位在数组中的位次」
// 派生，不落字段。
//
// `color` 与 `deletedTierIds` 是**可选**字段，两者都 omitempty：没选颜色（跟随位次
// 自动）与没删过档时，写出的值与引入这两个字段之前逐字节相同。这是回滚口径——
// 上一版二进制能正常解码「没用过新功能」的配置；只有运营**真正**选过颜色或删过档时，
// 旧二进制才会因 DisallowUnknownFields 拒绝这些未知字段而整份解码失败（此时回滚
// 必须同时清掉这两个字段，见文末 v1 注释里的同类说明）。
//
// 单键 JSON 的代价是没有逐键归一化兜底，所以解码必须严格：未知字段、未知 id、
// 重复 id、越界数值一律拒绝。拒绝比静默丢弃安全——静默丢弃会让运营以为保存成功。
//
// 拒绝时带回**字段级明细**（D-N14）：机器可判的码仍是一个（ErrInvalidConfig），
// 但每条错误都结构化地带 rank / tierId / field / reasonCode / params，HTTP 层投影成
// 数组下发。运营看到「第 3 档的图标不合法」才自救得了；只看到「配置无效」等于没提示。
//
// **文案纪律**：本包不输出可展示文案。每条明细给一个稳定且可枚举的 reasonCode，
// 由前端拼 i18n；reason 只是人读兜底（日志、没有 i18n 键的旧前端），不用于展示。
const ConfigSchemaVersion = 2

// legacyConfigSchemaVersion 是 v1 的形状（定长五档 levels[] + levelSlug）。
//
// **只在解码时接受**：存量站点的 option 值就是 v1，读完必须在内存里升级成 v2，
// 否则升级到本版本的那一次部署会让所有存量站点「开机即丢配置」。
//
// 升级的**写入时机**（D-N13）：只在写路径（options 的 normalizeTrustLevelsOption 与
// 后台 PUT /admin/trust-levels）把值落成 v2。DecodeConfig 本身纯读、不写库；
// 启动与所有读路径都不改库值。理由是读路径一旦顺手回写，就变成「谁先读谁决定数据」，
// 灰度/回滚时的数据状态无法预测。
//
// **回滚代价**：v2 是本版本的唯一写出形状，旧版本二进制无法解码 v2。所以回滚必须
// **同时**回退 trust.levels 的值（恢复成 v1 JSON），否则旧二进制会整份配置解码失败，
// 等级判定退回「未解析」而静默失效。这不是缓存问题，是数据形状的单项升级。
const legacyConfigSchemaVersion = 1

// MaxConfigErrorDetails 是一次解码最多带回的字段级明细条数。
//
// 有上界不是为了省内存，是为了让「一份全坏的配置」不把响应刷成几百条：运营修的
// 永远是前几条，剩下的在下次保存时自然浮现。
const MaxConfigErrorDetails = 8

// 字段级明细的**稳定代码**（D-N14）。取值域一旦定下就是契约：OpenAPI 的 enum 与
// 前端 i18n 都以它为准，改名等于破坏兼容。新增只能追加。
const (
	CodeSchemaVersionUnsupported = "schema_version_unsupported"
	CodeSchemaShapeInvalid       = "schema_shape_invalid"
	CodeJSONSyntaxInvalid        = "json_syntax_invalid"
	CodeJSONTypeMismatch         = "json_type_mismatch"
	CodeUnknownField             = "unknown_field"
	CodeConfigUnparseable        = "config_unparseable"
	CodeTailContent              = "tail_content"

	CodeTierCountOutOfRange  = "tier_count_out_of_range"
	CodeIDInvalid            = "id_invalid"
	CodeIDReservedForTL0     = "id_reserved_for_tl0"
	CodeIDDuplicate          = "id_duplicate"
	CodeIDDerivationConflict = "id_derivation_conflict"
	CodeIconNotWhitelisted   = "icon_not_whitelisted"
	CodeColorNotWhitelisted  = "color_not_whitelisted"
	CodeNameMissing          = "name_missing"
	CodeNameTooLong          = "name_too_long"

	CodeRequirementNegative      = "requirement_negative"
	CodeRequirementUnknownMetric = "requirement_unknown_metric"
	CodeLimitsMissing            = "limits_missing"
	CodeLimitsUnparseable        = "limits_unparseable"
	CodeLimitNegative            = "limit_negative"
	CodeEarnableMissing          = "earnable_missing"
	CodeEarnableUnparseable      = "earnable_unparseable"
	CodeEarnableKeyEmpty         = "earnable_key_empty"
	CodeEarnableNotWhitelisted   = "earnable_not_whitelisted"
	CodeEarnableInvalid          = "earnable_invalid"

	CodeLevelUnknown        = "level_unknown"
	CodeLevelReservedForTL0 = "level_reserved_for_tl0"
	CodeLevelDuplicate      = "level_duplicate"
	CodeLevelLegacyMissing  = "level_legacy_missing"

	// 删除墓碑（deletedTierIds）的三条判据。它们是**配置级**字段，因此 rank 恒为 0、
	// tierId 就是墓碑本身——运营要定位的是「这一条墓碑写错了」，不是某一档。
	CodeDeletedTierIDInvalid   = "deleted_tier_id_invalid"
	CodeDeletedTierIDDuplicate = "deleted_tier_id_duplicate"
	CodeDeletedTierIDPresent   = "deleted_tier_id_present"
)

// configErrorReasonCodes 是全部稳定代码（供契约/测试枚举；顺序稳定）。
var configErrorReasonCodes = []string{
	CodeSchemaVersionUnsupported,
	CodeSchemaShapeInvalid,
	CodeJSONSyntaxInvalid,
	CodeJSONTypeMismatch,
	CodeUnknownField,
	CodeConfigUnparseable,
	CodeTailContent,
	CodeTierCountOutOfRange,
	CodeIDInvalid,
	CodeIDReservedForTL0,
	CodeIDDuplicate,
	CodeIDDerivationConflict,
	CodeIconNotWhitelisted,
	CodeColorNotWhitelisted,
	CodeNameMissing,
	CodeNameTooLong,
	CodeRequirementNegative,
	CodeRequirementUnknownMetric,
	CodeLimitsMissing,
	CodeLimitsUnparseable,
	CodeLimitNegative,
	CodeEarnableMissing,
	CodeEarnableUnparseable,
	CodeEarnableKeyEmpty,
	CodeEarnableNotWhitelisted,
	CodeEarnableInvalid,
	CodeLevelUnknown,
	CodeLevelReservedForTL0,
	CodeLevelDuplicate,
	CodeLevelLegacyMissing,
	CodeDeletedTierIDInvalid,
	CodeDeletedTierIDDuplicate,
	CodeDeletedTierIDPresent,
}

// ConfigErrorReasonCodes 返回稳定代码表（副本）。OpenAPI 的 enum 与前端 i18n 键
// 都必须覆盖这里，增删它等于改契约。
func ConfigErrorReasonCodes() []string {
	codes := make([]string, len(configErrorReasonCodes))
	copy(codes, configErrorReasonCodes)
	return codes
}

// Config 是 trust.levels option 的**唯一契约形状**（v1 与 v2 共用同一个解码结构）。
//
// 前端表单必须逐字段对齐这里：DisallowUnknownFields 会拒绝任何幻影字段（例如
// 早期方案里的 `version` / 逐档 `account_age_days`），错误信息是
// `json: unknown field "..."`，运营看到无法自救——所以改形状必须同步前端与契约。
//
// 逐档账龄（account_age_days）已被 D-C7 作废：账龄只有 Policy.NewUserDays 一个
// 上限闸，`days_visited` 已经蕴含账龄。
//
// **没有 newUserDays 字段**（R6）：新人窗口的唯一来源是既有的
// `trust.new_user_days` option。JSON 里再存一份就是第二份真相，而且省略该字段时
// 会被静默当成 0 = 把新人闸关掉（D7 的反向）。装配层在组合策略时把
// `trust.new_user_days` 的值填进 Policy.NewUserDays。
type Config struct {
	SchemaVersion int `json:"schemaVersion"`
	// Tiers 是 v2 的档位数组。省略即按 0 档处理，由校验拒绝（active 至少 1 档）。
	Tiers []TierConfig `json:"tiers,omitempty"`
	// DeletedTierIDs 是删除墓碑：运营真正删掉的档位 id（按提交顺序）。
	//
	// omitempty 是回滚口径：没删过档就不写这个字段，上一版二进制仍能解码本值。
	// 解码期逐条校验（语法、重复、与 tiers 冲突）：重复不是去重接受，而是 422；
	// 任何一条不合法都整份拒绝，绝不静默丢弃。
	DeletedTierIDs []string `json:"deletedTierIds,omitempty"`
	// Levels 只为读入存量 v1 配置保留；编码永远不写它。v2 里出现本字段会被拒绝，
	// 反之亦然——两个形状不允许在同一份值里混着出现。
	//
	// 用指针而不是切片：`"levels":[]` 与「没有 levels 字段」必须能区分。切片下两者
	// 都是 len 0，会让「v2 里混进一个空 levels」静默通过。
	Levels *[]legacyLevelConfig `json:"levels,omitempty"`
}

// TierConfig 是一档等级的完整 v2 配置。
type TierConfig struct {
	// ID 是不可变身份（D-N1）。省略/空串表示「新档」：序号由解码期派生（D-N6），
	// 客户端不能自己指定一个新 id。已有档位必须原样回传，否则等于换身份。
	ID string `json:"id"`
	// Name 是运营可改的双语显示名。命名池（D-N3）只是编辑辅助，不存储。
	Name LocalizedName `json:"name"`
	// Icon 必须在 TierIcons() 白名单内；空串表示「用该 rank 的默认图标」（D-N4）。
	Icon string `json:"icon"`
	// Color 必须在 TierColors() 白名单内；**空串表示「跟随位次自动」**，解码期不填
	// 默认值（填了就把「自动」冻成一次性取值）。omitempty 是回滚口径：没选过颜色的
	// 档位不写这个字段，上一版二进制仍能解码。
	Color        string          `json:"color,omitempty"`
	Requirements Requirements    `json:"requirements"`
	Limits       json.RawMessage `json:"limits"`
	Earnable     json.RawMessage `json:"earnable"`
	// Retired 是软停用（D-N2）：配置保留、判定与 UI 跳过。
	Retired bool `json:"retired"`
}

// legacyLevelConfig 是 v1 的一档（levels[] + levelSlug）。
//
// 结构与 v1 时代逐字节相同：它只用于把存量值读进来，不参与任何写出路径。
type legacyLevelConfig struct {
	// LevelSlug 是 v1 的等级标识（member/resident/veteran/leader）。
	// 刻意不叫 slug：TL1 的 slug 是 member，与角色 key 同字符串，裸 slug 会被
	// 当成 roleKey 读错（D21）。
	LevelSlug    string          `json:"levelSlug"`
	Requirements Requirements    `json:"requirements"`
	Limits       json.RawMessage `json:"limits"`
	Earnable     json.RawMessage `json:"earnable"`
}

// ErrInvalidConfig 表示 trust.levels 的值不可用。
var ErrInvalidConfig = errors.New("trustlevels: invalid config")

// FieldError 是一条字段级错误明细（D-N14）。
//
// 它只描述**解码期已经判定的事实**，控制器不得在传输层重新判断业务规则：
// 那是第二份校验，迟早与这一份漂移。
type FieldError struct {
	// Rank 是出错的档位序号（TL1 起）。0 表示无法定位到具体档位：
	// 全局字段（schemaVersion / 结构混用）、JSON 语法错误，或出错的是一条
	// 停用档位（停用档位不占 rank，用 TierID 定位）。
	Rank Level
	// TierID 是出错的档位身份；新档（id 由解码期派生）或全局错误时为空串。
	TierID string
	// Field 是稳定的字段路径：schemaVersion / tiers / levels / id / levelSlug /
	// name.zh / name.en / icon / requirements.<metric> / limits /
	// limits.<limitKey> / earnable；空串表示无法归因到字段（JSON 语法错误等）。
	Field string
	// Code 是稳定的机器可判代码（见 Code* 常量与 ConfigErrorReasonCodes）。
	// 前端按它拼 i18n 文案；**这是明细里唯一参与展示决策的字段**。
	Code string
	// Params 是插值参数（图标名、指标名、长度上限、当前档数这类动态值）。
	Params map[string]any
	// Reason 是人读兜底：只用于服务端日志与「没有 i18n 键的旧前端」。
	// **不用于正式展示**——本仓口径是后端不输出文案，文案由前端按 Code 拼。
	Reason string
}

// ConfigErrors 聚合一份配置的字段级明细。
//
// 它仍然满足 errors.Is(err, ErrInvalidConfig)：调用方原有的「整体拒绝」判断不用改，
// 想展示明细的调用方用 ConfigErrorDetails 取结构化数据，不靠正则解析错误字符串。
type ConfigErrors struct {
	Details []FieldError
}

func (e *ConfigErrors) Error() string {
	if e == nil || len(e.Details) == 0 {
		return ErrInvalidConfig.Error()
	}
	first := e.Details[0]
	if len(e.Details) == 1 {
		return fmt.Sprintf("%v: [%s] %s", ErrInvalidConfig, first.Code, first.Reason)
	}
	return fmt.Sprintf("%v: [%s] %s（另有 %d 条字段级问题）", ErrInvalidConfig, first.Code, first.Reason, len(e.Details)-1)
}

// Unwrap 让 errors.Is(err, ErrInvalidConfig) 继续成立。
func (e *ConfigErrors) Unwrap() error { return ErrInvalidConfig }

// ConfigErrorDetails 提取字段级明细（副本）。不是结构化错误时返回 nil——
// 调用方据此回退到「只给统一错误码」的旧行为，而不是自己解析字符串。
func ConfigErrorDetails(err error) []FieldError {
	var configErrors *ConfigErrors
	if !errors.As(err, &configErrors) || configErrors == nil || len(configErrors.Details) == 0 {
		return nil
	}
	details := make([]FieldError, len(configErrors.Details))
	copy(details, configErrors.Details)
	return details
}

// configErrorCollector 收集解码期的字段级明细，到上界即停止接受新条目。
type configErrorCollector struct {
	details []FieldError
}

// add 记录一条明细；code 必须取自 Code* 常量（契约由 ConfigErrorReasonCodes 兜住）。
func (c *configErrorCollector) add(code string, rank Level, tierID, field string, params map[string]any, reason string) {
	if len(c.details) >= MaxConfigErrorDetails {
		return
	}
	c.details = append(c.details, FieldError{
		Rank: rank, TierID: tierID, Field: field, Code: code, Params: params, Reason: reason,
	})
}

// addDetail 追加一条已经构造好的明细（用于延后追加的校验：先算、后报）。
func (c *configErrorCollector) addDetail(detail FieldError) {
	if c == nil || len(c.details) >= MaxConfigErrorDetails {
		return
	}
	c.details = append(c.details, detail)
}

func (c *configErrorCollector) err() error {
	if c == nil || len(c.details) == 0 {
		return nil
	}
	return &ConfigErrors{Details: c.details}
}

// singleConfigError 造一条「到此为止」的结构性错误（JSON 坏、版本不符、形状混用）。
// 这类错误没有档位上下文，强行归类只会误导。
func singleConfigError(code, field, reason string) error {
	return &ConfigErrors{Details: []FieldError{{Field: field, Code: code, Reason: reason}}}
}

// classifyDecodeError 把 encoding/json 的错误归类成稳定代码。
//
// 只有 unknown field 那一条依赖 stdlib 的消息前缀：DisallowUnknownFields 返回的是
// 无类型的 errorString，没有可 type-switch 的东西。退化（措辞变化）时归到
// config_unparseable——更笼统，但不会错标。
func classifyDecodeError(err error) (code, field string) {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return CodeJSONTypeMismatch, typeErr.Field
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return CodeJSONSyntaxInvalid, ""
	}
	if strings.HasPrefix(err.Error(), "json: unknown field") {
		return CodeUnknownField, ""
	}
	return CodeConfigUnparseable, ""
}

// DecodeConfig 严格解码 trust.levels 的值，同时接受 v1 与 v2（D-N5）。
//
// 严格性（照抄 seo.footer_links 的形态）：
//   - 未知字段拒绝（DisallowUnknownFields，含嵌套对象）；
//   - schemaVersion 必须是 1 或 2，且不能混用两套字段；
//   - v2：active 档数 1..MaxTiers、id 唯一且合法、name 双语齐全且有长度上限、
//     icon 在白名单内、阈值与限额非负；
//   - v1：必须给出 TL1..TL4 四档，且不得重复/越界（与 v1 时代的判据一致）；
//   - earnable 走白名单校验（存在性 + 管理类禁令 + 父键禁令）。
//
// 解码失败整体拒绝，不做部分修正：静默修正会让「保存成功但策略不是运营以为的那份」
// 变成常态。拒绝时返回 *ConfigErrors（最多 MaxConfigErrorDetails 条明细），
// errors.Is(err, ErrInvalidConfig) 仍成立。
func DecodeConfig(raw string) (Policy, error) {
	trimmed := bytes.TrimSpace([]byte(raw))
	if len(trimmed) == 0 {
		return DefaultPolicy(), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		code, field := classifyDecodeError(err)
		return Policy{}, singleConfigError(code, field, err.Error())
	}
	if err := decoder.Decode(new(struct{})); !errors.Is(err, io.EOF) {
		return Policy{}, singleConfigError(CodeTailContent, "", "尾随内容")
	}
	switch config.SchemaVersion {
	case ConfigSchemaVersion:
		if config.Levels != nil {
			return Policy{}, singleConfigError(CodeSchemaShapeInvalid, "levels", "schemaVersion 2 不接受 levels 字段（旧形状请整体改写为 tiers）")
		}
		return decodeV2(config.Tiers, config.DeletedTierIDs)
	case legacyConfigSchemaVersion:
		if len(config.Tiers) > 0 {
			return Policy{}, singleConfigError(CodeSchemaShapeInvalid, "tiers",
				fmt.Sprintf("schemaVersion 1 不接受 tiers 字段（请把 schemaVersion 改为 %d）", ConfigSchemaVersion))
		}
		if config.Levels == nil {
			return Policy{}, singleConfigError(CodeSchemaShapeInvalid, "levels", "schemaVersion 1 缺少 levels 字段")
		}
		return decodeLegacyV1(*config.Levels)
	default:
		return Policy{}, singleConfigError(CodeSchemaVersionUnsupported, "schemaVersion",
			fmt.Sprintf("schemaVersion 必须为 %d（当前）或 %d（旧形状，仍被接受）", ConfigSchemaVersion, legacyConfigSchemaVersion))
	}
}

// decodeV2 解码 v2 档位数组。分两遍：先校验并收集已声明的 id，再给「新档」
// （id 为空）按 NextTierSeq 顺序派生 id——派生必须看到整份配置的最大序号，
// 否则同一份配置里两个新档会撞号。删除墓碑（deletedTierIds）在第二遍之前校验，
// 因为它们占过的数字后缀同样参与「最大序号」。
//
// 校验不短路到第一条错误：同一次保存里档位 2 缺 limits、档位 3 图标非法，运营
// 应该一次看到两条，而不是修一条再撞下一条（上界 MaxConfigErrorDetails）。
func decodeV2(tierConfigs []TierConfig, deletedTierIDs []string) (Policy, error) {
	if len(tierConfigs) == 0 {
		return Policy{}, singleConfigError(CodeTierCountOutOfRange, "tiers",
			fmt.Sprintf("tiers 至少要有 %d 档（TL0 固定存在，不需要配置）", MinTiers))
	}
	policy := DefaultPolicy()
	policy.Tiers = make([]Tier, 0, len(tierConfigs))
	// NewUserDays 不来自本 JSON（见 Config 注释）：这里保留推荐默认，装配层会用
	// trust.new_user_days 的真实值覆盖它。
	policy.Earnable = nil

	collector := &configErrorCollector{}
	seen := make(map[string]bool, len(tierConfigs))
	maxSeq := len(builtinTierIdentities)
	// activeHint 与主循环的 activeCount 同口径：停用档不占 rank（rank 恒 0、靠
	// tierId 定位）。用数组下标当 rank 会把「停用档排在最前」时的 id 级错误误报成
	// 「第 1 档」，与 FieldError.Rank 的约定自相矛盾。
	activeHint := Level(0)
	for _, tierConfig := range tierConfigs {
		rankHint := Level(0)
		if !tierConfig.Retired {
			activeHint++
			rankHint = activeHint
		}
		if tierConfig.ID == "" {
			continue
		}
		label := fmt.Sprintf("第 %d 档", rankHint)
		if rankHint == Level(0) {
			label = "停用档位"
		}
		switch {
		case !ValidTierID(tierConfig.ID):
			collector.add(CodeIDInvalid, rankHint, tierConfig.ID, "id",
				map[string]any{"id": tierConfig.ID},
				fmt.Sprintf("%s 的 id %q 不合法（只允许 newcomer/member/resident/veteran/leader 或 tier_<数字>）", label, tierConfig.ID))
			continue
		case tierConfig.ID == NewcomerID:
			collector.add(CodeIDReservedForTL0, rankHint, tierConfig.ID, "id",
				map[string]any{"id": tierConfig.ID},
				fmt.Sprintf("id %q 保留给 TL0（TL0 由 trust.new_user_* 决定，不能配置）", NewcomerID))
			continue
		case seen[tierConfig.ID]:
			collector.add(CodeIDDuplicate, rankHint, tierConfig.ID, "id",
				map[string]any{"id": tierConfig.ID},
				fmt.Sprintf("id %q 重复：同一份配置里两个档位不能共用一个身份", tierConfig.ID))
			continue
		}
		seen[tierConfig.ID] = true
		if seq, ok := tierSeq(tierConfig.ID); ok && seq > maxSeq {
			maxSeq = seq
		}
	}

	// 墓碑先校验、再生产：它们的数字后缀必须抬高标准，否则「删掉 tier_7 后新建一档」
	// 会当场把 tier_7 派给新档（id 复用）。明细本身延后追加，让运营先看到档位级问题。
	normalizedDeleted, tombstoneMaxSeq, tombstoneDetails := validateDeletedTierIDs(deletedTierIDs, seen)
	if tombstoneMaxSeq > maxSeq {
		maxSeq = tombstoneMaxSeq
	}

	earnableByRank := make(map[Level][]string, len(tierConfigs))
	activeCount := 0
	nextSeq := maxSeq + 1
	for _, tierConfig := range tierConfigs {
		id := tierConfig.ID
		if id == "" {
			// D-N6：新档 id 由解码期派生，不接受客户端指定。只软删除意味着
			// 派生上界只会前进，所以 id 永不复用。
			id = NewTierID(nextSeq)
			nextSeq++
			if seen[id] {
				collector.add(CodeIDDerivationConflict, 0, id, "id",
					map[string]any{"id": id}, fmt.Sprintf("派生出的 id %q 已被占用", id))
				continue
			}
			seen[id] = true
		}
		// 默认图标按「若为 active 时的 rank」推导：停用档位也保留一份确定性图标，
		// 恢复时不需要运营重新选。
		wouldBeRank := Level(activeCount + 1)
		if tierConfig.Retired {
			// 停用档位不占 rank：用 tierId 定位，避免报出「第 5 档」而它在 UI 上是停用项。
			wouldBeRank = 0
		}
		icon := tierConfig.Icon
		if icon == "" {
			icon = DefaultTierIcon(Level(activeCount + 1))
		} else if !IsTierIcon(icon) {
			collector.add(CodeIconNotWhitelisted, wouldBeRank, id, "icon",
				map[string]any{"icon": icon},
				fmt.Sprintf("档位 %q 的 icon %q 不合法（16 个默认短名，或 i-lucide-* / i-tabler-* 全名）", id, icon))
		}
		// 颜色：空串是合法值（= 跟随位次自动），**不填默认**；非空必须是默认 token
		// 或 #rrggbb。hex 先归一化（去空白 + 转小写）再校验与存储，让落库值确定。
		color := strings.ToLower(strings.TrimSpace(tierConfig.Color))
		if color != "" && !IsTierColor(color) {
			collector.add(CodeColorNotWhitelisted, wouldBeRank, id, "color",
				map[string]any{"color": tierConfig.Color},
				fmt.Sprintf("档位 %q 的 color %q 不合法（9 个默认 token，或 #rrggbb 六位 hex）", id, tierConfig.Color))
		}
		validateTierNameInto(collector, wouldBeRank, id, tierConfig.Name)
		validateRequirementsInto(collector, wouldBeRank, id, tierConfig.Requirements)
		// 省略 limits/earnable 一律拒绝：静默重置策略是保存成功、无人报错的失败模式。
		limits := LevelLimits{}
		if len(tierConfig.Limits) == 0 {
			collector.add(CodeLimitsMissing, wouldBeRank, id, "limits", nil,
				fmt.Sprintf("档位 %q 缺少 limits 字段；省略会让该档限额被静默重置为「全继承」", id))
		} else if decoded, err := decodeLevelLimits(tierConfig.Limits); err != nil {
			collector.add(CodeLimitsUnparseable, wouldBeRank, id, "limits", nil,
				fmt.Sprintf("档位 %q 的 limits 无法解析：%v", id, err))
		} else {
			limits = decoded
			validateLimitsInto(collector, wouldBeRank, id, limits)
		}
		var earnable []string
		if len(tierConfig.Earnable) == 0 {
			collector.add(CodeEarnableMissing, wouldBeRank, id, "earnable", nil,
				fmt.Sprintf("档位 %q 缺少 earnable 字段；省略会静默清空该档的已挣得映射", id))
		} else if decoded, err := decodeEarnableList(tierConfig.Earnable); err != nil {
			collector.add(CodeEarnableUnparseable, wouldBeRank, id, "earnable", nil,
				fmt.Sprintf("档位 %q 的 earnable 无法解析：%v", id, err))
		} else {
			earnable = decoded
			validateEarnableInto(collector, wouldBeRank, id, earnable)
		}

		tier := Tier{
			ID:           id,
			Name:         tierConfig.Name,
			Icon:         icon,
			Color:        color,
			Requirements: tierConfig.Requirements,
			Limits:       limits,
			Earnable:     earnable,
			Retired:      tierConfig.Retired,
		}
		policy.Tiers = append(policy.Tiers, tier)
		if tier.Retired {
			// 停用档位保留完整配置，但不参与 rank 编号、判定与已挣得映射。
			continue
		}
		activeCount++
		if activeCount <= MaxTiers {
			earnableByRank[Level(activeCount)] = earnable
		}
	}
	if activeCount > MaxTiers || activeCount < MinTiers {
		collector.add(CodeTierCountOutOfRange, 0, "", "tiers",
			map[string]any{"active": activeCount, "min": MinTiers, "max": MaxTiers},
			fmt.Sprintf("active 档位必须在 %d..%d 之间（当前 %d，停用档位不计入）", MinTiers, MaxTiers, activeCount))
	}
	// 墓碑明细最后追加：档位级问题永远比「墓碑写错了」更该先被看到。
	for _, detail := range tombstoneDetails {
		collector.addDetail(detail)
	}
	if err := collector.err(); err != nil {
		return Policy{}, err
	}

	// 校验通过的墓碑顺序即提交顺序：读面把它原样回给前端，前端靠它区分
	// 「已删除」与「从没配过」。重复项已在上面被 422 拒绝，这里不存在去重。
	policy.DeletedTierIDs = normalizedDeleted

	policy.Earnable = make(Earnable, activeCount)
	for rank, keys := range earnableByRank {
		if len(keys) > 0 {
			policy.Earnable[rank] = keys
		}
	}
	policy.Earnable = policy.Earnable.Normalize()
	// 兜底网：逐档校验已经做过白名单判断，这里再断言一次整份映射，
	// 防止将来有人新增一条绕过逐档校验的写入路径。
	if err := policy.Earnable.Validate(); err != nil {
		return Policy{}, singleConfigError(CodeEarnableInvalid, "earnable", err.Error())
	}
	return policy, nil
}

// decodeLegacyV1 把 v1 的定长五档在**内存里**升级成 v2（D-N5）。
//
// 升级规则：id = 原 levelSlug、name = 内置双语名、icon = 默认序列、retired = false。
// 内置四档的 id 与 v1 的 levelSlug 逐字相同，所以 levelSlug 对外契约不变；差别只在
// 多出 name/icon 两个展示字段。
func decodeLegacyV1(levelConfigs []legacyLevelConfig) (Policy, error) {
	policy := DefaultPolicy()
	policy.Tiers = make([]Tier, 0, len(levelConfigs))
	policy.Earnable = nil

	collector := &configErrorCollector{}
	byRank := make(map[Level]legacyLevelConfig, len(levelConfigs))
	for _, levelConfig := range levelConfigs {
		rank, ok := parseLegacyLevelSlug(levelConfig.LevelSlug)
		switch {
		case !ok:
			collector.add(CodeLevelUnknown, 0, levelConfig.LevelSlug, "levelSlug",
				map[string]any{"levelSlug": levelConfig.LevelSlug},
				fmt.Sprintf("未知等级 %q", levelConfig.LevelSlug))
			continue
		case rank == LevelNewcomer:
			collector.add(CodeLevelReservedForTL0, 0, levelConfig.LevelSlug, "levelSlug",
				map[string]any{"levelSlug": levelConfig.LevelSlug},
				"TL0 由 trust.new_user_* 决定，不能在 trust.levels 里配置")
			continue
		}
		if _, duplicated := byRank[rank]; duplicated {
			collector.add(CodeLevelDuplicate, rank, levelConfig.LevelSlug, "levelSlug",
				map[string]any{"levelSlug": levelConfig.LevelSlug},
				fmt.Sprintf("等级 %q 重复", levelConfig.LevelSlug))
			continue
		}
		byRank[rank] = levelConfig
	}
	for rank := LevelMember; rank < Level(len(builtinTierIdentities)); rank++ {
		if _, ok := byRank[rank]; !ok {
			collector.add(CodeLevelLegacyMissing, rank, BuiltinTierID(rank), "levels",
				map[string]any{"level": BuiltinTierID(rank)},
				fmt.Sprintf("缺少等级 %q", BuiltinTierID(rank)))
		}
	}
	if err := collector.err(); err != nil {
		return Policy{}, err
	}

	earnableByRank := make(map[Level][]string, len(levelConfigs))
	for rank := LevelMember; rank < Level(len(builtinTierIdentities)); rank++ {
		levelConfig := byRank[rank]
		id := BuiltinTierID(rank)
		validateRequirementsInto(collector, rank, id, levelConfig.Requirements)
		limits := LevelLimits{}
		if len(levelConfig.Limits) == 0 {
			collector.add(CodeLimitsMissing, rank, id, "limits", nil,
				fmt.Sprintf("等级 %q 缺少 limits 字段；省略会让该档限额被静默重置为「全继承」", levelConfig.LevelSlug))
		} else if decoded, err := decodeLevelLimits(levelConfig.Limits); err != nil {
			collector.add(CodeLimitsUnparseable, rank, id, "limits", nil,
				fmt.Sprintf("等级 %q 的 limits 无法解析：%v", levelConfig.LevelSlug, err))
		} else {
			limits = decoded
			validateLimitsInto(collector, rank, id, limits)
		}
		var earnable []string
		if len(levelConfig.Earnable) == 0 {
			collector.add(CodeEarnableMissing, rank, id, "earnable", nil,
				fmt.Sprintf("等级 %q 缺少 earnable 字段；省略会静默清空该档的已挣得映射", levelConfig.LevelSlug))
		} else if decoded, err := decodeEarnableList(levelConfig.Earnable); err != nil {
			collector.add(CodeEarnableUnparseable, rank, id, "earnable", nil,
				fmt.Sprintf("等级 %q 的 earnable 无法解析：%v", levelConfig.LevelSlug, err))
		} else {
			earnable = decoded
			validateEarnableInto(collector, rank, id, earnable)
		}
		policy.Tiers = append(policy.Tiers, Tier{
			ID:           id,
			Name:         BuiltinTierName(rank),
			Icon:         DefaultTierIcon(rank),
			Requirements: levelConfig.Requirements,
			Limits:       limits,
			Earnable:     earnable,
			Retired:      false,
		})
		earnableByRank[rank] = earnable
	}
	if err := collector.err(); err != nil {
		return Policy{}, err
	}

	policy.Earnable = make(Earnable, len(earnableByRank))
	for rank, keys := range earnableByRank {
		if len(keys) > 0 {
			policy.Earnable[rank] = keys
		}
	}
	policy.Earnable = policy.Earnable.Normalize()
	if err := policy.Earnable.Validate(); err != nil {
		return Policy{}, singleConfigError(CodeEarnableInvalid, "earnable", err.Error())
	}
	return policy, nil
}

// EncodeConfig 把策略编码回 option 值，**只写 v2**（后台读写与恢复默认共用同一份形状）。
//
// 后台读面下发的 `config` 是**规范化后的 v2**（DecodeConfig → EncodeConfig 的投影），
// 不是库里的原始字节：v1 值会被升级、派生 id 会落成字面量、可选字段的省略/空值会被
// 抹平。所以「读到的 config」只保证语义等价，不保证与库内字节逐字相同。
//
// active 档位的 earnable 以 Policy.Earnable（rank 键）为准；retired 档位的配置不在
// rank 映射里，直接取 Tier.Earnable —— 软停用要保留运营原本勾的那几个键。
func EncodeConfig(policy Policy) (string, error) {
	config := Config{
		SchemaVersion:  ConfigSchemaVersion,
		Tiers:          make([]TierConfig, 0, len(policy.Tiers)),
		DeletedTierIDs: policy.DeletedTierIDs,
	}
	rank := Level(0)
	for _, tier := range policy.Tiers {
		if !tier.Retired {
			rank++
		}
		keys := tier.Earnable
		if !tier.Retired {
			if configured := policy.Earnable.Permissions(rank); configured != nil {
				keys = configured
			}
		}
		if keys == nil {
			keys = []string{}
		}
		encodedLimits, err := json.Marshal(tier.Limits)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
		encodedEarnable, err := json.Marshal(keys)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
		config.Tiers = append(config.Tiers, TierConfig{
			ID:           tier.ID,
			Name:         tier.Name,
			Icon:         tier.Icon,
			Color:        tier.Color,
			Requirements: tier.Requirements,
			Limits:       encodedLimits,
			Earnable:     encodedEarnable,
			Retired:      tier.Retired,
		})
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	return string(encoded), nil
}

// parseLegacyLevelSlug 把 v1 的 levelSlug 折回 rank（含 newcomer，便于给出精确报错）。
func parseLegacyLevelSlug(slug string) (Level, bool) {
	for rank := LevelNewcomer; rank < Level(len(builtinTierIdentities)); rank++ {
		if BuiltinTierID(rank) == slug {
			return rank, true
		}
	}
	return LevelNewcomer, false
}

// validateTierNameInto 校验双语名：两种语言都必须有、且不超过长度上限。
//
// 两个都必填而不是「至少一种」：UI 是双语的，缺一种就会在另一个 locale 显示空档位名，
// 那种缺失只有切语言才发现得了。v1 升级出来的内置名天然满足。
func validateTierNameInto(collector *configErrorCollector, rank Level, id string, name LocalizedName) {
	if name.Zh == "" {
		collector.add(CodeNameMissing, rank, id, "name.zh", map[string]any{"locale": "zh"},
			fmt.Sprintf("档位 %q 缺少中文名称", id))
	} else if utf8.RuneCountInString(name.Zh) > MaxTierNameLength {
		collector.add(CodeNameTooLong, rank, id, "name.zh",
			map[string]any{"locale": "zh", "maxLength": MaxTierNameLength},
			fmt.Sprintf("档位 %q 的 name.zh 超过 %d 个字符", id, MaxTierNameLength))
	}
	if name.En == "" {
		collector.add(CodeNameMissing, rank, id, "name.en", map[string]any{"locale": "en"},
			fmt.Sprintf("档位 %q 缺少英文名称", id))
	} else if utf8.RuneCountInString(name.En) > MaxTierNameLength {
		collector.add(CodeNameTooLong, rank, id, "name.en",
			map[string]any{"locale": "en", "maxLength": MaxTierNameLength},
			fmt.Sprintf("档位 %q 的 name.en 超过 %d 个字符", id, MaxTierNameLength))
	}
}

// validateRequirementsInto 校验阈值非负，按指标逐条报（字段路径可定位到指标）。
func validateRequirementsInto(collector *configErrorCollector, rank Level, id string, requirement Requirements) {
	for _, metric := range Metrics {
		value, ok := requirement.Value(metric)
		if !ok {
			collector.add(CodeRequirementUnknownMetric, rank, id, "requirements."+metric,
				map[string]any{"metric": metric},
				fmt.Sprintf("档位 %q 的 requirements.%s 不是已知指标", id, metric))
			continue
		}
		if value < 0 {
			collector.add(CodeRequirementNegative, rank, id, "requirements."+metric,
				map[string]any{"metric": metric, "value": value},
				fmt.Sprintf("档位 %q 的 requirements.%s 不能为负", id, metric))
		}
	}
}

// validateLimitsInto 校验限额非负，按限额键逐条报。
func validateLimitsInto(collector *configErrorCollector, rank Level, id string, limits LevelLimits) {
	for _, item := range []struct {
		key   string
		value *int
	}{
		{"topicCooldownSeconds", limits.TopicCooldownSeconds},
		{"commentCooldownSeconds", limits.CommentCooldownSeconds},
		{"dailyTopicLimit", limits.DailyTopicLimit},
		{"dailyCommentLimit", limits.DailyCommentLimit},
	} {
		if item.value != nil && *item.value < 0 {
			collector.add(CodeLimitNegative, rank, id, "limits."+item.key,
				map[string]any{"key": item.key, "value": *item.value},
				fmt.Sprintf("档位 %q 的 limits.%s 不能为负", id, item.key))
		}
	}
}

// validateEarnableInto 校验可挣得键是否在白名单内，按档位报。
//
// 判据是**正白名单**（D24），不是「不是管理类就行」：后者挡不住 post.edit_any 这类
// 有特权、无审计的键。
func validateEarnableInto(collector *configErrorCollector, rank Level, id string, keys []string) {
	for _, key := range keys {
		switch {
		case key == "":
			collector.add(CodeEarnableKeyEmpty, rank, id, "earnable", nil,
				fmt.Sprintf("档位 %q 的可挣得权限键为空", id))
		case !IsWhitelisted(key):
			collector.add(CodeEarnableNotWhitelisted, rank, id, "earnable",
				map[string]any{"permission": key},
				fmt.Sprintf("%q 不在可挣得白名单内", key))
		}
	}
}

// validateDeletedTierIDs 校验删除墓碑（deletedTierIds），返回保序校验后的墓碑
// （重复即 422，所以顺序 = 提交顺序，路径上不存在「去重后静默接受」）、
// 它们占用的最大数字后缀（供新档派生 id 避开），以及待追加的字段级明细。
//
// 三条判据都不做静默修正：
//   - 语法/身份不合法（含保留给 TL0 的 newcomer）：墓碑会被写进历史，随手接受一个
//     拼错的 id 等于往配置里种一条永远对不上的记录；
//   - 重复：同一条墓碑出现两次说明前端把「删除」按了两遍，去重会让运营看不出这件事，
//     所以直接 422；
//   - 与 tiers 里仍在的档位冲突：既删又存，到底要哪一份无法猜，必须让运营选。
func validateDeletedTierIDs(ids []string, declared map[string]bool) ([]string, int, []FieldError) {
	if len(ids) == 0 {
		return nil, 0, nil
	}
	normalized := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	maxSeq := 0
	details := make([]FieldError, 0, len(ids))
	record := func(id, code, reason string) {
		details = append(details, FieldError{
			// rank 恒 0：墓碑不是某一档，定位靠 tierId（= 墓碑本身）。
			Rank: 0, TierID: id, Field: "deletedTierIds", Code: code,
			Params: map[string]any{"id": id}, Reason: reason,
		})
	}
	for _, id := range ids {
		switch {
		case id == NewcomerID || !ValidTierID(id):
			record(id, CodeDeletedTierIDInvalid,
				fmt.Sprintf("deletedTierIds 里的 %q 不合法（只允许内置档位 id 或 tier_<数字>；%q 保留给 TL0）", id, NewcomerID))
			continue
		case seen[id]:
			record(id, CodeDeletedTierIDDuplicate,
				fmt.Sprintf("deletedTierIds 里的 %q 重复出现", id))
			continue
		case declared[id]:
			record(id, CodeDeletedTierIDPresent,
				fmt.Sprintf("%q 同时出现在 deletedTierIds 与 tiers 里：要么删掉它，要么取消删除", id))
			continue
		}
		seen[id] = true
		normalized = append(normalized, id)
		if seq, ok := tierSeq(id); ok && seq > maxSeq {
			maxSeq = seq
		}
	}
	return normalized, maxSeq, details
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
