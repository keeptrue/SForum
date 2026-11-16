package mailcontroller

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// P2-6 的重放端点：allowed / denied / 状态不允许 / 入队失败四条结论。
//
// 授权在 handler 自己（Controller.actor），所以 denied 与匿名都不需要 store；
// allowed 用例用最小 fake 证明「重置 + 恰好入队一次」。

type replayDeliveryStore struct {
	notifications.Store
	delivery notifications.MailDelivery
	err      error
	calls    int
	// compensated 记录补偿调用：入队失败后必须把刚重置成 queued 的行改回 failed。
	compensated   bool
	compensatedID int64
	compensateErr error
}

func (s *replayDeliveryStore) FailReplayDelivery(_ context.Context, id int64, _ string) error {
	s.compensated = true
	s.compensatedID = id
	return s.compensateErr
}

func (s *replayDeliveryStore) ReplayDelivery(_ context.Context, id int64) (notifications.MailDelivery, error) {
	s.calls++
	if s.err != nil {
		return notifications.MailDelivery{}, s.err
	}
	if id != s.delivery.ID {
		return notifications.MailDelivery{}, notifications.ErrDeliveryNotFound
	}
	return s.delivery, nil
}

type replayEnqueuer struct {
	calls int
	args  []river.JobArgs
	err   error
}

func (e *replayEnqueuer) Enqueue(_ context.Context, args river.JobArgs, _ supportjobs.EnqueueOptions) (*rivertype.JobInsertResult, error) {
	e.calls++
	e.args = append(e.args, args)
	return &rivertype.JobInsertResult{}, e.err
}

func newReplayApp(t *testing.T, actor identity.Actor, store *replayDeliveryStore, enqueuer *replayEnqueuer) *fiber.App {
	t.Helper()
	app := fiber.New()
	// 与 permission_test.go 同构：WithAuth 提供 userID，actor store 提供权限投影。
	controller := NewController(nil, store, nil, mailPermissionActors{actor: actor}, nil, nil, nil)
	if enqueuer != nil {
		controller.WithDeliveryEnqueuer(enqueuer)
	}
	app.Use(func(c fiber.Ctx) error {
		if actor.ID > 0 {
			c.SetContext(apitokens.WithAuth(c.Context(), apitokens.Authenticated{UserID: actor.ID}))
		}
		return c.Next()
	})
	controller.RegisterRoutes(app.Group("/api/v1"))
	return app
}

func mailManager() identity.Actor {
	return identity.Actor{ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionSettingsMailManage: true,
	}}
}

