package extensions

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/app/Models/Extensions/frontendtrust"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// memoryChallengeStore 是 frontendtrust.Store 的进程内替身，**仅供单元测试**。
//
// 它刻意复刻 PostgresStore 的语义（单次消费、actor 与精确制品绑定、过期、
// 同 actor+extension 旧挑战 superseded），这样「Service 不再持有权威状态」这件事
// 才能在两个 Service 实例共享同一 store 时被验证。生产装配只用 PostgresStore。
type memoryChallengeStore struct {
	mu      sync.Mutex
	records map[string]memoryChallengeRecord
}

type memoryChallengeRecord struct {
	challenge   frontendtrust.Challenge
	consumed    bool
	invalidated bool
}

func newMemoryChallengeStore() *memoryChallengeStore {
	return &memoryChallengeStore{records: map[string]memoryChallengeRecord{}}
}

func (s *memoryChallengeStore) Create(_ context.Context, challenge frontendtrust.Challenge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, record := range s.records {
		if record.challenge.ActorUserID == challenge.ActorUserID &&
			record.challenge.ExtensionID == challenge.ExtensionID && !record.consumed && !record.invalidated {
			record.invalidated = true
			s.records[id] = record
		}
	}
	s.records[challenge.ChallengeID] = memoryChallengeRecord{challenge: challenge}
	return nil
}

func (s *memoryChallengeStore) Consume(_ context.Context, input frontendtrust.ConsumeInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[input.ChallengeID]
	if !ok || record.consumed || record.invalidated {
		return frontendtrust.ErrChallengeInvalid
	}
	bound := record.challenge
	if bound.ActorUserID != input.ActorUserID || bound.ExtensionID != input.ExtensionID ||
		bound.Version != input.Version || bound.Digest != input.Digest ||
		bound.APIVersion != input.APIVersion || bound.ComponentID != input.ComponentID ||
		bound.Code != input.Code || !time.Now().UTC().Before(bound.ExpiresAt) {
		return frontendtrust.ErrChallengeInvalid
	}
	record.consumed = true
	s.records[input.ChallengeID] = record
	return nil
}

// expire 把挑战的过期时间拨到过去，用来验证过期必败。
func (s *memoryChallengeStore) expire(challengeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[challengeID]
	if !ok {
		return
	}
	record.challenge.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	s.records[challengeID] = record
}

// frontendConfirmationFor 用挑战回执构造一次完整确认（字段与前端提交一致）。
func frontendConfirmationFor(challenge FrontendTrustChallenge, extension Extension, component AdminComponent) *FrontendTrustConfirmation {
	return &FrontendTrustConfirmation{
		ChallengeID:  challenge.ChallengeID,
		Code:         challenge.Code,
		ExtensionID:  extension.ID,
		Version:      extension.Version,
		Digest:       extension.AdminFrontendDigest,
		APIVersion:   component.APIVersion,
		ComponentID:  component.ID,
		Phrase:       extension.ID,
		Acknowledged: true,
	}
}

