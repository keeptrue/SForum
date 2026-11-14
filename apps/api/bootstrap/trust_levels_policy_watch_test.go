package bootstrap

import (
	"context"
	"errors"
	"testing"

	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

// fakeOptionStore 记录写入并可按需失败。
type fakeOptionStore struct {
	upserts  []string
	batches  int
	failWith error
}

func (s *fakeOptionStore) List(context.Context) ([]options.Option, error) {
	return []options.Option{{Name: options.NameSiteName, Value: "forum"}}, nil
}

func (s *fakeOptionStore) InsertMissing(_ context.Context, input options.UpdateInput) error {
	s.upserts = append(s.upserts, input.Name)
	return s.failWith
}

func (s *fakeOptionStore) Upsert(_ context.Context, input options.UpdateInput) (options.Option, error) {
	if s.failWith != nil {
		return options.Option{}, s.failWith
	}
	s.upserts = append(s.upserts, input.Name)
	return options.Option{Name: input.Name, Value: input.Value}, nil
}

func (s *fakeOptionStore) UpsertMany(_ context.Context, inputs []options.UpdateInput) ([]options.Option, error) {
	if s.failWith != nil {
		return nil, s.failWith
	}
	s.batches++
	updated := make([]options.Option, 0, len(inputs))
	for _, input := range inputs {
		s.upserts = append(s.upserts, input.Name)
		updated = append(updated, options.Option{Name: input.Name, Value: input.Value})
	}
	return updated, nil
}

// TestTrustPolicyWriteHookEnqueuesOnlyForTrustPolicyKeys 是这条装饰器的判据。
//
// 两半都要红得出来：改了信任策略必须入队；改了别的键**不得**入队（全员重算
// 是昂贵动作，不能让设置页任意一次保存带出来）。
func TestTrustPolicyWriteHookEnqueuesOnlyForTrustPolicyKeys(t *testing.T) {
	store := &fakeOptionStore{}
	hook := newTrustPolicyWriteHook(store, nil)
	enqueued := 0
	hook.Bind(func(context.Context) error {
		enqueued++
		return nil
	})

	if _, err := hook.Upsert(context.Background(), options.UpdateInput{Name: options.NameSiteName, Value: "x"}); err != nil {
		t.Fatal(err)
	}
	if enqueued != 0 {
		t.Fatalf("改 site.name 不该入队，实际 %d 次", enqueued)
	}

	if _, err := hook.Upsert(context.Background(), options.UpdateInput{Name: options.NameTrustLevels, Value: `{"schemaVersion":2,"tiers":[]}`}); err != nil {
		t.Fatal(err)
	}
	if enqueued != 1 {
		t.Fatalf("改 trust.levels 必须入队一次，实际 %d 次", enqueued)
	}

	// 批量路径：只有含信任策略键才入队，其它键的批量写不入队。
	if _, err := hook.UpsertMany(context.Background(), []options.UpdateInput{
		{Name: options.NameSiteName, Value: "y"},
		{Name: options.NameTrustNewUserDays, Value: "7"},
	}); err != nil {
		t.Fatal(err)
	}
	if enqueued != 2 {
		t.Fatalf("批量含 trust.new_user_days 必须再入队一次，实际 %d 次", enqueued)
	}
	if _, err := hook.UpsertMany(context.Background(), []options.UpdateInput{
		{Name: options.NameSiteName, Value: "z"},
	}); err != nil {
		t.Fatal(err)
	}
	if enqueued != 2 {
		t.Fatalf("批量只含 site.name 不该入队，实际 %d 次", enqueued)
	}
	// 批量路径必须真的走内层 UpsertMany，而不是退化成逐条 Upsert。
	if store.batches != 2 {
		t.Fatalf("内层 UpsertMany 被调用 %d 次，期望 2（转发丢了会静默退化）", store.batches)
	}
}

// TestTrustPolicyWriteHookKeepsStoreErrors 钉住「写失败不入队、错误原样返回」。
func TestTrustPolicyWriteHookKeepsStoreErrors(t *testing.T) {
	store := &fakeOptionStore{failWith: errors.New("db down")}
	hook := newTrustPolicyWriteHook(store, nil)
	enqueued := 0
	hook.Bind(func(context.Context) error {
		enqueued++
		return nil
	})

	if _, err := hook.Upsert(context.Background(), options.UpdateInput{Name: options.NameTrustLevels}); err == nil {
		t.Fatal("store 失败必须原样返回")
	}
	if _, err := hook.UpsertMany(context.Background(), []options.UpdateInput{{Name: options.NameTrustLevels}}); err == nil {
		t.Fatal("store 批量失败必须原样返回")
	}
	if enqueued != 0 {
		t.Fatalf("写失败不该入队，实际 %d 次", enqueued)
	}
}
