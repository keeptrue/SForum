package webhookscontroller

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
)

// 入站 gateway 是本仓唯一「无鉴权 + 主动读请求体」的公开写端点（RV-06）。
// 它必须按自己的上限读流，而不是把全局 64MB body limit 交给匿名请求决定内存峰值。
func TestInboundRejectsBodyOverInboundLimit(t *testing.T) {
	app := newInboundTestApp()
	payload := bytes.Repeat([]byte("a"), int(MaxInboundBodyBytes)+1)

	resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/inbound/github", bytes.NewReader(payload)))
	if err != nil {
		t.Fatalf("request inbound webhook: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusRequestEntityTooLarge {
		t.Fatalf("oversized inbound body status = %d, want %d", resp.StatusCode, fiber.StatusRequestEntityTooLarge)
	}
}

// 恰好在上限内的请求体必须照常回执，且 bytes 反映真实读取长度（不是全局 limit）。
func TestInboundAcceptsBodyWithinInboundLimit(t *testing.T) {
	app := newInboundTestApp()
	payload := bytes.Repeat([]byte("a"), int(MaxInboundBodyBytes))

	resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/inbound/github", bytes.NewReader(payload)))
	if err != nil {
		t.Fatalf("request inbound webhook: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read inbound response: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("inbound status = %d body=%s, want 200", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"bytes":1048576`)) {
		t.Fatalf("inbound receipt does not report the streamed body size: %s", body)
	}
}

// 普通小请求体必须照常回执：流式读取不能把「有 body」误判成空。
func TestInboundAcceptsSmallBody(t *testing.T) {
	app := newInboundTestApp()
	resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/inbound/github", bytes.NewReader([]byte(`{"ping":true}`))))
	if err != nil {
		t.Fatalf("request inbound webhook: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read inbound response: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("inbound status = %d body=%s, want 200", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"bytes":13`)) {
		t.Fatalf("inbound receipt = %s, want bytes 13", body)
	}
}

// 请求体已被上游预读（例如该路径被插件计划包裹）时，端点仍必须能回执：
// 此时 BodyStream 已关闭，只能退回已缓冲切片，但绝不能误报 inbound_empty。
func TestInboundAcceptsPrebufferedBody(t *testing.T) {
	app := fiber.New(fiber.Config{BodyLimit: 64 * 1024 * 1024, StreamRequestBody: true})
	app.Use(func(c fiber.Ctx) error {
		_ = c.Request().Body() // 模拟 dispatcher/插件计划提前读体
		return c.Next()
	})
	NewController(nil, nil, nil).RegisterRoutes(app.Group("/api/v1"))

	resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/inbound/github", bytes.NewReader([]byte(`{"ping":true}`))))
	if err != nil {
		t.Fatalf("request inbound webhook: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("prebuffered inbound status = %d body=%s, want 200", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"bytes":13`)) {
		t.Fatalf("prebuffered inbound receipt = %s, want bytes 13", body)
	}
}

// 这条盯住「不全量 materialize」本身// 这条盯住「不全量 materialize」本身：消费者必须在 limit+1 字节处停下。
// 去掉 io.LimitReader 后，read 计数会等于整个 payload 长度。
func TestCountInboundBodyStopsAtLimitPlusOne(t *testing.T) {
	payload := bytes.Repeat([]byte("a"), int(MaxInboundBodyBytes)+4096)
	reader := &countingReader{reader: bytes.NewReader(payload)}

	n, tooLarge, err := countInboundBody(reader, MaxInboundBodyBytes)
	if err != nil {
		t.Fatalf("count inbound body: %v", err)
	}
	if !tooLarge {
		t.Fatalf("body of %d bytes against a %d-byte limit was not rejected", len(payload), MaxInboundBodyBytes)
	}
	if n != MaxInboundBodyBytes+1 {
		t.Fatalf("counted %d bytes, want limit+1 (%d)", n, MaxInboundBodyBytes+1)
	}
	if reader.consumed > MaxInboundBodyBytes+1 {
		t.Fatalf("consumer read %d bytes for a %d-byte limit; it must stop at limit+1", reader.consumed, MaxInboundBodyBytes)
	}
}

func newInboundTestApp() *fiber.App {
	app := fiber.New(fiber.Config{
		// 与生产同值：Fiber 全局上限仍是 64MB，入站端点必须自己收口。
		BodyLimit:         64 * 1024 * 1024,
		StreamRequestBody: true,
	})
	NewController(nil, nil, nil).RegisterRoutes(app.Group("/api/v1"))
	return app
}

type countingReader struct {
	reader   io.Reader
	consumed int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.consumed += int64(n)
	return n, err
}
