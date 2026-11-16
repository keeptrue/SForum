package extensionsruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// fakeEventTx 只实现 observe 投递用到的 Commit/Rollback；其余 pgx.Tx 方法嵌入
// nil 接口，一旦被调用会 panic，从而暴露「测试之外还动了别的事务操作」。
type fakeEventTx struct {
	pgx.Tx
	store      *txAwareDeliveryStore
	rows       []extensions.ExtensionEventDelivery
	committed  bool
	rolledBack bool
	commitErr  error
}

func (t *fakeEventTx) Commit(context.Context) error {
	if t.commitErr != nil {
		return t.commitErr
	}
	t.committed = true
	t.store.committed = append(t.store.committed, t.rows...)
	return nil
}

func (t *fakeEventTx) Rollback(context.Context) error {
	if t.committed {
		return nil
	}
	t.rolledBack = true
	return nil
}

// txAwareDeliveryStore 区分「事务内建行」与「非事务建行」：只有 Commit 过的行
// 才算持久化，非事务建行立即持久化（用来证明修复后不再走那条路径）。
type txAwareDeliveryStore struct {
	committed      []extensions.ExtensionEventDelivery
	legacyCreates  int
	updates        []extensions.EventDeliveryUpdateInput
	nextID         int64
	tx             *fakeEventTx
	createErr      error
}

func (s *txAwareDeliveryStore) CreateEventDelivery(_ context.Context, input extensions.EventDeliveryInput) (extensions.ExtensionEventDelivery, error) {
	s.legacyCreates++
	delivery := s.newDelivery(input)
	s.committed = append(s.committed, delivery)
	return delivery, nil
}

func (s *txAwareDeliveryStore) CreateEventDeliveryTx(_ context.Context, tx pgx.Tx, input extensions.EventDeliveryInput) (extensions.ExtensionEventDelivery, error) {
	if s.createErr != nil {
		return extensions.ExtensionEventDelivery{}, s.createErr
	}
	fake, ok := tx.(*fakeEventTx)
	if !ok {
		return extensions.ExtensionEventDelivery{}, errors.New("unexpected tx type")
	}
	delivery := s.newDelivery(input)
	fake.rows = append(fake.rows, delivery)
	return delivery, nil
}

func (s *txAwareDeliveryStore) UpdateEventDelivery(_ context.Context, input extensions.EventDeliveryUpdateInput) error {
	s.updates = append(s.updates, input)
	return nil
}

func (s *txAwareDeliveryStore) newDelivery(input extensions.EventDeliveryInput) extensions.ExtensionEventDelivery {
	s.nextID++
	return extensions.ExtensionEventDelivery{
		ID:            s.nextID,
		ExtensionID:   input.ExtensionID,
		EventName:     input.EventName,
		EventKind:     input.EventKind,
		Status:        input.Status,
		CorrelationID: input.CorrelationID,
	}
}

type fakeTxBeginner struct {
	store    *txAwareDeliveryStore
	beginErr error
}

func (b fakeTxBeginner) Begin(context.Context) (pgx.Tx, error) {
	if b.beginErr != nil {
		return nil, b.beginErr
	}
	tx := &fakeEventTx{store: b.store}
	b.store.tx = tx
	return tx, nil
}

type txRecordingDispatcher struct {
	enqueued   []EventDeliveryArgs
	enqueueErr error
	txEnqueues int
	plainCalls int
}

func (d *txRecordingDispatcher) Enqueue(_ context.Context, args EventDeliveryArgs, _ supportjobs.EnqueueOptions) error {
	d.plainCalls++
	d.enqueued = append(d.enqueued, args)
	return d.enqueueErr
}

func (d *txRecordingDispatcher) EnqueueTx(_ context.Context, _ pgx.Tx, args EventDeliveryArgs, _ supportjobs.EnqueueOptions) error {
	d.txEnqueues++
	if d.enqueueErr != nil {
		return d.enqueueErr
	}
	d.enqueued = append(d.enqueued, args)
	return nil
}

func observeTestManager(t *testing.T, store DeliveryStore, dispatcher EventDispatcher, pool TxBeginner) *Manager {
	t.Helper()
	bus := NewHookBus(HookBusConfig{Invoker: HookInvokerFunc(func(context.Context, extensions.Extension, HookInput) HookResult {
		return HookResult{OK: true}
	})})
	manager := NewManager(ManagerConfig{HookBus: bus, DeliveryStore: store, Dispatcher: dispatcher, Pool: pool})
	extension := runtimeExtension("observe.plugin")
	extension.Manifest.Events = []extensions.ManifestEvent{{Name: appevents.TopicCreated, Kind: appevents.KindObserve}}
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatalf("start: %v", err)
	}
	return manager
}

