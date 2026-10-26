package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	avatar "github.com/keeptrue/sforum/apps/api/app/Support/Avatar"
	"github.com/keeptrue/sforum/apps/api/app/Support/Outbox"
)

type queryRunner interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type PostgresStore struct {
	runner        queryRunner
	pool          *pgxpool.Pool
	wakes         *RevisionHub
	avatarBuilder *avatar.ViewBuilder
	// pushDeviceCipher / pushDeviceNow 服务原生推送设备注册（见 devices.go）。
	pushDeviceCipher PushDeviceCipher
	pushDeviceNow    func() time.Time
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return NewPostgresStoreWithAvatar(pool, nil)
}

func NewPostgresStoreWithAvatar(pool *pgxpool.Pool, avatarOptions avatar.OptionResolver) *PostgresStore {
	return &PostgresStore{runner: pool, pool: pool, avatarBuilder: avatar.NewViewBuilder(avatarOptions)}
}

func newPostgresStore(runner queryRunner) *PostgresStore {
	return &PostgresStore{runner: runner, avatarBuilder: avatar.NewViewBuilder(nil)}
}

// WithRevisionWakeups starts the process-wide LISTEN connection used by SSE.
// Durable revisions remain authoritative when this hint connection is down.
func (s *PostgresStore) WithRevisionWakeups(ctx context.Context) *PostgresStore {
	if s != nil && s.pool != nil && s.wakes == nil {
		s.wakes = NewRevisionHub(ctx, s.pool)
	}
	return s
}

// Close stops process-owned background listeners. The caller still owns pool.
func (s *PostgresStore) Close() {
	if s != nil && s.wakes != nil {
		s.wakes.Close()
	}
}

func (s *PostgresStore) CreateBundleTx(ctx context.Context, tx queryRunner, input CreateBundleInput) (Bundle, error) {
	notification, err := s.CreateNotificationTx(ctx, tx, input.Notification)
	if err != nil {
		return Bundle{}, err
	}
	result := Bundle{Notification: notification}
	templateData := input.Delivery.TemplateData
	if len(templateData) == 0 {
		templateData = json.RawMessage(`{}`)
	}
	err = tx.QueryRow(ctx, `
INSERT INTO mail_deliveries (recipient, template_key, template_data, idempotency_key, correlation_id)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (idempotency_key) DO UPDATE SET idempotency_key = EXCLUDED.idempotency_key
RETURNING id, recipient, template_key, template_data, idempotency_key, correlation_id, status, extension_id,
attempt_count, reason, error_summary, created_at, updated_at, completed_at`, input.Delivery.Recipient,
		input.Delivery.TemplateKey, templateData, input.Delivery.IdempotencyKey, input.Delivery.CorrelationID,
	).Scan(&result.Delivery.ID, &result.Delivery.Recipient, &result.Delivery.TemplateKey, &result.Delivery.TemplateData,
		&result.Delivery.IdempotencyKey, &result.Delivery.CorrelationID, &result.Delivery.Status,
		&result.Delivery.ExtensionID, &result.Delivery.AttemptCount, &result.Delivery.Reason,
		&result.Delivery.ErrorSummary, &result.Delivery.CreatedAt, &result.Delivery.UpdatedAt, &result.Delivery.CompletedAt)
	return result, err
}

