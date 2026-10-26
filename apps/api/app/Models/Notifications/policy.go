package notifications

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

type DeliveryPolicyResolver interface {
	DeliveryEnabled(context.Context, int64, string, string) (bool, error)
}

// DeliveryEnabled applies the V2 hard-limit, required, inherit and explicit
// user override rules. Provider availability is deliberately not folded into
// this decision: a missing external provider must not suppress the inbox.
func (s *PostgresStore) DeliveryEnabled(ctx context.Context, userID int64, typ, channel string) (bool, error) {
	return deliveryEnabled(ctx, s.runner, userID, typ, channel)
}

// DeliveryEnabledTx keeps policy and projection on the caller's transaction
// snapshot. Host commands must not resolve preferences through a second pool
// connection while their receipt and notification rows are still uncommitted.
func (s *PostgresStore) DeliveryEnabledTx(ctx context.Context, tx pgx.Tx, userID int64, typ, channel string) (bool, error) {
	return deliveryEnabled(ctx, tx, userID, typ, channel)
}

func deliveryEnabled(ctx context.Context, runner queryRunner, userID int64, typ, channel string) (bool, error) {
	var active, enabled, recommended, configurable, required bool
	var preference string
	err := runner.QueryRow(ctx, `
		SELECT descriptor.active, policy.enabled, policy.recommended_enabled,
		  policy.user_configurable, policy.required,
		  COALESCE(preference.state, 'inherit')
		FROM notification_type_descriptors descriptor
		JOIN notification_type_policies policy ON policy.type=descriptor.type
		LEFT JOIN notification_preferences preference
		  ON preference.user_id=$1 AND preference.type=descriptor.type AND preference.channel=policy.channel
		WHERE descriptor.type=$2 AND policy.channel=$3`, userID, typ, channel,
	).Scan(&active, &enabled, &recommended, &configurable, &required, &preference)
	if err != nil {
		return false, fmt.Errorf("resolve notification delivery policy: %w", err)
	}
	if !active || !enabled {
		return false, nil
	}
	if required {
		return channel == "in_app", nil
	}
	if !configurable || preference == "inherit" {
		return recommended, nil
	}
	return preference == "enabled", nil
}

// NotificationPolicy resolves the legacy Core projection view from the V2
// type/channel authority. It deliberately has the same shape as the temporary
// Mail compatibility API so callers cannot create a second policy source.
func (s *PostgresStore) NotificationPolicy(ctx context.Context) (options.NotificationPolicy, error) {
	var replyInApp, replyEmail, mentionInApp, mentionEmail bool
	var moderationInApp, moderationEmail bool
	err := s.runner.QueryRow(ctx, `
		SELECT
		  COALESCE(BOOL_OR(enabled) FILTER (WHERE type = 'reply' AND channel = 'in_app'), FALSE),
		  COALESCE(BOOL_OR(enabled) FILTER (WHERE type = 'reply' AND channel = 'email'), FALSE),
		  COALESCE(BOOL_OR(enabled) FILTER (WHERE type = 'mention' AND channel = 'in_app'), FALSE),
		  COALESCE(BOOL_OR(enabled) FILTER (WHERE type = 'mention' AND channel = 'email'), FALSE),
		  COALESCE(BOOL_AND(enabled) FILTER (WHERE type IN ('moderation_approved', 'moderation_rejected') AND channel = 'in_app'), FALSE),
		  COALESCE(BOOL_AND(enabled) FILTER (WHERE type IN ('moderation_approved', 'moderation_rejected') AND channel = 'email'), FALSE)
		FROM notification_type_policies`).Scan(&replyInApp, &replyEmail, &mentionInApp, &mentionEmail, &moderationInApp, &moderationEmail)
	if err != nil {
		return options.NotificationPolicy{}, fmt.Errorf("read notification policy: %w", err)
	}
	return options.NotificationPolicy{
		Reply:      options.ChannelPolicy{InAppEnabled: replyInApp, EmailEnabled: replyEmail},
		Mention:    options.ChannelPolicy{InAppEnabled: mentionInApp, EmailEnabled: mentionEmail},
		Moderation: options.ChannelPolicy{InAppEnabled: moderationInApp, EmailEnabled: moderationEmail},
	}, nil
}

// UpdateCoreNotificationPolicy updates only the legacy Core type projection.
// Plugin policy remains separately owned by its descriptor lifecycle and the
// dedicated notification settings surface.
func (s *PostgresStore) UpdateCoreNotificationPolicy(ctx context.Context, policy options.NotificationPolicy) error {
	updates := []struct {
		types []string
		value options.ChannelPolicy
	}{
		{[]string{TypeReply}, policy.Reply},
		{[]string{TypeMention}, policy.Mention},
		{[]string{TypeModerationApproved, TypeModerationRejected}, policy.Moderation},
	}
	for _, update := range updates {
		if _, err := s.runner.Exec(ctx, `
			UPDATE notification_type_policies
			SET enabled = CASE channel WHEN 'in_app' THEN $2 WHEN 'email' THEN $3 ELSE enabled END,
			    recommended_enabled = CASE channel WHEN 'in_app' THEN $2 WHEN 'email' THEN $3 ELSE recommended_enabled END,
			    revision = revision + 1, updated_at = now()
			WHERE type = ANY($1) AND channel IN ('in_app', 'email')`, update.types, update.value.InAppEnabled, update.value.EmailEnabled); err != nil {
			return fmt.Errorf("update notification policy: %w", err)
		}
	}
	return nil
}

func (s *PostgresStore) RestoreCoreNotificationPolicy(ctx context.Context) error {
	_, err := s.runner.Exec(ctx, `
		UPDATE notification_type_policies
		SET enabled = (channel = 'in_app'), recommended_enabled = (channel = 'in_app'),
		    revision = revision + 1, updated_at = now()
		WHERE type IN ('reply', 'mention', 'moderation_approved', 'moderation_rejected')
		  AND channel IN ('in_app', 'email')`)
	return err
}
