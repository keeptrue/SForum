package events

const (
	ExtensionEnabled  = "extension.enabled"
	ExtensionDisabled = "extension.disabled"
	// UserBeforeRegister E1.3：注册落库前同步 validate，仅可拒绝不可补丁（v1）。
	UserBeforeRegister = "user.before_register"
	UserRegistered     = "user.registered"
	TopicBeforeCreate  = "topic.before_create"
	// TopicBeforeUpdate E1.2：主题编辑提交前同步 filter，可拒绝或补丁 allowlist 字段。
	TopicBeforeUpdate = "topic.before_update"
	TopicCreated      = "topic.created"
	TopicUpdated      = "topic.updated"
	TopicDeleted      = "topic.deleted"
	TopicHidden       = "topic.hidden"
	TopicRestored     = "topic.restored"
	TopicLocked       = "topic.locked"
	TopicUnlocked     = "topic.unlocked"
	TopicPinned       = "topic.pinned"
	TopicUnpinned     = "topic.unpinned"
	// CommentBeforeCreate E1.1：评论提交前同步 filter，可拒绝或补丁 content。
	CommentBeforeCreate = "comment.before_create"
	CommentCreated      = "comment.created"
	CommentBeforeUpdate = "comment.before_update"
	CommentUpdated      = "comment.updated"
	CategoryCreated     = "category.created"
	CategoryUpdated     = "category.updated"
	CategoryDeleted     = "category.deleted"
	TagCreated          = "tag.created"
	TagUpdated          = "tag.updated"
	// AttachmentBeforeUpload E1.4：存储写入前同步 validate；仅元数据，无文件字节。
	AttachmentBeforeUpload = "attachment.before_upload"
	AttachmentUploaded     = "attachment.uploaded"
	// EntityMetaUpdated F4.4：实体自定义字段值变更后发出（observe，不阻断写路径）。
	EntityMetaUpdated = "entity_meta.updated"
	// ReactionCreated / ReactionRemoved：点赞关系变更后发出（observe）。
	// 这是徽章类插件将来唯一需要的数据源，payload 只含公开安全字段。
	ReactionCreated = "reaction.created"
	ReactionRemoved = "reaction.removed"
	// BookmarkCreated / BookmarkRemoved：收藏关系变更后发出（observe）。
	// 与 reaction 同姿态：payload 只含公开安全字段，私密收藏不外泄内容摘要。
	BookmarkCreated = "bookmark.created"
	BookmarkRemoved = "bookmark.removed"
)

// 目录约定（F1.3）：
//   - observe：异步投递，TimeoutMS 为 worker 内单次 invoke 上限；失败不阻断业务写路径。
//   - filter/validate：同步调用，TimeoutMS 由 host context 强制；FailurePolicy 默认 fail_closed。
//   - 禁止在 filter 内做重 I/O、批量索引、发信；应 enqueue job。
var definitions = []Definition{
	observe(ExtensionEnabled, "Emitted after a plugin is enabled and its runtime starts.", []string{"extensionId"}),
	observe(ExtensionDisabled, "Emitted after a plugin is disabled and its runtime stops.", []string{"extensionId"}),
	validate(UserBeforeRegister,
		"Runs before a user row is committed and may reject registration. Payload never includes password. v1 is reject-only (no patch) so uniqueness/policy stay host-owned.",
		[]string{"username", "email", "locale"},
	),
	observe(UserRegistered, "Emitted after a new user is committed.", []string{"userId", "username", "email", "locale"}),
	filter(TopicBeforeCreate,
		"Runs before a topic is committed and may reject or patch allowlisted input. Heavy work must enqueue jobs, never block this filter.",
		[]string{"actorUserId", "categorySlug", "tagSlugs", "title", "content"},
		[]string{"categorySlug", "tagSlugs", "title", "content"},
	),
	filter(TopicBeforeUpdate,
		"Runs before a topic update is committed and may reject or patch allowlisted input. Heavy work must enqueue jobs, never block this filter.",
		[]string{"actorUserId", "topicId", "categorySlug", "tagSlugs", "title", "content"},
		[]string{"categorySlug", "tagSlugs", "title", "content"},
	),
	observe(TopicCreated, "Emitted after a topic is committed with a public-safe topic, author, taxonomy, and URL snapshot.", topicObservePayload()),
	observe(TopicUpdated, "Emitted after a topic's content or taxonomy is updated. Includes the current public-safe snapshot; revision metadata never includes raw content or reason.", topicObservePayload("actorUserId", "revisionNo", "operation", "changedFields", "restoredFromRevisionNo")),
	observe(TopicDeleted, "Emitted after a topic is soft-deleted with its last public-safe snapshot.", topicObservePayload("actorUserId")),
	observe(TopicHidden, "Emitted after a topic is hidden by a moderator with its current public-safe snapshot.", topicObservePayload("actorUserId")),
	observe(TopicRestored, "Emitted after a hidden or deleted topic is restored to active with its current public-safe snapshot.", topicObservePayload("actorUserId")),
	observe(TopicLocked, "Emitted after a topic is locked with its current public-safe snapshot.", topicObservePayload("actorUserId")),
	observe(TopicUnlocked, "Emitted after a topic is unlocked with its current public-safe snapshot.", topicObservePayload("actorUserId")),
	observe(TopicPinned, "Emitted after a topic is pinned with its current public-safe snapshot.", topicObservePayload("actorUserId")),
	observe(TopicUnpinned, "Emitted after a topic is unpinned with its current public-safe snapshot.", topicObservePayload("actorUserId")),
	observe(CategoryCreated, "Emitted after a category is created.", []string{"categoryId", "categorySlug", "groupId"}),
	observe(CategoryUpdated, "Emitted after a category is updated.", []string{"categoryId", "categorySlug", "groupId"}),
	observe(CategoryDeleted, "Emitted after a category or an empty category group is deleted. Groups report categoryGroupId with the group slug in categorySlug.", []string{"categoryId", "categoryGroupId", "categorySlug", "groupId"}),
	observe(TagCreated, "Emitted after a tag is created.", []string{"tagId", "tagSlug", "status"}),
	observe(TagUpdated, "Emitted after a tag is updated.", []string{"tagId", "tagSlug", "status"}),
	filter(CommentBeforeCreate,
		"Runs before a comment is committed and may reject or patch allowlisted input. Heavy work must enqueue jobs, never block this filter.",
		[]string{"actorUserId", "topicId", "parentId", "content"},
		// v1 仅允许补丁正文；parentId 改树需 host 重验，暂不开放。
		[]string{"content"},
	),
	observe(CommentCreated, "Emitted after a comment is committed.", []string{"commentId", "topicId", "authorUserId", "parentId"}),
	filter(CommentBeforeUpdate,
		"Runs after edit authority/CAS checks and before a comment update is committed. It may patch content only.",
		[]string{"actorUserId", "commentId", "topicId", "content"},
		[]string{"content"},
	),
	observe(CommentUpdated, "Emitted after a comment update is committed. Revision metadata never includes raw content or reason.", []string{"commentId", "topicId", "actorUserId", "revisionNo", "operation", "changedFields", "restoredFromRevisionNo"}),
	validate(AttachmentBeforeUpload,
		"Runs after host MIME/size policy and before storage write. Reject-only in v1; payload is metadata only (no raw file bytes).",
		[]string{"actorUserId", "contentType", "sizeBytes", "filename"},
	),
	observe(AttachmentUploaded, "Emitted after attachment metadata is committed.", []string{"attachmentId", "publicId", "ownerUserId", "provider", "contentType", "sizeBytes"}),
	observe(EntityMetaUpdated, "Emitted after entity custom field values are written or cleared.", []string{"entityType", "entityId", "fieldKeys", "actorUserId"}),
	observe(ReactionCreated, "Emitted after a like is committed on a visible topic or comment.", []string{"targetType", "targetId", "reaction", "actorUserId"}),
	observe(ReactionRemoved, "Emitted after a like is removed from a visible topic or comment.", []string{"targetType", "targetId", "reaction", "actorUserId"}),
	observe(BookmarkCreated, "Emitted after a bookmark is committed on a visible topic or comment.", []string{"targetType", "targetId", "visibility", "actorUserId"}),
	observe(BookmarkRemoved, "Emitted after a bookmark is removed from a visible topic or comment.", []string{"targetType", "targetId", "visibility", "actorUserId"}),
}

