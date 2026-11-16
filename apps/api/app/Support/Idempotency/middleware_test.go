package idempotency

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/recover"
)

func TestMiddlewareReplaysSuccessfulResponse(t *testing.T) {
	store := NewStore(NewMemoryBackend(), DefaultTTL)
	app := fiber.New()
	var calls int
	app.Post("/topics", Middleware(store, func(fiber.Ctx) (int64, error) { return 7, nil }), func(c fiber.Ctx) error {
		calls++
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"id": 42})
	})

	first := postWithKey(t, app, "k-1")
	if first.StatusCode != fiber.StatusCreated {
		t.Fatalf("first status=%d", first.StatusCode)
	}
	if first.Header.Get(ReplayedHeader) != "" {
		t.Fatal("first response should not be replayed")
	}
	body1, _ := io.ReadAll(first.Body)
	_ = first.Body.Close()

	second := postWithKey(t, app, "k-1")
	if second.StatusCode != fiber.StatusCreated {
		t.Fatalf("second status=%d", second.StatusCode)
	}
	if second.Header.Get(ReplayedHeader) != "true" {
		t.Fatal("expected replayed header")
	}
	body2, _ := io.ReadAll(second.Body)
	_ = second.Body.Close()
	if string(body1) != string(body2) {
		t.Fatalf("body mismatch: %s vs %s", body1, body2)
	}
	if calls != 1 {
		t.Fatalf("handler calls=%d want 1", calls)
	}
}

func TestMiddlewareDoesNotCacheError(t *testing.T) {
	store := NewStore(NewMemoryBackend(), DefaultTTL)
	app := fiber.New()
	var calls int
	app.Post("/topics", Middleware(store, func(fiber.Ctx) (int64, error) { return 1, nil }), func(c fiber.Ctx) error {
		calls++
		if calls == 1 {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"err": true})
		}
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"ok": true})
	})

	first := postWithKey(t, app, "retry-me")
	_ = first.Body.Close()
	if first.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("first=%d", first.StatusCode)
	}
	second := postWithKey(t, app, "retry-me")
	_ = second.Body.Close()
	if second.StatusCode != fiber.StatusCreated {
		t.Fatalf("second=%d want created after retry", second.StatusCode)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestMiddlewareConflictOnInFlight(t *testing.T) {
	store := NewStore(NewMemoryBackend(), DefaultTTL)
	// 手动占用 pending
	key := StorageKey(3, http.MethodPost, "/topics", "inflight")
	_, started, _, err := store.Begin(t.Context(), key)
	if err != nil || !started {
		t.Fatalf("begin: started=%v err=%v", started, err)
	}

	app := fiber.New()
	app.Post("/topics", Middleware(store, func(fiber.Ctx) (int64, error) { return 3, nil }), func(c fiber.Ctx) error {
		return c.SendStatus(fiber.StatusCreated)
	})
	resp := postWithKey(t, app, "inflight")
	_ = resp.Body.Close()
	if resp.StatusCode != fiber.StatusConflict {
		t.Fatalf("status=%d want 409", resp.StatusCode)
	}
}

// TestMiddlewarePanicReleasesPendingLease 是 P2-4 的 panic 兜底断言。
//
// 处理器 panic 时中间件正常路径的 Complete/Abort 都到不了；没有 defer 兜底的话
// pending 只能等 90s 短租约自愈，客户端在这段时间里用同一个 key 重试只会拿到 409。
// 这里用真实的 fiber recover 中间件复现生产装配：panic → 500，紧接着同一 key 的
// 重试必须重新拿到执行权（201），而不是 409。
//
// 变异要求：删掉 middleware 里的 defer 兜底，第二次请求会变成 409。
func TestMiddlewarePanicReleasesPendingLease(t *testing.T) {
	store := NewStore(NewMemoryBackend(), DefaultTTL)
	app := fiber.New()
	var calls int
	app.Post("/topics",
		recover.New(),
		Middleware(store, func(fiber.Ctx) (int64, error) { return 9, nil }),
		func(c fiber.Ctx) error {
			calls++
			if calls == 1 {
				panic("handler exploded")
			}
			return c.Status(fiber.StatusCreated).JSON(fiber.Map{"ok": true})
		})

	first := postWithKey(t, app, "panic-retry")
	_ = first.Body.Close()
	if first.StatusCode != fiber.StatusInternalServerError {
		t.Fatalf("panicking handler status=%d want 500", first.StatusCode)
	}

	second := postWithKey(t, app, "panic-retry")
	_ = second.Body.Close()
	if second.StatusCode != fiber.StatusCreated {
		t.Fatalf("retry after panic status=%d want 201 (pending 没被释放就会是 409)", second.StatusCode)
	}
	if calls != 2 {
		t.Fatalf("handler calls=%d want 2", calls)
	}
}

func TestValidateKey(t *testing.T) {
	if ValidateKey("") == nil {
		t.Fatal("empty")
	}
	if ValidateKey(string(make([]byte, MaxKeyLength+1))) == nil {
		t.Fatal("too long")
	}
	if ValidateKey("ok-key_1") != nil {
		t.Fatal("valid key rejected")
	}
}

func postWithKey(t *testing.T, app *fiber.App, key string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/topics", nil)
	req.Header.Set(HeaderName, key)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
