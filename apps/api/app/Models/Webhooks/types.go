package webhooks

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/keeptrue/sforum/apps/api/app/Support/Outbox"
)

const (
	// 投递状态复用 outbox 词表。
	StatusQueued  = outbox.StatusQueued
	StatusSending = outbox.StatusSending
	StatusSent    = outbox.StatusSent
	StatusFailed  = outbox.StatusFailed
	StatusSkipped = outbox.StatusSkipped
	StatusDead    = outbox.StatusDead

	// DefaultMaxAttempts River + 本地 attempt 预算。
	DefaultMaxAttempts = 5
)

var (
	ErrEndpointNotFound = errors.New("webhooks: endpoint not found")
	ErrDeliveryNotFound = errors.New("webhooks: delivery not found")
	ErrInvalidEndpoint  = errors.New("webhooks: invalid endpoint")
	ErrInvalidURL       = errors.New("webhooks: invalid target url")
)

// Endpoint 是管理员配置的出站订阅。
type Endpoint struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	TargetURL    string `json:"targetUrl"`
	SecretMasked string `json:"secretMasked"`
	// HasSecret 表示是否已配置签名密钥（不回传明文）。
	HasSecret   bool      `json:"hasSecret"`
	Events      []string  `json:"events"`
	Enabled     bool      `json:"enabled"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// EndpointRecord 内部行（含 secret 明文，仅 store/worker 使用）。
type EndpointRecord struct {
	Endpoint
	Secret string
}

type Delivery struct {
	ID              int64           `json:"id"`
	EndpointID      int64           `json:"endpointId"`
	EventName       string          `json:"eventName"`
	EventID         string          `json:"eventId,omitempty"`
	CorrelationID   string          `json:"correlationId,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	Status          string          `json:"status"`
	AttemptCount    int             `json:"attemptCount"`
	HTTPStatus      int             `json:"httpStatus,omitempty"`
	ResponseSnippet string          `json:"responseSnippet,omitempty"`
	Reason          string          `json:"reason,omitempty"`
	ErrorSummary    string          `json:"errorSummary,omitempty"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
	CompletedAt     *time.Time      `json:"completedAt,omitempty"`
}

type CreateEndpointInput struct {
	Name        string
	TargetURL   string
	Secret      string
	Events      []string
	Enabled     *bool
	Description string
}

type UpdateEndpointInput struct {
	Name        *string
	TargetURL   *string
	Secret      *string // 空字符串表示不改；非空则轮换
	Events      []string
	Enabled     *bool
	Description *string
	ClearSecret bool
}

type CreateDeliveryInput struct {
	EndpointID    int64
	EventName     string
	EventID       string
	CorrelationID string
	Payload       json.RawMessage
}

type DeliveryUpdate struct {
	ID              int64
	Status          string
	AttemptCount    int
	HTTPStatus      int
	ResponseSnippet string
	Reason          string
	ErrorSummary    string
}
