package profile

import (
	"context"
	"log/slog"

	settingssurface "github.com/keeptrue/sforum/apps/api/app/Models/Extensions/settingssurface"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 插件设置面（forum.settings.profile.tabs）与 entity-meta 值的编排。
//
// 这里只做「拼装视图 / 转发写入」，不解析 manifest、也不判断哪个字段键合法：
// 字段目录的唯一来源是 SettingsSectionProvider 的投影（Providers 层实现）。
// 在读模型上再解析一次 manifest 就会出现第二份「哪些键合法」的真相。

// CodeSettingsInvalid 是插件字段写入失败的错误码（配合字段级消息使用）。
const CodeSettingsInvalid = "profile.settings_invalid"

// 字段级消息键。前端把它们贴在被标记的字段旁边，因此每条只回答「这一个字段错在哪」。
const (
	MessageSettingsFieldUnknown = "profile.settings.field_unknown"
	// MessageSettingsFieldUndefined 表示「字段键合法，但 entity-meta 里没有这条定义」。
	// 它几乎总是同步/投影没跑（能看不能存），单独成句让运维一眼定位。
	MessageSettingsFieldUndefined = "profile.settings.field_undefined"
	MessageSettingsFieldForbidden = "profile.settings.field_forbidden"
	MessageSettingsFieldInvalid   = "profile.settings.field_invalid"
	MessageSettingsFieldRequired  = "profile.settings.field_required"
)

// SettingsSectionProvider 解析个人设置页的插件 tab。
// viewer 是请求者视角：provider 已按权限与站点治理过滤，并已把字段键换算成
// entity-meta 组合键（x.<extension>.<key>）。
type SettingsSectionProvider interface {
	ProfileSections(ctx context.Context, viewer identity.Actor) ([]settingssurface.Section, error)
}

// SettingsMetaValue 是公开资料页上的一个插件字段值。
type SettingsMetaValue struct {
	Label map[string]string `json:"label,omitempty"`
	Value any               `json:"value"`
}

// SettingsMetaReader 读插件字段值。两个入口共用同一个过滤实现（见 Providers
// 的 SettingsSurfaceMetaStore）：自助面拿 public+owner，公开面只拿 public。
type SettingsMetaReader interface {
	// MySettingsValues 返回当前用户可编辑字段的值，键是组合 fieldKey。
	MySettingsValues(ctx context.Context, actor identity.Actor) (map[string]any, error)
	// PublicSettingsMeta 返回某用户对 viewer 可见的公开字段值，键是组合 fieldKey。
	PublicSettingsMeta(ctx context.Context, viewer identity.Actor, userID int64) (map[string]SettingsMetaValue, error)
}

// SettingsMetaWriter 写当前用户的插件字段值。
//
// 实现必须先按「字段目录」校验每个键（未声明 / 被站点停用 / 无写权限都算非法），
// 再把值交给 entity-meta。未声明的键绝不能被静默丢弃：用户以为改了、实际没改，
// 比报错更难查。
type SettingsMetaWriter interface {
	WriteSettingsValues(ctx context.Context, actor identity.Actor, values map[string]any) error
}

// 核心资料字段的字段级消息键（键名用前端字段名：websiteUrl / bio / ...）。
const (
	CodeProfileFieldsInvalid = "profile.invalid"
	MessageFieldTooLong      = "profile.field.too_long"
	MessageFieldInvalidURL   = "profile.field.invalid_url"
	MessageFieldInvalidID    = "profile.field.invalid_attachment"
)

// ProfileFieldsError 携带核心资料字段的字段级错误。
//
// 与 NicknameInvalidError 同一模式：domain 决定哪个字段错了、错在哪种规则，
// 控制器只负责把它翻译成 422 + data.fields。一次提交里的所有非法字段必须
// 一起返回——前端按 tab 汇总「还有 N 处待修正」，只报第一条就汇总不出来。
type ProfileFieldsError struct {
	Fields map[string][]string
}

func (e *ProfileFieldsError) Error() string {
	return "profile: profile fields are invalid"
}

// Is 让字段级错误仍然满足 errors.Is(err, ErrProfileInvalid)：错误分类没变，
// 只是多了字段明细。既有调用方（含测试）不必改写判断。
func (e *ProfileFieldsError) Is(target error) bool {
	return target == ErrProfileInvalid
}

// SettingsFieldError 携带字段级错误，键是组合 fieldKey（x.<extension>.<key>）。
//
// 用组合键而不是裸键，是因为前端把「字段键 = 值键 = 错误键」当同一把键：
// 错误直接贴到对应输入框，不需要再做一次命名空间拼接。
type SettingsFieldError struct {
	Fields map[string][]string
}

func (e *SettingsFieldError) Error() string {
	return "profile: settings fields are invalid"
}

func newSettingsFieldError(fields map[string][]string) *SettingsFieldError {
	return &SettingsFieldError{Fields: fields}
}

// WithSettingsSections 注入个人设置页的插件 tab 来源（链式可选）。
// 未注入时 GET /profile 不带 settingsSections，与本次改造之前逐字节等价。
func (s *Service) WithSettingsSections(provider SettingsSectionProvider) *Service {
	if s != nil {
		s.settingsSections = provider
	}
	return s
}

// WithSettingsMeta 注入插件字段值的读写（链式可选）。读与写分开注入是刻意的：
// 只读面（公开资料页）与自助写入是两条权限路径，任何一个没接上都要能单独发现。
func (s *Service) WithSettingsMeta(reader SettingsMetaReader, writer SettingsMetaWriter) *Service {
	if s != nil {
		s.settingsMeta = reader
		s.settingsMetaWriter = writer
	}
	return s
}

// attachSelfSettings 给自助资料视图补上插件 tab 与当前值。
//
// 插件设置面读失败只降级为「没有插件 tab」，不让用户连自己的资料设置都打不开；
// 但仍然留 warn 日志，避免静默消失。
func (s *Service) attachSelfSettings(ctx context.Context, actor identity.Actor, view *PublicProfile) {
	if s == nil || view == nil || s.settingsSections == nil {
		return
	}
	sections, err := s.settingsSections.ProfileSections(ctx, actor)
	if err != nil {
		slog.Warn("profile: load settings sections failed", "userId", actor.ID, "error", err)
		return
	}
	if len(sections) == 0 {
		return
	}
	view.SettingsSections = sections
	// settingsValues 在有 tab 时**恒下发**：空对象表示「还没填」，
	// 整键消失会让前端无法区分「没有插件字段」与「有字段但一个值都没存」。
	empty := map[string]any{}
	view.SettingsValues = &empty
	if s.settingsMeta == nil {
		return
	}
	values, err := s.settingsMeta.MySettingsValues(ctx, actor)
	if err != nil {
		slog.Warn("profile: load settings values failed", "userId", actor.ID, "error", err)
		return
	}
	if filtered := filterSettingsValues(sections, values); filtered != nil {
		view.SettingsValues = &filtered
	}
}

// attachPublicSettingsMeta 给公开资料页补上可见的插件字段值。
// 过滤由 provider 完成（只放 visibility=public 且 viewer 有权的字段），这里只负责挂载。
func (s *Service) attachPublicSettingsMeta(ctx context.Context, viewer identity.Actor, userID int64, view *PublicProfile) {
	if s == nil || view == nil || s.settingsMeta == nil {
		return
	}
	meta, err := s.settingsMeta.PublicSettingsMeta(ctx, viewer, userID)
	if err != nil {
		slog.Warn("profile: load public settings meta failed", "userId", userID, "error", err)
		return
	}
	if len(meta) == 0 {
		return
	}
	view.Meta = meta
}

// writeSettingsMeta 在资料字段写成功之后写插件字段。
//
// 顺序：先 profile 再 meta。两者没有共同不变量（资料字段与插件字段互不约束），
// 因此不引入跨域事务；代价是极端情况下前者已落、后者整体 422——重试即可收敛，
// 反过来的顺序会让「昵称被拒但简介已经存了」这种更糟的结果出现在主路径上。
//
// 返回 *SettingsFieldError 时调用方应把整次请求判为 422 并把字段错误贴回前端；
// 前端保留 dirty 状态并跳到出错的 tab，因此这里不需要回滚已写入的字段。
func (s *Service) writeSettingsMeta(ctx context.Context, actor identity.Actor, values map[string]any) error {
	if len(values) == 0 {
		return nil
	}
	if s.settingsMetaWriter == nil {
		// 未装配 = 服务没准备好。拒绝而不是假装成功：静默丢弃用户输入是最坏的失败方式。
		return ErrProfileInvalid
	}
	return s.settingsMetaWriter.WriteSettingsValues(ctx, actor, values)
}

// filterSettingsValues 只保留当前 tab 声明里出现过的键。
//
// entity-meta 里可能还有运营手建字段或别的插件的字段，它们不属于这次渲染，
// 进 settingsValues 只会让前端拿不到对应的输入框。
func filterSettingsValues(sections []settingssurface.Section, values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, 16)
	for _, section := range sections {
		for _, field := range section.Fields {
			allowed[field.FieldKey] = struct{}{}
		}
	}
	filtered := make(map[string]any, len(allowed))
	for key, value := range values {
		if _, ok := allowed[key]; !ok {
			continue
		}
		filtered[key] = value
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

// GetMyProfileForActor 读取当前用户可编辑的资料，并按请求者权限组装插件设置 tab。
//
// 与 GetMyProfile 的区别只有 viewer：插件 tab 的 permission 过滤需要角色/权限，
// 只传 userID 的旧签名会把「有权看到的 tab」也滤掉。
func (s *Service) GetMyProfileForActor(ctx context.Context, actor identity.Actor) (PublicProfile, error) {
	if actor.ID <= 0 {
		return PublicProfile{}, identity.ErrPermissionDenied
	}
	user, err := s.store.GetUserSummaryByID(ctx, actor.ID)
	if err != nil {
		return PublicProfile{}, err
	}
	profile, err := s.store.GetProfile(ctx, actor.ID)
	if err != nil {
		return PublicProfile{}, err
	}
	view := s.myProfileView(ctx, user, profile)
	s.attachSelfSettings(ctx, actor, &view)
	return view, nil
}