func TestAdminReplayMailDeliveryMatrix(t *testing.T) {
	failed := notifications.MailDelivery{ID: 42, Status: notifications.DeliveryQueued}

	t.Run("failed delivery is reset and enqueued exactly once", func(t *testing.T) {
		store := &replayDeliveryStore{delivery: failed}
		enqueuer := &replayEnqueuer{}
		app := newReplayApp(t, mailManager(), store, enqueuer)

		status, body := mailPermissionCall(t, app, http.MethodPost, "/api/v1/admin/mail/deliveries/42/replay", "")
		if status != http.StatusOK {
			t.Fatalf("status=%d want 200 body=%s", status, body)
		}
		if store.calls != 1 {
			t.Fatalf("ReplayDelivery calls=%d want 1", store.calls)
		}
		if enqueuer.calls != 1 {
			t.Fatalf("enqueue calls=%d want 1（只入队一次）", enqueuer.calls)
		}
	})

	t.Run("non-replayable state is 409 and enqueues nothing", func(t *testing.T) {
		store := &replayDeliveryStore{err: notifications.ErrDeliveryNotReplayable}
		enqueuer := &replayEnqueuer{}
		app := newReplayApp(t, mailManager(), store, enqueuer)

		status, body := mailPermissionCall(t, app, http.MethodPost, "/api/v1/admin/mail/deliveries/42/replay", "")
		if status != http.StatusConflict {
			t.Fatalf("status=%d want 409 body=%s", status, body)
		}
		if enqueuer.calls != 0 {
			t.Fatalf("enqueue calls=%d want 0", enqueuer.calls)
		}
	})

	t.Run("unknown delivery is 404", func(t *testing.T) {
		store := &replayDeliveryStore{err: notifications.ErrDeliveryNotFound}
		app := newReplayApp(t, mailManager(), store, &replayEnqueuer{})

		status, body := mailPermissionCall(t, app, http.MethodPost, "/api/v1/admin/mail/deliveries/99/replay", "")
		if status != http.StatusNotFound {
			t.Fatalf("status=%d want 404 body=%s", status, body)
		}
	})

	t.Run("missing mail permission is 403 and never reaches the store", func(t *testing.T) {
		store := &replayDeliveryStore{delivery: failed}
		app := newReplayApp(t, identity.Actor{ID: 8, Status: identity.UserStatusActive}, store, &replayEnqueuer{})

		status, body := mailPermissionCall(t, app, http.MethodPost, "/api/v1/admin/mail/deliveries/42/replay", "")
		if status != http.StatusForbidden {
			t.Fatalf("status=%d want 403 body=%s", status, body)
		}
		if store.calls != 0 {
			t.Fatalf("ReplayDelivery calls=%d want 0（门在触库之前）", store.calls)
		}
	})

	t.Run("anonymous is 401", func(t *testing.T) {
		app := newReplayApp(t, identity.Actor{}, &replayDeliveryStore{delivery: failed}, &replayEnqueuer{})
		status, body := mailPermissionCall(t, app, http.MethodPost, "/api/v1/admin/mail/deliveries/42/replay", "")
		if status != http.StatusUnauthorized {
			t.Fatalf("status=%d want 401 body=%s", status, body)
		}
	})

	t.Run("enqueuer unavailable fails closed", func(t *testing.T) {
		store := &replayDeliveryStore{delivery: failed}
		app := newReplayApp(t, mailManager(), store, nil)
		status, body := mailPermissionCall(t, app, http.MethodPost, "/api/v1/admin/mail/deliveries/42/replay", "")
		if status != http.StatusServiceUnavailable {
			t.Fatalf("status=%d want 503 body=%s", status, body)
		}
		if store.calls != 0 {
			t.Fatalf("ReplayDelivery calls=%d want 0（没有入队能力时不能先重置状态）", store.calls)
		}
	})

	t.Run("enqueue failure surfaces 503 and compensates the row back to failed", func(t *testing.T) {
		store := &replayDeliveryStore{delivery: failed}
		enqueuer := &replayEnqueuer{err: errors.New("river unavailable")}
		app := newReplayApp(t, mailManager(), store, enqueuer)
		status, body := mailPermissionCall(t, app, http.MethodPost, "/api/v1/admin/mail/deliveries/42/replay", "")
		if status != http.StatusServiceUnavailable {
			t.Fatalf("status=%d want 503 body=%s", status, body)
		}
		// 只断言 503 是不够的：行已经被重置成 queued，没有 job 会永久停在半路。
		if !store.compensated || store.compensatedID != 42 {
			t.Fatalf("入队失败后没有补偿回 failed：compensated=%v id=%d", store.compensated, store.compensatedID)
		}
	})

	t.Run("compensation failure still surfaces 503 without panicking", func(t *testing.T) {
		store := &replayDeliveryStore{delivery: failed, compensateErr: errors.New("store down")}
		enqueuer := &replayEnqueuer{err: errors.New("river unavailable")}
		app := newReplayApp(t, mailManager(), store, enqueuer)
		status, body := mailPermissionCall(t, app, http.MethodPost, "/api/v1/admin/mail/deliveries/42/replay", "")
		if status != http.StatusServiceUnavailable {
			t.Fatalf("status=%d want 503 body=%s", status, body)
		}
		if !store.compensated {
			t.Fatal("补偿失败也要尝试过补偿（sweeper 是最后兜底，但必须留下尝试痕迹）")
		}
	})
}
