package webhooks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/keeptrue/sforum/apps/api/app/Support/Outbox"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) ListEndpoints(ctx context.Context) ([]EndpointRecord, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, name, target_url, secret, events, enabled, description, created_at, updated_at
FROM webhook_endpoints ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []EndpointRecord{}
	for rows.Next() {
		item, err := scanEndpoint(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *PostgresStore) GetEndpoint(ctx context.Context, id int64) (EndpointRecord, error) {
	row := s.pool.QueryRow(ctx, `
SELECT id, name, target_url, secret, events, enabled, description, created_at, updated_at
FROM webhook_endpoints WHERE id=$1`, id)
	item, err := scanEndpoint(row)
	if err == pgx.ErrNoRows {
		return EndpointRecord{}, ErrEndpointNotFound
	}
	return item, err
}

func (s *PostgresStore) CreateEndpoint(ctx context.Context, input CreateEndpointInput) (EndpointRecord, error) {
	eventsJSON, err := json.Marshal(normalizeEvents(input.Events))
	if err != nil {
		return EndpointRecord{}, err
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	row := s.pool.QueryRow(ctx, `
INSERT INTO webhook_endpoints (name, target_url, secret, events, enabled, description)
VALUES ($1,$2,$3,$4,$5,$6)
RETURNING id, name, target_url, secret, events, enabled, description, created_at, updated_at`,
		strings.TrimSpace(input.Name), strings.TrimSpace(input.TargetURL), input.Secret,
		eventsJSON, enabled, strings.TrimSpace(input.Description))
	return scanEndpoint(row)
}

func (s *PostgresStore) UpdateEndpoint(ctx context.Context, id int64, input UpdateEndpointInput) (EndpointRecord, error) {
	current, err := s.GetEndpoint(ctx, id)
	if err != nil {
		return EndpointRecord{}, err
	}
	name := current.Name
	if input.Name != nil {
		name = strings.TrimSpace(*input.Name)
	}
	target := current.TargetURL
	if input.TargetURL != nil {
		target = strings.TrimSpace(*input.TargetURL)
	}
	secret := current.Secret
	if input.ClearSecret {
		secret = ""
	} else if input.Secret != nil && strings.TrimSpace(*input.Secret) != "" {
		secret = *input.Secret
	}
	events := current.Events
	if input.Events != nil {
		events = normalizeEvents(input.Events)
	}
	enabled := current.Enabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	description := current.Description
	if input.Description != nil {
		description = strings.TrimSpace(*input.Description)
	}
	eventsJSON, err := json.Marshal(events)
	if err != nil {
		return EndpointRecord{}, err
	}
	row := s.pool.QueryRow(ctx, `
UPDATE webhook_endpoints
SET name=$2, target_url=$3, secret=$4, events=$5, enabled=$6, description=$7, updated_at=NOW()
WHERE id=$1
RETURNING id, name, target_url, secret, events, enabled, description, created_at, updated_at`,
		id, name, target, secret, eventsJSON, enabled, description)
	item, err := scanEndpoint(row)
	if err == pgx.ErrNoRows {
		return EndpointRecord{}, ErrEndpointNotFound
	}
	return item, err
}

func (s *PostgresStore) DeleteEndpoint(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM webhook_endpoints WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrEndpointNotFound
	}
	return nil
}

// UpdateEndpointSecret 懒迁移：仅更新 secret 列（明文 → 密文）。
func (s *PostgresStore) UpdateEndpointSecret(ctx context.Context, id int64, encrypted string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE webhook_endpoints SET secret=$2, updated_at=NOW() WHERE id=$1`, id, encrypted)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrEndpointNotFound
	}
	return nil
}

func (s *PostgresStore) CreateDeliveryTx(ctx context.Context, tx pgx.Tx, input CreateDeliveryInput) (Delivery, error) {
	payload := input.Payload
	if len(payload) == 0 {
		payload = []byte(`{}`)
	}
	var item Delivery
	err := tx.QueryRow(ctx, `
INSERT INTO webhook_deliveries (endpoint_id, event_name, event_id, correlation_id, payload, status)
VALUES ($1,$2,$3,$4,$5,$6)
RETURNING id, endpoint_id, event_name, event_id, correlation_id, payload, status, attempt_count,
  http_status, response_snippet, reason, error_summary, created_at, updated_at, completed_at`,
		input.EndpointID, input.EventName, input.EventID, input.CorrelationID, payload, StatusQueued,
	).Scan(
		&item.ID, &item.EndpointID, &item.EventName, &item.EventID, &item.CorrelationID, &item.Payload,
		&item.Status, &item.AttemptCount, &item.HTTPStatus, &item.ResponseSnippet, &item.Reason,
		&item.ErrorSummary, &item.CreatedAt, &item.UpdatedAt, &item.CompletedAt,
	)
	return item, err
}

func (s *PostgresStore) GetDelivery(ctx context.Context, id int64) (Delivery, error) {
	var item Delivery
	err := s.pool.QueryRow(ctx, `
SELECT id, endpoint_id, event_name, event_id, correlation_id, payload, status, attempt_count,
  http_status, response_snippet, reason, error_summary, created_at, updated_at, completed_at
FROM webhook_deliveries WHERE id=$1`, id).Scan(
		&item.ID, &item.EndpointID, &item.EventName, &item.EventID, &item.CorrelationID, &item.Payload,
		&item.Status, &item.AttemptCount, &item.HTTPStatus, &item.ResponseSnippet, &item.Reason,
		&item.ErrorSummary, &item.CreatedAt, &item.UpdatedAt, &item.CompletedAt,
	)
	if err == pgx.ErrNoRows {
		return Delivery{}, ErrDeliveryNotFound
	}
	return item, err
}

func (s *PostgresStore) UpdateDelivery(ctx context.Context, input DeliveryUpdate) error {
	completed := any(nil)
	if outbox.ShouldSetCompletedAt(input.Status) {
		completed = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `
UPDATE webhook_deliveries
SET status=$2, attempt_count=$3, http_status=$4, response_snippet=$5, reason=$6, error_summary=$7,
    updated_at=NOW(), completed_at=COALESCE($8, completed_at)
WHERE id=$1`,
		input.ID, input.Status, input.AttemptCount, input.HTTPStatus, input.ResponseSnippet,
		input.Reason, input.ErrorSummary, completed)
	return err
}

func (s *PostgresStore) ListDeliveries(ctx context.Context, endpointID int64, limit int) ([]Delivery, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var rows pgx.Rows
	var err error
	if endpointID > 0 {
		rows, err = s.pool.Query(ctx, `
SELECT id, endpoint_id, event_name, event_id, correlation_id, payload, status, attempt_count,
  http_status, response_snippet, reason, error_summary, created_at, updated_at, completed_at
FROM webhook_deliveries WHERE endpoint_id=$1 ORDER BY id DESC LIMIT $2`, endpointID, limit)
	} else {
		rows, err = s.pool.Query(ctx, `
SELECT id, endpoint_id, event_name, event_id, correlation_id, payload, status, attempt_count,
  http_status, response_snippet, reason, error_summary, created_at, updated_at, completed_at
FROM webhook_deliveries ORDER BY id DESC LIMIT $1`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Delivery{}
	for rows.Next() {
		var item Delivery
		if err := rows.Scan(
			&item.ID, &item.EndpointID, &item.EventName, &item.EventID, &item.CorrelationID, &item.Payload,
			&item.Status, &item.AttemptCount, &item.HTTPStatus, &item.ResponseSnippet, &item.Reason,
			&item.ErrorSummary, &item.CreatedAt, &item.UpdatedAt, &item.CompletedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *PostgresStore) ListEnabledEndpointsForEvent(ctx context.Context, eventName string) ([]EndpointRecord, error) {
	// 订阅规则：events 空数组 = 全部 observe；否则 JSON 数组包含事件名。
	rows, err := s.pool.Query(ctx, `
SELECT id, name, target_url, secret, events, enabled, description, created_at, updated_at
FROM webhook_endpoints
WHERE enabled = true
  AND (
    events = '[]'::jsonb
    OR events @> to_jsonb(ARRAY[$1::text])
  )
ORDER BY id ASC`, eventName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []EndpointRecord{}
	for rows.Next() {
		item, err := scanEndpoint(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type scannable interface {
	Scan(dest ...any) error
}

func scanEndpoint(row scannable) (EndpointRecord, error) {
	var item EndpointRecord
	var eventsRaw []byte
	if err := row.Scan(
		&item.ID, &item.Name, &item.TargetURL, &item.Secret, &eventsRaw,
		&item.Enabled, &item.Description, &item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		return EndpointRecord{}, err
	}
	item.Events = []string{}
	if len(eventsRaw) > 0 {
		_ = json.Unmarshal(eventsRaw, &item.Events)
	}
	item.HasSecret = strings.TrimSpace(item.Secret) != ""
	item.SecretMasked = maskSecret(item.Secret)
	return item, nil
}

func normalizeEvents(events []string) []string {
	if events == nil {
		return []string{}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(events))
	for _, event := range events {
		event = strings.TrimSpace(event)
		if event == "" {
			continue
		}
		if _, ok := seen[event]; ok {
			continue
		}
		seen[event] = struct{}{}
		out = append(out, event)
	}
	return out
}

func maskSecret(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	if len(secret) <= 4 {
		return "****"
	}
	return "****" + secret[len(secret)-4:]
}

// PublicEndpoint 去掉 secret 明文后再返回 API。
func PublicEndpoint(record EndpointRecord) Endpoint {
	ep := record.Endpoint
	ep.HasSecret = strings.TrimSpace(record.Secret) != ""
	ep.SecretMasked = maskSecret(record.Secret)
	return ep
}

func FormatEndpointError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w", err)
}
