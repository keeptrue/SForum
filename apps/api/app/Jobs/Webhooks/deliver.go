package webhookjobs

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/riverqueue/river"
	webhooks "github.com/keeptrue/sforum/apps/api/app/Models/Webhooks"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	outboundhttp "github.com/keeptrue/sforum/apps/api/app/Support/OutboundHTTP"
	"github.com/keeptrue/sforum/apps/api/app/Support/Outbox"
)

type DeliverArgs struct {
	DeliveryID int64 `json:"delivery_id" river:"unique"`
}

func (DeliverArgs) Kind() string { return "webhook.deliver" }
func (DeliverArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueDefault,
		MaxAttempts: webhooks.DefaultMaxAttempts,
		Unique:      river.UniqueOpts{ByArgs: true},
	}
}

type DeliveryStore interface {
	GetDelivery(context.Context, int64) (webhooks.Delivery, error)
	UpdateDelivery(context.Context, webhooks.DeliveryUpdate) error
	GetEndpoint(context.Context, int64) (webhooks.EndpointRecord, error)
}

// SecretCipher 解密 webhook 签名密钥（兼容历史明文）。
type SecretCipher interface {
	DecryptEndpointSecret(stored string) (string, error)
	MaybeMigrateSecret(stored string) (encrypted string, ok bool)
}

// SecretMigrator 可选：将历史明文 secret 懒迁移为密文。
type SecretMigrator interface {
	UpdateEndpointSecret(ctx context.Context, id int64, encrypted string) error
}

type DeliverWorker struct {
	river.WorkerDefaults[DeliverArgs]
	Store  DeliveryStore
	Client *http.Client
	// Secrets 解密 signing secret；nil 时按明文使用（仅测试/无密钥开发）。
	Secrets SecretCipher
	// Migrator 可选懒迁移明文 secret。
	Migrator SecretMigrator
	// AllowHTTP 与配置时校验一致；生产应 false。
	AllowHTTP bool
	Now       func() time.Time
}

