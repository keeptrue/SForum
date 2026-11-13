package entitymeta

import "context"

// Store 持久化字段定义与元数据值。
type Store interface {
	ListDefinitions(ctx context.Context, entityType string) ([]fieldRow, error)
	GetDefinitionByKey(ctx context.Context, fieldKey string) (fieldRow, error)
	CreateDefinition(ctx context.Context, row fieldRow) (fieldRow, error)
	UpdateDefinition(ctx context.Context, fieldKey string, row fieldRow) (fieldRow, error)
	DeleteDefinition(ctx context.Context, fieldKey string) error
	// SetEnabledDefinition 翻转单个字段的启用态（差集清扫用）。
	SetEnabledDefinition(ctx context.Context, fieldKey string, enabled bool) error
	// SetEnabledByOwnerExtension 批量翻转某扩展拥有的字段的启用态。
	// 只改 enabled，不删字段、不删值：插件停用/卸载后用户数据必须留下来。
	SetEnabledByOwnerExtension(ctx context.Context, extensionID string, enabled bool) error

	ListValues(ctx context.Context, entityType string, entityID int64) ([]valueRow, error)
	UpsertValue(ctx context.Context, row valueRow) (valueRow, error)
	DeleteValue(ctx context.Context, entityType string, entityID int64, fieldKey string) error

	// EntityExists 校验目标实体是否存在（user / topic）。
	EntityExists(ctx context.Context, entityType string, entityID int64) (bool, error)
	// TopicAuthorID 返回主题作者；不存在时 ok=false。
	TopicAuthorID(ctx context.Context, topicID int64) (authorID int64, ok bool, err error)
}
