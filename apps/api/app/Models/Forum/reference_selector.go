package forum

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	avatar "github.com/keeptrue/sforum/apps/api/app/Support/Avatar"
)

const (
	ReferenceKindUser     = "user"
	ReferenceKindTopic    = "topic"
	ReferenceKindComment  = "comment"
	ReferenceKindCategory = "category"

	ReferenceSelectorDefaultLimit  = 12
	ReferenceSelectorMaxLimit      = 20
	ReferenceSelectorMaxQueryRunes = 80
)

var ErrInvalidReferenceSelector = errors.New("forum: invalid reference selector")

const CodeReferenceSelectorInvalid = "forum.reference_selector_invalid"

type ReferenceSelectorInput struct {
	Kind       string
	Query      string
	SelectedID int64
	Limit      int
}

type ReferenceOption struct {
	ID             int64        `json:"id"`
	Label          string       `json:"label"`
	SecondaryLabel string       `json:"secondaryLabel,omitempty"`
	Avatar         *avatar.View `json:"avatar,omitempty"`
	Icon           string       `json:"icon,omitempty"`
	IconColor      string       `json:"iconColor,omitempty"`
}

type ReferenceOptionList struct {
	Items   []ReferenceOption `json:"items"`
	HasMore bool              `json:"hasMore"`
}

type ReferenceSelectorStore interface {
	ListReferenceOptions(context.Context, ReferenceSelectorInput) ([]ReferenceOption, error)
}

// ReferenceSelector owns the authenticated composer lookup workflow instead of
// growing the legacy Forum Service facade.
type ReferenceSelector struct {
	store ReferenceSelectorStore
}

func NewReferenceSelector(store ReferenceSelectorStore) *ReferenceSelector {
	return &ReferenceSelector{store: store}
}

// ReferenceSelectorFromService is a compatibility assembly helper for the
// existing forum controller constructors.
func ReferenceSelectorFromService(service *Service) *ReferenceSelector {
	if service == nil {
		return NewReferenceSelector(nil)
	}
	store, _ := service.store.(ReferenceSelectorStore)
	return NewReferenceSelector(store)
}

func (s *ReferenceSelector) ListReferenceOptions(
	ctx context.Context,
	actor identity.Actor,
	input ReferenceSelectorInput,
) (ReferenceOptionList, error) {
	if actor.ID <= 0 || actor.Status != identity.UserStatusActive {
		return ReferenceOptionList{}, identity.ErrPermissionDenied
	}
	normalized, err := normalizeReferenceSelectorInput(input)
	if err != nil {
		return ReferenceOptionList{}, err
	}
	if s == nil || s.store == nil {
		return ReferenceOptionList{}, ErrInvalidReferenceSelector
	}
	items, err := s.store.ListReferenceOptions(ctx, normalized)
	if err != nil {
		return ReferenceOptionList{}, err
	}
	hasMore := len(items) > normalized.Limit
	if hasMore {
		items = items[:normalized.Limit]
	}
	if items == nil {
		items = []ReferenceOption{}
	}
	return ReferenceOptionList{Items: items, HasMore: hasMore}, nil
}

func normalizeReferenceSelectorInput(input ReferenceSelectorInput) (ReferenceSelectorInput, error) {
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	switch input.Kind {
	case ReferenceKindUser, ReferenceKindTopic, ReferenceKindComment, ReferenceKindCategory:
	default:
		return ReferenceSelectorInput{}, ErrInvalidReferenceSelector
	}
	input.Query = strings.TrimSpace(input.Query)
	if utf8.RuneCountInString(input.Query) > ReferenceSelectorMaxQueryRunes || input.SelectedID < 0 {
		return ReferenceSelectorInput{}, ErrInvalidReferenceSelector
	}
	if input.Limit <= 0 {
		input.Limit = ReferenceSelectorDefaultLimit
	}
	if input.Limit > ReferenceSelectorMaxLimit {
		input.Limit = ReferenceSelectorMaxLimit
	}
	return input, nil
}