func (w *DeliverWorker) Work(ctx context.Context, job *river.Job[DeliverArgs]) error {
	if w.Store == nil {
		return fmt.Errorf("webhook deliver worker is not configured")
	}
	delivery, err := w.Store.GetDelivery(ctx, job.Args.DeliveryID)
	if err != nil {
		return err
	}
	if outbox.IsTerminal(delivery.Status) {
		return nil
	}
	endpoint, err := w.Store.GetEndpoint(ctx, delivery.EndpointID)
	if err != nil {
		// 端点已删：标记 skipped，避免无限重试。
		_ = w.Store.UpdateDelivery(ctx, webhooks.DeliveryUpdate{
			ID: delivery.ID, Status: webhooks.StatusSkipped, AttemptCount: delivery.AttemptCount + 1,
			Reason: "endpoint_missing", ErrorSummary: err.Error(),
		})
		return nil
	}
	if !endpoint.Enabled {
		_ = w.Store.UpdateDelivery(ctx, webhooks.DeliveryUpdate{
			ID: delivery.ID, Status: webhooks.StatusSkipped, AttemptCount: delivery.AttemptCount + 1,
			Reason: "endpoint_disabled",
		})
		return nil
	}

	attempt := delivery.AttemptCount + 1
	if outbox.ExhaustedAttempts(attempt, webhooks.DefaultMaxAttempts) {
		_ = w.Store.UpdateDelivery(ctx, webhooks.DeliveryUpdate{
			ID: delivery.ID, Status: webhooks.StatusDead, AttemptCount: attempt,
			Reason: "max_attempts", ErrorSummary: "delivery attempt budget exhausted",
		})
		return nil
	}
	if err := w.Store.UpdateDelivery(ctx, webhooks.DeliveryUpdate{
		ID: delivery.ID, Status: webhooks.StatusSending, AttemptCount: attempt,
	}); err != nil {
		return err
	}

	// 投递前再校验目标（配置后 DNS 变化 / 历史脏数据）。
	if err := outboundhttp.ValidatePublicURL(endpoint.TargetURL, outboundhttp.Options{AllowHTTP: w.AllowHTTP}); err != nil {
		return w.failPermanent(ctx, delivery.ID, attempt, "ssrf_blocked", "target url is not allowed", 0, "")
	}

	client := w.Client
	if client == nil {
		// 默认使用 SSRF 安全客户端：连接时重解析 IP + 重定向校验。
		client = outboundhttp.NewSafeClient(outboundhttp.Options{
			AllowHTTP: w.AllowHTTP,
			Timeout:   15 * time.Second,
		})
	}
	body := delivery.Payload
	if len(body) == 0 {
		body = []byte(`{}`)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.TargetURL, bytes.NewReader(body))
	if err != nil {
		return w.failPermanent(ctx, delivery.ID, attempt, "request_build_failed", err.Error(), 0, "")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "SForum-Webhooks/1.0")
	req.Header.Set("X-SForum-Event", delivery.EventName)
	req.Header.Set("X-SForum-Delivery", strconv.FormatInt(delivery.ID, 10))
	if delivery.EventID != "" {
		req.Header.Set("X-SForum-Event-Id", delivery.EventID)
	}
	if delivery.CorrelationID != "" {
		req.Header.Set("X-SForum-Correlation-Id", delivery.CorrelationID)
	}
	timestamp := w.now().UTC().Unix()
	req.Header.Set("X-SForum-Timestamp", strconv.FormatInt(timestamp, 10))
	secretPlain := strings.TrimSpace(endpoint.Secret)
	if w.Secrets != nil {
		decrypted, decErr := w.Secrets.DecryptEndpointSecret(endpoint.Secret)
		if decErr != nil {
			return w.failPermanent(ctx, delivery.ID, attempt, "secret_unavailable", "signing secret unavailable", 0, "")
		}
		secretPlain = strings.TrimSpace(decrypted)
		if enc, ok := w.Secrets.MaybeMigrateSecret(endpoint.Secret); ok && w.Migrator != nil {
			_ = w.Migrator.UpdateEndpointSecret(ctx, endpoint.ID, enc)
		}
	}
	if secretPlain != "" {
		req.Header.Set("X-SForum-Signature", signPayload(secretPlain, timestamp, body))
	}

	resp, err := client.Do(req)
	if err != nil {
		// SSRF / 不安全重定向：永久失败，不重试到内网。
		if errors.Is(err, outboundhttp.ErrUnsafeURL) || isUnsafeURLError(err) {
			return w.failPermanent(ctx, delivery.ID, attempt, "ssrf_blocked", "target url is not allowed", 0, "")
		}
		// 网络临时失败：保持 sending，返回 error 让 River 重试。
		_ = w.Store.UpdateDelivery(ctx, webhooks.DeliveryUpdate{
			ID: delivery.ID, Status: webhooks.StatusSending, AttemptCount: attempt,
			Reason: "transport_error", ErrorSummary: truncate(err.Error(), 500),
		})
		return fmt.Errorf("webhook transport: %w", err)
	}
	defer resp.Body.Close()
	snippetBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	snippet := truncate(string(snippetBytes), 500)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return w.Store.UpdateDelivery(ctx, webhooks.DeliveryUpdate{
			ID: delivery.ID, Status: webhooks.StatusSent, AttemptCount: attempt,
			HTTPStatus: resp.StatusCode, ResponseSnippet: snippet,
		})
	}
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		_ = w.Store.UpdateDelivery(ctx, webhooks.DeliveryUpdate{
			ID: delivery.ID, Status: webhooks.StatusSending, AttemptCount: attempt,
			HTTPStatus: resp.StatusCode, ResponseSnippet: snippet,
			Reason: "remote_temporary", ErrorSummary: fmt.Sprintf("http %d", resp.StatusCode),
		})
		return fmt.Errorf("webhook temporary http %d", resp.StatusCode)
	}
	return w.failPermanent(ctx, delivery.ID, attempt, "remote_rejected",
		fmt.Sprintf("http %d", resp.StatusCode), resp.StatusCode, snippet)
}

func (w *DeliverWorker) failPermanent(ctx context.Context, id int64, attempt int, reason, summary string, status int, snippet string) error {
	return w.Store.UpdateDelivery(ctx, webhooks.DeliveryUpdate{
		ID: id, Status: webhooks.StatusFailed, AttemptCount: attempt,
		HTTPStatus: status, ResponseSnippet: snippet, Reason: reason, ErrorSummary: summary,
	})
}

func (w *DeliverWorker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// signPayload 使用 HMAC-SHA256，格式与常见 webhook 兼容：t=<unix>,v1=<hex>。
func signPayload(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%d.", timestamp)
	_, _ = mac.Write(body)
	return "t=" + strconv.FormatInt(timestamp, 10) + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func isUnsafeURLError(err error) bool {
	if err == nil {
		return false
	}
	// net/http 常包装 CheckRedirect / Dial 错误。
	msg := err.Error()
	return strings.Contains(msg, "outboundhttp: unsafe url") || strings.Contains(msg, "ssrf")
}

func Register(registry *supportjobs.Registry, worker *DeliverWorker) {
	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[DeliverArgs](workers, worker)
	})
}
