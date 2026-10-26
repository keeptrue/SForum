// Package extensionreference 让扩展复用 Core 的附件体系。
//
// 插件唯一缺的能力是「把某个已上传的附件登记成扩展拥有的公开资产」，也就是往
// attachment_references 里写一行并维持 reference_count。本包提供那条路径的
// 全部领域语义（claim / release / list），宿主通过 Host Command 暴露给插件。
//
// 存储约定：
//
//	resource_type = 'extension'，resource_id 固定 0，
//	context       = "<extensionId>:<pluginContext>"
//
// extensionId 取自 Host 证实的运行时身份，绝不取插件输入；pluginContext 由插件
// 自己定义，只在本扩展命名空间内唯一。attachment_references 上的
// (resource_type, resource_id, context) 全表唯一索引保证同一 context 全局至多
// 一行，所以「换图」必须删旧插新。
package extensionreference

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

const (
	// ResourceType 是扩展引用在 attachment_references 中的固定 resource_type。
	ResourceType = "extension"

	// MaxImageBytes 是单张扩展公开图片的硬上限（512 KiB）。
	// 公开图片会以匿名可读的内联响应返回，因此这里比普通上传更严格。
	MaxImageBytes int64 = 512 * 1024
	// DefaultLimit 是单扩展存活引用数的默认上限。
	DefaultLimit = 500
	// LimitMax 是运营可配置上限的最大值。
	LimitMax = 10000
	// ContextMaxRunes 是插件侧 context 的最大长度。
	ContextMaxRunes = 64
	// ExtensionIDMaxRunes 与 manifest 扩展 id 长度上限一致。
	ExtensionIDMaxRunes = 80
	// maxListPerPage 与后台附件列表一致，避免深分页与超大响应。
	maxListPerPage = 100
)

var (
	// ErrNotFound 表示附件不存在或已不是 active。
	ErrNotFound = errors.New("extensionreference: attachment not found")
	// ErrForbidden 表示调用者无权把该附件登记为扩展公开资产。
	ErrForbidden = errors.New("extensionreference: forbidden")
	// ErrInvalid 表示 context/MIME 等参数不满足契约。
	ErrInvalid = errors.New("extensionreference: invalid reference")
	// ErrTooLarge 表示图片超过站点上限与 512 KiB 的较小值。
	ErrTooLarge = errors.New("extensionreference: image too large")
	// ErrLimitReached 表示该扩展的存活引用数已达上限。
	ErrLimitReached = errors.New("extensionreference: reference limit reached")
	// ErrConflict 表示唯一索引冲突，属锁失效的异常路径。
	ErrConflict = errors.New("extensionreference: reference conflict")
	// ErrRevisionConflict 表示附件修订号已变化（并发提交）。
	ErrRevisionConflict = errors.New("extensionreference: revision conflict")
)

// contentTypes 是允许作为扩展公开图片的 MIME 白名单。
// SVG/HTML 等主动内容一旦可匿名内联读取就是同源 XSS 面，永不纳入。
var contentTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
}

// contextPattern 是插件侧 context 的字符集。
//
// 允许 ':' 是因为插件普遍用 "resource:identifier" 形态（例如
// oauth2-client:sfo_client_xxx）。它不引入歧义：存储键是
// "<extensionId>:<context>"，扩展 id 本身不含 ':'，而所有命名空间判定都是对
// 已知前缀 "extID:" 的等值比较，从不按键内容切分。
var contextPattern = regexp.MustCompile(`^[a-z0-9._:-]{1,64}$`)

// IsContentType 报告 MIME 是否允许作为扩展公开图片。
func IsContentType(contentType string) bool {
	return contentTypes[strings.ToLower(strings.TrimSpace(contentType))]
}

// URLPath 是扩展公开资产的稳定媒体地址。
//
// 与宿主域名无关，也不复用存储 PublicURL：插件业务数据里存下来的地址在换域名
// 或换存储后端时不能烂掉，管理列表与附件 API 也不该暴露对象存储直链。
func URLPath(publicID string) string {
	return "/media/attachments/" + publicID
}

// Policy 是扩展认领附件时的有效约束。
type Policy struct {
	// MaxBytes 是站点上传上限与 MaxImageBytes 的较小值。
	MaxBytes int64
	// ReferenceLimit 是单扩展存活引用数上限。
	ReferenceLimit int
}