func (s *PostgresStore) ListReferenceOptions(
	ctx context.Context,
	input ReferenceSelectorInput,
) ([]ReferenceOption, error) {
	query, args := referenceSelectorSQL(input)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list %s reference options: %w", input.Kind, err)
	}
	defer rows.Close()
	items := make([]ReferenceOption, 0, input.Limit+1)
	for rows.Next() {
		var item ReferenceOption
		var err error
		switch input.Kind {
		case ReferenceKindUser:
			var username, displayName, email sql.NullString
			var avatarAttachmentID, attachmentID, attachmentOwnerID sql.NullInt64
			var attachmentPublicID, attachmentContentType, attachmentStatus sql.NullString
			err = rows.Scan(
				&item.ID, &item.Label, &item.SecondaryLabel,
				&username, &displayName, &email,
				&avatarAttachmentID, &attachmentID, &attachmentPublicID,
				&attachmentOwnerID, &attachmentContentType, &attachmentStatus,
			)
			if err == nil {
				user := userSummaryWithAvatar(s.avatarBuilder, sql.NullInt64{Int64: item.ID, Valid: true}, username, displayName, email, avatarAttachmentID, attachmentID, attachmentPublicID, attachmentOwnerID, attachmentContentType, attachmentStatus)
				if user != nil {
					item.Avatar = &user.Avatar
				}
			}
		case ReferenceKindCategory:
			err = rows.Scan(&item.ID, &item.Label, &item.SecondaryLabel, &item.Icon, &item.IconColor)
		default:
			err = rows.Scan(&item.ID, &item.Label, &item.SecondaryLabel)
		}
		if err != nil {
			return nil, fmt.Errorf("scan %s reference option: %w", input.Kind, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s reference options: %w", input.Kind, err)
	}
	return items, nil
}

func referenceSelectorSQL(input ReferenceSelectorInput) (string, []any) {
	pattern := "%" + escapeReferenceLike(input.Query) + "%"
	limit := input.Limit + 1
	args := []any{pattern, input.SelectedID, limit}
	order := `ORDER BY (id = $2) DESC, rank_label ASC, id ASC LIMIT $3`
	switch input.Kind {
	case ReferenceKindUser:
		return `
			SELECT id, label, secondary_label, username, display_name, email,
				avatar_attachment_id, attachment_id, attachment_public_id,
				attachment_owner_id, attachment_content_type, attachment_status
			FROM (
				SELECT users.id,
				  COALESCE(NULLIF(BTRIM(users.display_name), ''), users.username) AS label,
				  '@' || users.username AS secondary_label,
				  users.username, users.display_name, users.email,
				  user_profiles.avatar_attachment_id,
				  avatar_attachments.id AS attachment_id,
				  avatar_attachments.public_id AS attachment_public_id,
				  avatar_attachments.owner_user_id AS attachment_owner_id,
				  avatar_attachments.content_type AS attachment_content_type,
				  avatar_attachments.status AS attachment_status,
				  LOWER(COALESCE(NULLIF(BTRIM(users.display_name), ''), users.username)) AS rank_label
				FROM users
				LEFT JOIN user_profiles ON user_profiles.user_id = users.id
				LEFT JOIN attachments avatar_attachments
				  ON avatar_attachments.id = user_profiles.avatar_attachment_id
				WHERE users.status = 'active'
				  AND ($2 = users.id OR $1 = '%%' OR users.username ILIKE $1 ESCAPE E'\\'
				    OR users.display_name ILIKE $1 ESCAPE E'\\')
			) options
			` + order, args
	case ReferenceKindTopic:
		return `
			SELECT id, label, secondary_label
			FROM (
				SELECT topics.id, topics.title AS label, categories.name AS secondary_label,
				  LOWER(topics.title) AS rank_label
				FROM topics
				JOIN categories ON categories.id = topics.category_id
				JOIN category_groups ON category_groups.id = categories.group_id
				WHERE topics.status IN ('active', 'locked')
				  AND categories.visibility = 'public' AND category_groups.visibility = 'public'
				  AND ($2 = topics.id OR $1 = '%%' OR topics.title ILIKE $1 ESCAPE E'\\'
				    OR topics.id::text = $4)
			) options
			` + order, append(args, input.Query)
	case ReferenceKindComment:
		return `
			SELECT id, label, secondary_label
			FROM (
				SELECT comments.id,
				  '#' || comments.id::text || COALESCE(' · ' || NULLIF(BTRIM(COALESCE(NULLIF(BTRIM(users.display_name), ''), users.username, '')), ''), '') AS label,
				  topics.title AS secondary_label,
				  LOWER(COALESCE(NULLIF(BTRIM(users.display_name), ''), users.username, topics.title)) AS rank_label
				FROM comments
				JOIN topics ON topics.id = comments.topic_id
				JOIN categories ON categories.id = topics.category_id
				JOIN category_groups ON category_groups.id = categories.group_id
				LEFT JOIN users ON users.id = comments.author_user_id
				WHERE comments.status = 'active' AND topics.status IN ('active', 'locked')
				  AND categories.visibility = 'public' AND category_groups.visibility = 'public'
				  AND ($2 = comments.id OR $1 = '%%' OR comments.id::text = $4
				    OR users.username ILIKE $1 ESCAPE E'\\' OR users.display_name ILIKE $1 ESCAPE E'\\'
				    OR topics.title ILIKE $1 ESCAPE E'\\')
			) options
			` + order, append(args, input.Query)
	default:
		return `
			SELECT id, label, secondary_label, icon, icon_color
			FROM (
				SELECT categories.id, categories.name AS label, category_groups.name AS secondary_label,
				  categories.icon, categories.icon_color,
				  LOWER(categories.name) AS rank_label
				FROM categories
				JOIN category_groups ON category_groups.id = categories.group_id
				WHERE categories.visibility = 'public' AND category_groups.visibility = 'public'
				  AND ($2 = categories.id OR $1 = '%%' OR categories.name ILIKE $1 ESCAPE E'\\'
				    OR categories.slug ILIKE $1 ESCAPE E'\\' OR category_groups.name ILIKE $1 ESCAPE E'\\')
			) options
			` + order, args
	}
}

func escapeReferenceLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}

var _ ReferenceSelectorStore = (*PostgresStore)(nil)