func observeTestEnvelope() appevents.Envelope {
	return appevents.Envelope{
		Name:          appevents.TopicCreated,
		Kind:          appevents.KindObserve,
		CorrelationID: "corr-atomic",
		Payload:       map[string]any{"topicId": int64(7)},
	}
}

// 正常路径：delivery 行与 job 在同一个事务里提交，job 携带刚建出的行 ID。
func TestObserveDeliveryCommitsRowAndJobInOneTransaction(t *testing.T) {
	store := &txAwareDeliveryStore{}
	dispatcher := &txRecordingDispatcher{}
	manager := observeTestManager(t, store, dispatcher, fakeTxBeginner{store: store})

	result := manager.Emit(context.Background(), observeTestEnvelope())
	if !result.OK {
		t.Fatalf("observe emit failed: %#v", result)
	}
	if store.legacyCreates != 0 {
		t.Fatalf("delivery row was created outside the transaction: %d", store.legacyCreates)
	}
	if len(store.committed) != 1 || store.committed[0].Status != extensions.DeliveryQueued {
		t.Fatalf("committed deliveries = %#v", store.committed)
	}
	if dispatcher.txEnqueues != 1 || dispatcher.plainCalls != 0 {
		t.Fatalf("enqueue path tx=%d plain=%d", dispatcher.txEnqueues, dispatcher.plainCalls)
	}
	if len(dispatcher.enqueued) != 1 || dispatcher.enqueued[0].DeliveryID != store.committed[0].ID {
		t.Fatalf("enqueued = %#v, delivery = %#v", dispatcher.enqueued, store.committed)
	}
	if store.tx == nil || !store.tx.committed || store.tx.rolledBack {
		t.Fatalf("tx state = %#v", store.tx)
	}
	if opts := dispatcher.enqueued[0].EnqueueOptions(); !opts.Unique.ByArgs || opts.MaxAttempts != 3 {
		t.Fatalf("dedupe options changed inside tx: %#v", opts)
	}
}

// ① 入队失败：事务回滚，不留任何 delivery 行，也不去更新一个不存在的行。
func TestObserveDeliveryRollsBackWhenEnqueueTxFails(t *testing.T) {
	store := &txAwareDeliveryStore{}
	dispatcher := &txRecordingDispatcher{enqueueErr: errors.New("river insert failed")}
	manager := observeTestManager(t, store, dispatcher, fakeTxBeginner{store: store})

	result := manager.Emit(context.Background(), observeTestEnvelope())
	if result.OK || result.Reason != "extension.delivery_enqueue_failed" {
		t.Fatalf("expected enqueue failure, got %#v", result)
	}
	if len(store.committed) != 0 {
		t.Fatalf("orphan delivery rows survived rollback: %#v", store.committed)
	}
	if store.legacyCreates != 0 {
		t.Fatalf("delivery row was created outside the transaction: %d", store.legacyCreates)
	}
	if store.tx == nil || !store.tx.rolledBack || store.tx.committed {
		t.Fatalf("tx state = %#v", store.tx)
	}
	if len(store.updates) != 0 {
		t.Fatalf("rolled-back delivery was marked afterwards: %#v", store.updates)
	}
}

// ② 崩溃等价场景：提交失败同样不留孤儿行（只有 job 侧可能重复入队，River 的
// Unique.ByArgs 兜住重复）。
func TestObserveDeliveryLeavesNoOrphanRowWhenCommitFails(t *testing.T) {
	store := &txAwareDeliveryStore{}
	dispatcher := &txRecordingDispatcher{}
	manager := observeTestManager(t, store, dispatcher, fakeTxBeginner{store: store})
	store.tx = nil

	result := manager.Emit(context.Background(), appevents.Envelope{
		Name: appevents.TopicCreated, Kind: appevents.KindObserve, CorrelationID: "corr-commit",
	})
	// 第一次成功提交后，把下一轮的 commit 强制失败。
	if !result.OK || len(store.committed) != 1 {
		t.Fatalf("warm-up emit = %#v deliveries=%#v", result, store.committed)
	}
	failing := &fakeEventTx{store: store, commitErr: errors.New("commit failed")}
	manager.txBeginner = staticTxBeginner{tx: failing}

	result = manager.Emit(context.Background(), appevents.Envelope{
		Name: appevents.TopicCreated, Kind: appevents.KindObserve, CorrelationID: "corr-commit-2",
	})
	if result.OK || result.Reason != "extension.delivery_record_failed" {
		t.Fatalf("expected commit failure, got %#v", result)
	}
	if len(store.committed) != 1 {
		t.Fatalf("commit failure leaked a delivery row: %#v", store.committed)
	}
	if failing.committed || !failing.rolledBack {
		t.Fatalf("tx state = %#v", failing)
	}
}