// PolicyFromValues 从 web_options 值解析扩展附件约束。
func PolicyFromValues(values map[string]string) Policy {
	limit, err := strconv.Atoi(strings.TrimSpace(values[options.NameAttachmentExtensionReferenceLimit]))
	if err != nil || limit < 1 {
		limit = DefaultLimit
	}
	if limit > LimitMax {
		limit = LimitMax
	}
	maxBytes := int64(0)
	if parsed, err := strconv.Atoi(strings.TrimSpace(values[options.NameAttachmentMaxFileSizeMB])); err == nil {
		maxBytes = int64(parsed) * 1024 * 1024
	}
	if maxBytes <= 0 || maxBytes > MaxImageBytes {
		maxBytes = MaxImageBytes
	}
	return Policy{MaxBytes: maxBytes, ReferenceLimit: limit}
}

// ClaimInput 是一次「认领」的完整输入。
type ClaimInput struct {
	ExtensionID string
	Context     string
	PublicID    string
	ActorUserID int64
	// ManageAny 为真表示 actor 持 attachment.manage，可认领任意附件。
	ManageAny bool
	// ExpectedRevision 必须等于附件当前 updated_at，否则按并发冲突拒绝。
	ExpectedRevision time.Time
	Policy           Policy
}

// ClaimPlan 是 Prepare 阶段产出的不透明认领计划，
// 只在同一事务的 Apply 阶段回传；Host 不解释其内容。
type ClaimPlan struct {
	ExtensionID     string
	Scope           string
	AttachmentID    int64
	PublicID        string
	ContentType     string
	SHA256          string
	SizeBytes       int64
	Width           int
	Height          int
	Visibility      string
	OldAttachmentID int64
	ActorUserID     int64
	// ReferenceCount / UpdatedAt 是 Prepare 读到的当前状态，供 Plan 阶段投影。
	ReferenceCount int
	UpdatedAt      time.Time
}

// Claim 是认领完成后的附件公开快照。
type Claim struct {
	AttachmentID   int64
	PublicID       string
	URL            string
	ContentType    string
	Width          int
	Height         int
	SizeBytes      int64
	SHA256         string
	ReferenceCount int
	UpdatedAt      time.Time
}

// ReleaseInput 按 context 或 publicId 释放本扩展的引用。
type ReleaseInput struct {
	ExtensionID string
	// Context 与 PublicID 必须且只能提供一个。
	Context  string
	PublicID string
}

// Release 是释放结果；无匹配行时 Released 为 0 且不报错。
type Release struct {
	Released int
	Revision time.Time
}

// ListInput 是只读对账分页输入。
type ListInput struct {
	ExtensionID string
	Page        int
	PerPage     int
}

// List 是本扩展全部存活引用的分页结果。
type List struct {
	Items   []Item
	Total   int64
	Page    int
	PerPage int
}

// Item 是单个扩展引用及其附件元数据。
type Item struct {
	Context     string
	PublicID    string
	URL         string
	ContentType string
	Width       int
	Height      int
	SizeBytes   int64
	SHA256      string
	CreatedAt   time.Time
}

// NormalizeExtensionID 校验并归一化扩展 id。
// 字符集与 manifest 扩展 id 对齐（小写字母数字与 ._-）。
func NormalizeExtensionID(extensionID string) (string, error) {
	value := strings.TrimSpace(extensionID)
	if value == "" || len(value) > ExtensionIDMaxRunes {
		return "", ErrInvalid
	}
	for index, char := range value {
		ok := (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-'
		if !ok {
			return "", ErrInvalid
		}
		if index == 0 && (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return "", ErrInvalid
		}
	}
	return value, nil
}

// Scope 返回存储键 "<extensionId>:<context>"。
func Scope(extensionID string, context string) (string, string, error) {
	id, err := NormalizeExtensionID(extensionID)
	if err != nil {
		return "", "", err
	}
	normalized := strings.TrimSpace(context)
	if !contextPattern.MatchString(normalized) {
		return "", "", ErrInvalid
	}
	return id, id + ":" + normalized, nil
}

// lockKey 是扩展级 advisory 锁的键。
//
// 命令引擎自带的 advisory 键含 IdempotencyKey，只串行化同一请求的重试，挡不住
// 「同 context、不同附件、不同 Idempotency-Key」的并发提交：两个事务会在唯一
// 索引上互等，ON CONFLICT DO NOTHING 吞掉插入后仍盲目 +1，产生
// reference_count > 0 却无引用行的幽灵附件（永不回收）。以扩展 id 为粒度加锁
// 才能让「读旧引用 → 删旧 → 插新 → 重算计数」成为一个串行化临界区。
func lockKey(extensionID string) string {
	return "sforum:attachment-extension@1:" + extensionID
}

// 扩展命名空间的前缀判定一律写成
//
//	LEFT(context, length($n) + 1) = $n || ':'
//
// 而不是 LIKE：扩展 id 合法字符集包含 '_'（以及未来可能出现的 '%'），
// LIKE 会把它们当通配符，误切到其他扩展的引用。
