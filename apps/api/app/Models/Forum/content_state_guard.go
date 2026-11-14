package forum

import (
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 本文件集中「内容还能不能被编辑/删除」的判定：生命周期状态谓词、编辑/删除
// 权限谓词，以及乐观并发令牌的匹配规则。编辑入口是公开可达的，状态判定必须
// 映射成 NotFound 而不是 PermissionDenied——后者会让没有查看权限的调用方确认
// 该 ID 仍然存在。
//
// 主题与评论共用同一组状态字面量（hidden/deleted），因此共用一份集合；
// content_state_guard_test.go 里有断言锁住这层耦合。

var unavailableContentStatuses = map[string]struct{}{
	TopicStatusHidden:  {},
	TopicStatusDeleted: {},
}

// topicEditStateError 判定主题是否处于不可编辑状态。
func topicEditStateError(topic TopicSummary) error {
	if _, unavailable := unavailableContentStatuses[topic.Status]; unavailable {
		return ErrTopicNotFound
	}
	return nil
}

// commentEditStateError 判定评论是否处于不可编辑状态。
func commentEditStateError(comment CommentSummary) error {
	if _, unavailable := unavailableContentStatuses[comment.Status]; unavailable {
		return ErrCommentNotFound
	}
	return nil
}

// assertTopicEditable 是编辑管道的入口状态谓词。
func assertTopicEditable(topic TopicSummary, operation string) error {
	// 版本恢复是独立的版主操作（RestoreTopic 已单独校验 edit_any 与历史查看权），
	// 复用编辑管道时放行，否则「恢复历史版本」会被误判成内容复活。
	if operation == RevisionOperationRestore {
		return nil
	}
	return topicEditStateError(topic)
}

// assertCommentEditable 是评论编辑管道的入口状态谓词。
func assertCommentEditable(comment CommentSummary, operation string) error {
	// RestoreComment 与主题恢复同理：恢复历史版本不是「复活被隐藏的内容」。
	if operation == RevisionOperationRestore {
		return nil
	}
	return commentEditStateError(comment)
}

// requeueGuardSQL 给「编辑触发预审」的 UPDATE 追加状态谓词，保证
// deleted/hidden 内容不会被洗回 pending。谓词与调用方的行锁互为冗余：
// 锁一旦放宽，这里仍然拒绝复活终态内容。
const requeueGuardSQL = ` AND status NOT IN ('hidden', 'deleted')`

func canEditComment(actor identity.Actor, comment CommentSummary) bool {
	if actor.Can(identity.PermissionPostEditAny) {
		return true
	}
	return comment.AuthorUserID == actor.ID && actor.Can(identity.PermissionPostEditOwn)
}

func canDeleteComment(actor identity.Actor, comment CommentSummary) bool {
	if actor.Can(identity.PermissionPostDeleteAny) {
		return true
	}
	return comment.AuthorUserID == actor.ID && actor.Can(identity.PermissionPostDeleteOwn)
}

// canEditTopic: 作者凭 topic.edit_own；版主凭 topic.edit_any。
func canEditTopic(actor identity.Actor, topic TopicSummary) bool {
	if actor.Can(identity.PermissionTopicEditAny) {
		return true
	}
	return topic.AuthorUserID == actor.ID && actor.Can(identity.PermissionTopicEditOwn)
}

// canDeleteTopic: 作者凭 topic.delete_own；版主凭 topic.delete_any。
// 站点策略 AllowAuthorDelete=false 时作者删除被禁止（版主仍可删）。
func canDeleteTopic(actor identity.Actor, topic TopicSummary) bool {
	if actor.Can(identity.PermissionTopicDeleteAny) {
		return true
	}
	return topic.AuthorUserID == actor.ID && actor.Can(identity.PermissionTopicDeleteOwn)
}

func matchesExpectedRevision(expected, current int64) bool {
	if expected <= 0 {
		return false
	}
	// current_revision=0 只会出现在 M1 回填未完成的旧行；公开读模型将它
	// 表示为 version 1，写事务仍会在锁内以真实值二次确认。
	if current <= 0 {
		current = 1
	}
	return expected == current
}