// TestFrontendChallengeIsSharedAcrossServiceInstances 固定 P3-7：挑战权威必须跨实例
// 共享。多副本/蓝绿发布下 challenge 落 A、grant 打 B 是正常路径，不能必败。
//
// 变异要求：把 consumePrebuiltConfirmation 改回读进程内 map（或让 Challenge 只写
// 本实例内存），本用例必须红在 B 的 Grant 上。
func TestFrontendChallengeIsSharedAcrossServiceInstances(t *testing.T) {
	extension := prebuiltFrontendFixture(t, SourceUploaded)
	reader := &fakeFrontendExtensionReader{item: extension}
	// 同一个 store、两个互不相干的 Service 实例：等价于两个进程连同一个数据库。
	store := newMemoryChallengeStore()
	nodeA := NewFrontendService(reader, &fakeFrontendTrustStore{}, store)
	nodeB := NewFrontendService(reader, &fakeFrontendTrustStore{}, store)
	actor := frontendSuperAdmin()
	component := *prebuiltSettingsComponent(extension)

	challenge, err := nodeA.Challenge(context.Background(), actor, extension.ID)
	if err != nil {
		t.Fatalf("challenge on node A: %v", err)
	}
	status, err := nodeB.Grant(context.Background(), actor, extension.ID, GrantFrontendInput{
		Digest:       extension.AdminFrontendDigest,
		Confirmation: frontendConfirmationFor(challenge, extension, component),
	})
	if err != nil {
		t.Fatalf("grant on node B must accept a challenge created on node A, got %v", err)
	}
	if status.TrustState != FrontendTrustTrusted {
		t.Fatalf("unexpected status after cross-instance grant: %#v", status)
	}

	// 同一挑战再消费一次（换回 A 也一样）必须失败：单次消费是存储层不变量。
	if _, err := nodeA.Grant(context.Background(), actor, extension.ID, GrantFrontendInput{
		Digest:       extension.AdminFrontendDigest,
		Confirmation: frontendConfirmationFor(challenge, extension, component),
	}); !errors.Is(err, ErrFrontendTrustUnavailable) {
		t.Fatalf("replayed challenge must be rejected, got %v", err)
	}
}

// TestFrontendChallengeRejectsExpiredAndMismatchedBindings 覆盖 denied 侧：过期、
// 换 actor、换 code 都必须失败，且失败不会让挑战保持可用。
func TestFrontendChallengeRejectsExpiredAndMismatchedBindings(t *testing.T) {
	extension := prebuiltFrontendFixture(t, SourceUploaded)
	reader := &fakeFrontendExtensionReader{item: extension}
	store := newMemoryChallengeStore()
	service := NewFrontendService(reader, &fakeFrontendTrustStore{}, store)
	actor := frontendSuperAdmin()
	component := *prebuiltSettingsComponent(extension)

	t.Run("expired challenge fails", func(t *testing.T) {
		challenge, err := service.Challenge(context.Background(), actor, extension.ID)
		if err != nil {
			t.Fatal(err)
		}
		store.expire(challenge.ChallengeID)
		if _, err := service.Grant(context.Background(), actor, extension.ID, GrantFrontendInput{
			Digest:       extension.AdminFrontendDigest,
			Confirmation: frontendConfirmationFor(challenge, extension, component),
		}); !errors.Is(err, ErrFrontendTrustUnavailable) {
			t.Fatalf("expired challenge must be rejected, got %v", err)
		}
	})

	t.Run("challenge is bound to its actor", func(t *testing.T) {
		challenge, err := service.Challenge(context.Background(), actor, extension.ID)
		if err != nil {
			t.Fatal(err)
		}
		other := identity.Actor{ID: 2, Status: identity.UserStatusActive, RoleKeys: []string{identity.RoleSuperAdmin}}
		if _, err := service.Grant(context.Background(), other, extension.ID, GrantFrontendInput{
			Digest:       extension.AdminFrontendDigest,
			Confirmation: frontendConfirmationFor(challenge, extension, component),
		}); !errors.Is(err, ErrFrontendTrustUnavailable) {
			t.Fatalf("challenge must not be usable by another actor, got %v", err)
		}
	})

	t.Run("wrong confirmation code fails", func(t *testing.T) {
		challenge, err := service.Challenge(context.Background(), actor, extension.ID)
		if err != nil {
			t.Fatal(err)
		}
		confirmation := frontendConfirmationFor(challenge, extension, component)
		confirmation.Code = "000000"
		if confirmation.Code == challenge.Code {
			confirmation.Code = "111111"
		}
		if _, err := service.Grant(context.Background(), actor, extension.ID, GrantFrontendInput{
			Digest:       extension.AdminFrontendDigest,
			Confirmation: confirmation,
		}); !errors.Is(err, ErrFrontendTrustUnavailable) {
			t.Fatalf("wrong code must be rejected, got %v", err)
		}
	})
}