func topicObservePayload(extra ...string) []string {
	payload := []string{"topicId", "authorUserId", "categorySlug", "tagSlugs", "title", "path", "url", "topic", "author", "category", "tags"}
	return append(payload, extra...)
}

func observe(name, description string, payload []string) Definition {
	return Definition{
		Name:          name,
		Kind:          KindObserve,
		Description:   description,
		PayloadFields: payload,
		TimeoutMS:     DefaultAsyncTimeoutMS,
		// observe 不阻塞写路径；字段保留便于目录 UI 统一展示。
		FailurePolicy: FailurePolicyFailOpen,
	}
}

func filter(name, description string, payload, patch []string) Definition {
	return Definition{
		Name:          name,
		Kind:          KindFilter,
		Description:   description,
		PayloadFields: payload,
		PatchFields:   patch,
		TimeoutMS:     DefaultSyncTimeoutMS,
		FailurePolicy: FailurePolicyFailClosed,
	}
}

// validate 注册同步校验事件：fail_closed，无 patch 白名单（插件只能拒绝）。
func validate(name, description string, payload []string) Definition {
	return Definition{
		Name:          name,
		Kind:          KindValidate,
		Description:   description,
		PayloadFields: payload,
		TimeoutMS:     DefaultSyncTimeoutMS,
		FailurePolicy: FailurePolicyFailClosed,
	}
}

func Definitions() []Definition {
	items := make([]Definition, len(definitions))
	copy(items, definitions)
	for index := range items {
		items[index] = normalizeDefinition(items[index])
	}
	return items
}

func FindDefinition(name string) (Definition, bool) {
	for _, definition := range definitions {
		if definition.Name == name {
			return normalizeDefinition(definition), true
		}
	}
	return Definition{}, false
}

func normalizeDefinition(definition Definition) Definition {
	definition.PayloadFields = append([]string{}, definition.PayloadFields...)
	definition.PatchFields = append([]string{}, definition.PatchFields...)
	if definition.TimeoutMS <= 0 {
		if definition.Kind == KindFilter || definition.Kind == KindValidate {
			definition.TimeoutMS = DefaultSyncTimeoutMS
		} else {
			definition.TimeoutMS = DefaultAsyncTimeoutMS
		}
	}
	if definition.FailurePolicy == "" {
		if definition.Kind == KindFilter || definition.Kind == KindValidate {
			definition.FailurePolicy = FailurePolicyFailClosed
		} else {
			definition.FailurePolicy = FailurePolicyFailOpen
		}
	}
	return definition
}

func Known(name string) bool {
	_, ok := FindDefinition(name)
	return ok
}