func (s *PostgresStore) CreateNotificationTx(ctx context.Context, tx queryRunner, input CreateInput) (Notification, error) {
	payload := input.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	category := input.Category
	if category == "" {
		category = categoryForType(input.Type)
	}
	typeVersion, payloadVersion := input.TypeVersion, input.PayloadVersion
	if typeVersion <= 0 {
		typeVersion = 1
	}
	if payloadVersion <= 0 {
		payloadVersion = 1
	}
	var result Notification
	var created bool
	err := tx.QueryRow(ctx, `
WITH inserted AS (
  INSERT INTO notifications (recipient_user_id, type, category, type_version, payload_version, actor_user_id, target_type, target_id, payload, dedupe_key)
  VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
  ON CONFLICT (dedupe_key) DO NOTHING
  RETURNING id, recipient_user_id, type, category, type_version, payload_version, actor_user_id, target_type, target_id, payload, dedupe_key, read_at, created_at
)
SELECT id, recipient_user_id, type, category, type_version, payload_version, actor_user_id, target_type, target_id, payload, dedupe_key, read_at, created_at, TRUE FROM inserted
UNION ALL
SELECT id, recipient_user_id, type, category, type_version, payload_version, actor_user_id, target_type, target_id, payload, dedupe_key, read_at, created_at, FALSE
FROM notifications WHERE dedupe_key=$10 AND NOT EXISTS (SELECT 1 FROM inserted)`,
		input.RecipientUserID, input.Type, category, typeVersion, payloadVersion, input.ActorUserID, input.TargetType, input.TargetID, payload, input.DedupeKey,
	).Scan(&result.ID, &result.RecipientUserID, &result.Type, &result.Category, &result.TypeVersion, &result.PayloadVersion, &result.ActorUserID, &result.TargetType, &result.TargetID,
		&result.Payload, &result.DedupeKey, &result.ReadAt, &result.CreatedAt, &created)
	if err != nil || !created {
		return result, err
	}
	if err := bumpRecipientRevisionTx(ctx, tx, result.RecipientUserID); err != nil {
		return Notification{}, fmt.Errorf("bump notification recipient revision: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) Create(ctx context.Context, input CreateInput) (Notification, error) {
	return s.CreateNotificationTx(ctx, s.runner, input)
}

func (s *PostgresStore) CreateDeliveryTx(ctx context.Context, tx queryRunner, input CreateDeliveryInput) (MailDelivery, error) {
	templateData := input.TemplateData
	if len(templateData) == 0 {
		templateData = json.RawMessage(`{}`)
	}
	var item MailDelivery
	err := tx.QueryRow(ctx, `INSERT INTO mail_deliveries (recipient, template_key, template_data, idempotency_key, correlation_id)
VALUES ($1,$2,$3,$4,$5) ON CONFLICT (idempotency_key) DO UPDATE SET idempotency_key=EXCLUDED.idempotency_key
RETURNING id, recipient, template_key, template_data, idempotency_key, correlation_id, status, extension_id,
attempt_count, reason, error_summary, created_at, updated_at, completed_at`, input.Recipient, input.TemplateKey, templateData,
		input.IdempotencyKey, input.CorrelationID).Scan(&item.ID, &item.Recipient, &item.TemplateKey, &item.TemplateData,
		&item.IdempotencyKey, &item.CorrelationID, &item.Status, &item.ExtensionID, &item.AttemptCount, &item.Reason,
		&item.ErrorSummary, &item.CreatedAt, &item.UpdatedAt, &item.CompletedAt)
	return item, err
}

func (s *PostgresStore) List(ctx context.Context, input ListInput) (Page, error) {
	limit := input.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.runner.Query(ctx, `SELECT notifications.id, notifications.recipient_user_id, notifications.type, notifications.category,
notifications.type_version, notifications.payload_version, notifications.actor_user_id, notifications.target_type,
notifications.target_id, notifications.payload, notifications.dedupe_key, notifications.read_at, notifications.created_at,
actor_users.id, actor_users.username, actor_users.display_name, actor_users.email,
actor_profiles.avatar_attachment_id, actor_avatars.id, actor_avatars.public_id, actor_avatars.owner_user_id,
actor_avatars.content_type, actor_avatars.status
FROM notifications
LEFT JOIN users actor_users ON actor_users.id=notifications.actor_user_id
LEFT JOIN user_profiles actor_profiles ON actor_profiles.user_id=actor_users.id
LEFT JOIN attachments actor_avatars ON actor_avatars.id=actor_profiles.avatar_attachment_id
WHERE notifications.recipient_user_id=$1 AND ($2::bigint=0 OR notifications.id<$2)
  AND ($3='' OR notifications.category=$3) AND ($4='' OR notifications.type=$4)
  AND ($5::boolean IS NULL OR (notifications.read_at IS NULL)=$5)
ORDER BY notifications.id DESC LIMIT $6`, input.RecipientUserID, input.BeforeID, input.Category, input.Type, input.Unread, limit+1)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	page := Page{Items: make([]Notification, 0, limit)}
	for rows.Next() {
		item, err := s.scanNotificationWithActor(ctx, rows)
		if err != nil {
			return Page{}, err
		}
		if len(page.Items) == limit {
			page.HasMore = true
			break
		}
		page.Items = append(page.Items, item)
	}
	return page, rows.Err()
}

func (s *PostgresStore) GetNotification(ctx context.Context, userID, id int64) (Notification, error) {
	item, err := s.scanNotificationWithActor(ctx, s.runner.QueryRow(ctx, `SELECT notifications.id, notifications.recipient_user_id,
notifications.type, notifications.category, notifications.type_version, notifications.payload_version,
notifications.actor_user_id, notifications.target_type, notifications.target_id, notifications.payload,
notifications.dedupe_key, notifications.read_at, notifications.created_at,
actor_users.id, actor_users.username, actor_users.display_name, actor_users.email,
actor_profiles.avatar_attachment_id, actor_avatars.id, actor_avatars.public_id, actor_avatars.owner_user_id,
actor_avatars.content_type, actor_avatars.status
FROM notifications
LEFT JOIN users actor_users ON actor_users.id=notifications.actor_user_id
LEFT JOIN user_profiles actor_profiles ON actor_profiles.user_id=actor_users.id
LEFT JOIN attachments actor_avatars ON actor_avatars.id=actor_profiles.avatar_attachment_id
WHERE notifications.id=$1 AND notifications.recipient_user_id=$2`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Notification{}, ErrNotificationNotFound
	}
	return item, err
}

type notificationScanner interface {
	Scan(...any) error
}

func (s *PostgresStore) scanNotificationWithActor(ctx context.Context, row notificationScanner) (Notification, error) {
	var item Notification
	var actorID sql.NullInt64
	var username, displayName, email sql.NullString
	var avatarAttachmentID, attachmentID, attachmentOwnerID sql.NullInt64
	var attachmentPublicID, attachmentContentType, attachmentStatus sql.NullString
	if err := row.Scan(
		&item.ID, &item.RecipientUserID, &item.Type, &item.Category, &item.TypeVersion, &item.PayloadVersion,
		&item.ActorUserID, &item.TargetType, &item.TargetID, &item.Payload, &item.DedupeKey, &item.ReadAt, &item.CreatedAt,
		&actorID, &username, &displayName, &email, &avatarAttachmentID, &attachmentID, &attachmentPublicID,
		&attachmentOwnerID, &attachmentContentType, &attachmentStatus,
	); err != nil {
		return Notification{}, err
	}
	if notificationUsesUserAvatar(item.Type) && actorID.Valid {
		item.Actor = &NotificationActor{
			ID:          actorID.Int64,
			Username:    username.String,
			DisplayName: displayName.String,
			Avatar: s.avatarBuilder.AvatarView(ctx, avatar.User{
				UserID: actorID.Int64, Username: username.String, DisplayName: displayName.String, Email: email.String,
			}, avatar.Source{
				AttachmentID: nullableNotificationID(avatarAttachmentID),
				Attachment:   notificationAvatarAttachment(attachmentID, attachmentPublicID, attachmentOwnerID, attachmentContentType, attachmentStatus),
			}),
		}
	}
	return item, nil
}

func notificationUsesUserAvatar(typ string) bool {
	return typ == TypeReply || typ == TypeMention
}

func nullableNotificationID(value sql.NullInt64) *int64 {
	if !value.Valid || value.Int64 <= 0 {
		return nil
	}
	id := value.Int64
	return &id
}

func notificationAvatarAttachment(
	id sql.NullInt64,
	publicID sql.NullString,
	ownerID sql.NullInt64,
	contentType, status sql.NullString,
) *avatar.Attachment {
	if !id.Valid || id.Int64 <= 0 {
		return nil
	}
	return &avatar.Attachment{
		ID: id.Int64, PublicID: publicID.String, OwnerUserID: ownerID.Int64,
		ContentType: contentType.String, Status: status.String,
	}
}

func (s *PostgresStore) UnreadCount(ctx context.Context, userID int64) (int64, error) {
	var count int64
	err := s.runner.QueryRow(ctx, `SELECT COUNT(*) FROM notifications WHERE recipient_user_id=$1 AND read_at IS NULL`, userID).Scan(&count)
	return count, err
}

func (s *PostgresStore) MarkRead(ctx context.Context, userID, id int64) error {
	if s.pool != nil {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		var exists, changed bool
		if err := tx.QueryRow(ctx, `
			WITH target AS (
			  SELECT id FROM notifications WHERE id=$1 AND recipient_user_id=$2
			), changed AS (
			  UPDATE notifications SET read_at=now()
			  WHERE id=$1 AND recipient_user_id=$2 AND read_at IS NULL
			  RETURNING id
			)
			SELECT EXISTS(SELECT 1 FROM target), EXISTS(SELECT 1 FROM changed)`, id, userID).Scan(&exists, &changed); err != nil {
			return err
		}
		if !exists {
			return ErrNotificationNotFound
		}
		if changed {
			if err := bumpRecipientRevisionTx(ctx, tx, userID); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	}
	tag, err := s.runner.Exec(ctx, `UPDATE notifications SET read_at=COALESCE(read_at, NOW()) WHERE id=$1 AND recipient_user_id=$2`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotificationNotFound
	}
	if err != nil {
		return err
	}
	return s.bumpRecipientRevision(ctx, userID)
}

func (s *PostgresStore) MarkAllRead(ctx context.Context, userID int64) (int64, error) {
	if s.pool != nil {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return 0, err
		}
		defer tx.Rollback(ctx)
		tag, err := tx.Exec(ctx, `UPDATE notifications SET read_at=now() WHERE recipient_user_id=$1 AND read_at IS NULL`, userID)
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() > 0 {
			if err := bumpRecipientRevisionTx(ctx, tx, userID); err != nil {
				return 0, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return 0, err
		}
		return tag.RowsAffected(), nil
	}
	tag, err := s.runner.Exec(ctx, `UPDATE notifications SET read_at=NOW() WHERE recipient_user_id=$1 AND read_at IS NULL`, userID)
	if err != nil || tag.RowsAffected() == 0 {
		return tag.RowsAffected(), err
	}
	return tag.RowsAffected(), s.bumpRecipientRevision(ctx, userID)
}

func (s *PostgresStore) bumpRecipientRevision(ctx context.Context, userID int64) error {
	return bumpRecipientRevisionTx(ctx, s.runner, userID)
}

func bumpRecipientRevisionTx(ctx context.Context, runner queryRunner, userID int64) error {
	_, err := runner.Exec(ctx, `
		INSERT INTO notification_recipient_revisions (user_id, revision)
		VALUES ($1, 1)
		ON CONFLICT (user_id) DO UPDATE
		SET revision=notification_recipient_revisions.revision+1, updated_at=now()`, userID)
	if err != nil {
		return err
	}
	// PostgreSQL 只会在事务提交后投递 NOTIFY；payload 仅含收件人 id，
	// 私有通知正文始终通过登录态 REST 读取。
	_, err = runner.Exec(ctx, `SELECT pg_notify('sforum_notification_revision', $1::text)`, fmt.Sprint(userID))
	return err
}

func (s *PostgresStore) RecipientRevision(ctx context.Context, userID int64) (int64, error) {
	var revision int64
	err := s.runner.QueryRow(ctx, `SELECT revision FROM notification_recipient_revisions WHERE user_id=$1`, userID).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return revision, err
}

func (s *PostgresStore) SubscribeRevision(userID int64) (<-chan struct{}, func(), error) {
	if s == nil || s.wakes == nil {
		return nil, nil, ErrRevisionWakeUnavailable
	}
	return s.wakes.Subscribe(userID)
}

func (s *PostgresStore) GetDelivery(ctx context.Context, id int64) (MailDelivery, error) {
	var item MailDelivery
	err := s.runner.QueryRow(ctx, `SELECT id, recipient, template_key, template_data, idempotency_key, correlation_id,
status, extension_id, attempt_count, reason, error_summary, created_at, updated_at, completed_at FROM mail_deliveries WHERE id=$1`, id).Scan(
		&item.ID, &item.Recipient, &item.TemplateKey, &item.TemplateData, &item.IdempotencyKey, &item.CorrelationID,
		&item.Status, &item.ExtensionID, &item.AttemptCount, &item.Reason, &item.ErrorSummary, &item.CreatedAt, &item.UpdatedAt, &item.CompletedAt)
	return item, err
}

func (s *PostgresStore) UpdateDelivery(ctx context.Context, input DeliveryUpdate) error {
	completed := any(nil)
	// 终态判定走共享 outbox 约定，避免各 vertical 各写一套。
	if outbox.ShouldSetCompletedAt(input.Status) {
		completed = time.Now().UTC()
	}
	_, err := s.runner.Exec(ctx, `UPDATE mail_deliveries SET status=$2, extension_id=$3, attempt_count=$4, reason=$5,
error_summary=$6, updated_at=NOW(), completed_at=COALESCE($7, completed_at) WHERE id=$1`, input.ID, input.Status,
		input.ExtensionID, input.AttemptCount, input.Reason, input.ErrorSummary, completed)
	return err
}

func (s *PostgresStore) ListDeliveries(ctx context.Context, limit int) ([]MailDelivery, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.runner.Query(ctx, `SELECT id, recipient, template_key, '{}'::jsonb, idempotency_key, correlation_id,
status, extension_id, attempt_count, reason, error_summary, created_at, updated_at, completed_at FROM mail_deliveries ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []MailDelivery{}
	for rows.Next() {
		var item MailDelivery
		if err := rows.Scan(&item.ID, &item.Recipient, &item.TemplateKey, &item.TemplateData, &item.IdempotencyKey, &item.CorrelationID, &item.Status, &item.ExtensionID, &item.AttemptCount, &item.Reason, &item.ErrorSummary, &item.CreatedAt, &item.UpdatedAt, &item.CompletedAt); err != nil {
			return nil, err
		}
		item.IdempotencyKey = ""
		items = append(items, item)
	}
	return items, rows.Err()
}