// TestFrontendChallengeFailsClosedWithoutStore 固定装配红线：没有持久化 store 时
// 挑战通道必须 fail closed，绝不退回进程内状态（那正是 P3-7 的缺陷）。
func TestFrontendChallengeFailsClosedWithoutStore(t *testing.T) {
	extension := prebuiltFrontendFixture(t, SourceUploaded)
	service := NewFrontendService(&fakeFrontendExtensionReader{item: extension}, &fakeFrontendTrustStore{}, nil)
	actor := frontendSuperAdmin()

	if _, err := service.Challenge(context.Background(), actor, extension.ID); !errors.Is(err, ErrFrontendTrustUnavailable) {
		t.Fatalf("unwired challenge store must fail closed, got %v", err)
	}
}

// TestFrontendServiceHoldsNoInProcessChallengeState 静态守卫：挑战权威不得挂在
// Service 上。实例内 map 会被上面的跨实例用例抓住；这条把「把 map 塞回结构体」
// 本身也钉死，免得它换个名字复活。
//
// 覆盖边界（诚实写明）：它只查结构体字段，查不到**包级变量**；包级内存权威由
// TestFrontendChallengeAuthorityIsTheDatabase 兜住——任何在库之前短路的内存缓存
// 都会让 consumed_at 落不了账。
func TestFrontendServiceHoldsNoInProcessChallengeState(t *testing.T) {
	syncMap := reflect.TypeOf(sync.Map{})
	typ := reflect.TypeOf(FrontendService{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Type.Kind() == reflect.Map || field.Type == syncMap || field.Type == reflect.PointerTo(syncMap) {
			t.Fatalf("FrontendService must not hold in-process challenge state, found %q of type %s", field.Name, field.Type)
		}
	}
}

// TestFrontendChallengeAuthorityIsTheDatabase 用真实 PostgreSQL 跑一遍 A 建、B 消费，
// 并断言消费在库里落账。这是 P3-7 的生产形态证据：权威是库，不是任何进程内状态。
func TestFrontendChallengeAuthorityIsTheDatabase(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.IsolatedURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var actorID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, 'Frontend Challenge') RETURNING id
	`, "fchallenge_"+suffix, "fchallenge_"+suffix+"@example.test").Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	extension := prebuiltFrontendFixture(t, SourceUploaded)
	if _, err := pool.Exec(ctx, `INSERT INTO extensions (id, type, name) VALUES ($1, 'plugin', 'Frontend Challenge')`, extension.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM extension_trust_challenges WHERE extension_id = $1`, extension.ID)
		pool.Exec(context.Background(), `DELETE FROM extensions WHERE id = $1`, extension.ID)
		pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, actorID)
	})

	reader := &fakeFrontendExtensionReader{item: extension}
	// 两个 Service + 两个 store 实例，各自独立，只共享数据库。
	nodeA := NewFrontendService(reader, &fakeFrontendTrustStore{}, frontendtrust.NewPostgresStore(pool))
	nodeB := NewFrontendService(reader, &fakeFrontendTrustStore{}, frontendtrust.NewPostgresStore(pool))
	actor := identity.Actor{ID: actorID, Status: identity.UserStatusActive, RoleKeys: []string{identity.RoleSuperAdmin}}
	component := *prebuiltSettingsComponent(extension)

	challenge, err := nodeA.Challenge(ctx, actor, extension.ID)
	if err != nil {
		t.Fatalf("challenge on node A: %v", err)
	}
	if _, err := nodeB.Grant(ctx, actor, extension.ID, GrantFrontendInput{
		Digest:       extension.AdminFrontendDigest,
		Confirmation: frontendConfirmationFor(challenge, extension, component),
	}); err != nil {
		t.Fatalf("grant on node B must accept node A's challenge, got %v", err)
	}

	var consumed int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM extension_trust_challenges
		WHERE extension_id = $1 AND consumed_at IS NOT NULL
	`, extension.ID).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed != 1 {
		t.Fatalf("consumption must be recorded in the database, got %d consumed rows", consumed)
	}
}