// ③ 未配置事务边界时显式失败，不悄悄退回两次独立往返。
func TestObserveDeliveryRequiresTransactionalPool(t *testing.T) {
	store := &txAwareDeliveryStore{}
	dispatcher := &txRecordingDispatcher{}
	manager := observeTestManager(t, store, dispatcher, nil)

	result := manager.Emit(context.Background(), observeTestEnvelope())
	if result.OK || result.Reason != "extension.delivery_atomic_unavailable" {
		t.Fatalf("expected atomic-unavailable failure, got %#v", result)
	}
	if len(store.committed) != 0 || store.legacyCreates != 0 || len(dispatcher.enqueued) != 0 {
		t.Fatalf("delivery escaped atomicity: rows=%#v legacy=%d jobs=%#v", store.committed, store.legacyCreates, dispatcher.enqueued)
	}
}

// 建行失败（事务内）同样不留行、不入队。
func TestObserveDeliverySkipsEnqueueWhenRecordFails(t *testing.T) {
	store := &txAwareDeliveryStore{createErr: errors.New("insert failed")}
	dispatcher := &txRecordingDispatcher{}
	manager := observeTestManager(t, store, dispatcher, fakeTxBeginner{store: store})

	result := manager.Emit(context.Background(), observeTestEnvelope())
	if result.OK || result.Reason != "extension.delivery_record_failed" {
		t.Fatalf("expected record failure, got %#v", result)
	}
	if len(store.committed) != 0 || dispatcher.txEnqueues != 0 || len(dispatcher.enqueued) != 0 {
		t.Fatalf("record failure still enqueued: rows=%#v tx=%d jobs=%#v", store.committed, dispatcher.txEnqueues, dispatcher.enqueued)
	}
}

// F4：事务起不来时返回 delivery_record_failed，不 panic、不建行、不入队。
func TestObserveDeliveryFailsWhenTransactionCannotBegin(t *testing.T) {
	store := &txAwareDeliveryStore{}
	dispatcher := &txRecordingDispatcher{}
	manager := observeTestManager(t, store, dispatcher, fakeTxBeginner{
		store:    store,
		beginErr: errors.New("pool exhausted"),
	})

	result := manager.Emit(context.Background(), observeTestEnvelope())
	if result.OK || result.Reason != "extension.delivery_record_failed" {
		t.Fatalf("expected begin failure surfaced as delivery_record_failed, got %#v", result)
	}
	if len(store.committed) != 0 || store.legacyCreates != 0 {
		t.Fatalf("begin failure still wrote a delivery row: rows=%#v legacy=%d", store.committed, store.legacyCreates)
	}
	if dispatcher.txEnqueues != 0 || len(dispatcher.enqueued) != 0 {
		t.Fatalf("begin failure still enqueued: tx=%d jobs=%#v", dispatcher.txEnqueues, dispatcher.enqueued)
	}
}

// 无投递账本（只有队列）时保持既有行为：单次入队，DeliveryID 为 0。
func TestObserveDeliveryWithoutStoreEnqueuesOnce(t *testing.T) {
	dispatcher := &txRecordingDispatcher{}
	manager := observeTestManager(t, nil, dispatcher, nil)

	result := manager.Emit(context.Background(), observeTestEnvelope())
	if !result.OK {
		t.Fatalf("observe emit failed: %#v", result)
	}
	if dispatcher.plainCalls != 1 || len(dispatcher.enqueued) != 1 || dispatcher.enqueued[0].DeliveryID != 0 {
		t.Fatalf("enqueued = %#v (plain=%d)", dispatcher.enqueued, dispatcher.plainCalls)
	}
}

type staticTxBeginner struct{ tx pgx.Tx }

func (b staticTxBeginner) Begin(context.Context) (pgx.Tx, error) {
	return b.tx, nil
}
