package forum

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	avatar "github.com/keeptrue/sforum/apps/api/app/Support/Avatar"
)

type PostgresStore struct {
	pool          *pgxpool.Pool
	avatarBuilder *avatar.ViewBuilder
	notifications NotificationWriter
	auditor       audit.TxWriter
}

type CommentNotificationInput struct {
	CommentID, TopicID, ActorUserID, TopicAuthorUserID, ParentAuthorUserID int64
	MentionedUsernames                                                     []string
}
type TopicNotificationInput struct {
	TopicID, ActorUserID int64
	MentionedUsernames   []string
}
type PendingReviewNotificationInput struct {
	TargetType, Title               string
	TargetID, TopicID, AuthorUserID int64
	Revision                        int64
}

type NotificationWriter interface {
	NotifyCommentTx(context.Context, pgx.Tx, CommentNotificationInput) error
	NotifyTopicTx(context.Context, pgx.Tx, TopicNotificationInput) error
	NotifyPendingReviewTx(context.Context, pgx.Tx, PendingReviewNotificationInput) error
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return NewPostgresStoreWithAvatar(pool, nil)
}

func (s *PostgresStore) WithCommentNotifications(writer NotificationWriter) *PostgresStore {
	s.notifications = writer
	return s
}

// WithAuditor wires the shared transaction-aware audit boundary. The no-op
// default keeps local tools/read-only stores usable; production injects it.
func (s *PostgresStore) WithAuditor(writer audit.TxWriter) *PostgresStore {
	if writer == nil {
		writer = audit.NoopWriter{}
	}
	s.auditor = writer
	return s
}

func NewPostgresStoreWithAvatar(pool *pgxpool.Pool, avatarOptions avatar.OptionResolver) *PostgresStore {
	return &PostgresStore{pool: pool, avatarBuilder: avatar.NewViewBuilder(avatarOptions)}
}
