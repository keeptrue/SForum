package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	pluginsessionstate "github.com/keeptrue/sforum/apps/api/app/Support/PluginSessionState"
	privacy "github.com/keeptrue/sforum/apps/api/app/Support/Privacy"
)

// privacyTestBackend 是本测试需要的全部 Redis 语义（与 HostAPI 侧的替身同形）。
type privacyTestBackend struct {
	mu     sync.Mutex
	values map[string][]byte
	sets   map[string]map[string]bool
}

func newPrivacyTestBackend() *privacyTestBackend {
	return &privacyTestBackend{values: map[string][]byte{}, sets: map[string]map[string]bool{}}
}

func (b *privacyTestBackend) Get(_ context.Context, key string) ([]byte, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	value, ok := b.values[key]
	return append([]byte(nil), value...), ok, nil
}

func (b *privacyTestBackend) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.values[key] = append([]byte(nil), value...)
	return nil
}

func (b *privacyTestBackend) Delete(_ context.Context, key string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.values[key]
	delete(b.values, key)
	delete(b.sets, key)
	return ok, nil
}

func (b *privacyTestBackend) SetAdd(_ context.Context, key string, member string, _ time.Duration) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sets[key] == nil {
		b.sets[key] = map[string]bool{}
	}
	added := !b.sets[key][member]
	b.sets[key][member] = true
	return added, nil
}

func (b *privacyTestBackend) SetMembers(_ context.Context, key string) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	members := make([]string, 0, len(b.sets[key]))
	for member := range b.sets[key] {
		members = append(members, member)
	}
	return members, nil
}

func (b *privacyTestBackend) SetRemove(_ context.Context, key string, members ...string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, member := range members {
		delete(b.sets[key], member)
	}
	return nil
}

// recordingPrivacyAuditor 记录隐私审计事件，供「审计里没有值」的断言使用。
type recordingPrivacyAuditor struct {
	mu     sync.Mutex
	stored []privacy.AuditEvent
}

func (a *recordingPrivacyAuditor) Append(_ context.Context, event privacy.AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stored = append(a.stored, event)
	return nil
}

func (a *recordingPrivacyAuditor) events() []privacy.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]privacy.AuditEvent(nil), a.stored...)
}

// fakeSessionLister 分页返回预设的 sid，并记录被请求过的页。
type fakeSessionLister struct {
	all   []string
	err   error
	mu    sync.Mutex
	pages []int
}

func (l *fakeSessionLister) ListUserSessions(
	_ context.Context,
	_ int64,
	_ string,
	includeHistory bool,
	page int,
	perPage int,
) (identity.SessionListResult, error) {
	if l.err != nil {
		return identity.SessionListResult{}, l.err
	}
	l.mu.Lock()
	l.pages = append(l.pages, page)
	l.mu.Unlock()
	if !includeHistory {
		return identity.SessionListResult{}, errors.New("erase must include revoked history")
	}
	if perPage <= 0 {
		perPage = 10
	}
	start := (page - 1) * perPage
	if start >= len(l.all) {
		return identity.SessionListResult{Total: int64(len(l.all)), Page: page, PerPage: perPage}, nil
	}
	end := min(start+perPage, len(l.all))
	items := make([]identity.SessionRecord, 0, end-start)
	for _, sid := range l.all[start:end] {
		items = append(items, identity.SessionRecord{ID: sid})
	}
	return identity.SessionListResult{
		Items: items, Total: int64(len(l.all)), Page: page, PerPage: perPage,
	}, nil
}

func newPrivacyEraserFixture(t *testing.T, sids []string) (*pluginSessionStateEraser, *pluginsessionstate.Store, *fakeSessionLister, *productionPluginSessionState) {
	t.Helper()
	store, err := pluginsessionstate.NewStore(newPrivacyTestBackend())
	if err != nil {
		t.Fatal(err)
	}
	substrate := &productionPluginSessionState{
		Store: store, installationID: "installation-privacy-test",
	}
	lister := &fakeSessionLister{all: sids}
	return &pluginSessionStateEraser{invalidator: substrate, sessions: lister}, store, lister, substrate
}

func seedPluginSessionState(t *testing.T, store *pluginsessionstate.Store, sid string, value string) {
	t.Helper()
	scope := pluginsessionstate.Scope{
		InstallationID: "installation-privacy-test", ExtensionID: "acme.workflow",
		SessionFingerprint:   pluginsessionstate.Fingerprint(sid),
		SessionIDleExpiresAt: time.Now().Add(time.Hour),
	}
	if _, err := store.Set(context.Background(), scope, "checkout/step", []byte(value), time.Minute); err != nil {
		t.Fatal(err)
	}
}

func pluginSessionStateValue(t *testing.T, store *pluginsessionstate.Store, sid string) ([]byte, bool) {
	t.Helper()
	scope := pluginsessionstate.Scope{
		InstallationID: "installation-privacy-test", ExtensionID: "acme.workflow",
		SessionFingerprint:   pluginsessionstate.Fingerprint(sid),
		SessionIDleExpiresAt: time.Now().Add(time.Hour),
	}
	value, found, err := store.Get(context.Background(), scope, "checkout/step")
	if err != nil {
		t.Fatal(err)
	}
	return value, found
}

// 擦除必须清掉该用户所有会话的插件状态（含已被撤销的历史会话），且不动别人的。
func TestPluginSessionStateEraseRemovesEverySessionNamespace(t *testing.T) {
	sids := []string{"sid-one", "sid-two", "sid-three"}
	eraser, store, lister, _ := newPrivacyEraserFixture(t, sids)
	for _, sid := range sids {
		seedPluginSessionState(t, store, sid, "value-for-"+sid)
	}
	seedPluginSessionState(t, store, "someone-elses-sid", "untouched")

	result, err := eraser.erase(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Erased {
		t.Fatalf("erase did not report success: %#v", result)
	}
	if result.ExtensionID != pluginSessionStatePrivacyContributionID {
		t.Fatalf("contribution id = %q", result.ExtensionID)
	}
	for _, sid := range sids {
		if _, found := pluginSessionStateValue(t, store, sid); found {
			t.Fatalf("session %q still holds plugin state after erase", sid)
		}
	}
	if value, found := pluginSessionStateValue(t, store, "someone-elses-sid"); !found || string(value) != "untouched" {
		t.Fatalf("erase touched another user's session state: %q %t", value, found)
	}
	if len(lister.pages) < 1 {
		t.Fatal("the erase never listed sessions")
	}
	// 报告只含计数，不含任何值。
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("value-for-")) {
		t.Fatalf("erase result leaked a plugin value: %s", encoded)
	}
}

func TestPluginSessionStateEraseFailsClosedOnBadInputAndStorageErrors(t *testing.T) {
	eraser, store, lister, _ := newPrivacyEraserFixture(t, []string{"sid-one"})
	seedPluginSessionState(t, store, "sid-one", "value")

	if _, err := eraser.erase(context.Background(), "not-a-user"); err == nil {
		t.Fatal("a non-numeric user id was accepted")
	}
	if _, found := pluginSessionStateValue(t, store, "sid-one"); !found {
		t.Fatal("an invalid user id erased state anyway")
	}

	lister.err = errors.New("database is down")
	result, err := eraser.erase(context.Background(), "42")
	if err == nil || result.Erased {
		t.Fatalf("a listing failure was reported as success: %#v %v", result, err)
	}
	if strings.Contains(result.Error, "database is down") {
		t.Fatalf("erase result leaked the storage error: %q", result.Error)
	}
	if _, found := pluginSessionStateValue(t, store, "sid-one"); !found {
		t.Fatal("state was erased even though listing failed")
	}

	if _, err := (&pluginSessionStateEraser{}).erase(context.Background(), "42"); err == nil {
		t.Fatal("an unwired eraser reported success")
	}
}

// 注册到真实 Privacy registry 之后：擦除报告与审计事件里都不得出现任何值。
func TestPluginSessionStatePrivacyContributionErasesWithoutLeakingValues(t *testing.T) {
	eraser, store, _, substrate := newPrivacyEraserFixture(t, []string{"sid-one", "sid-two"})
	seedPluginSessionState(t, store, "sid-one", "SECRET-VALUE-ONE")
	seedPluginSessionState(t, store, "sid-two", "SECRET-VALUE-TWO")

	registry := privacy.New()
	registry.SetPermissionCheck(func(context.Context, string, string, string) error { return nil })
	auditor := &recordingPrivacyAuditor{}
	registry.SetAuditor(auditor)
	if err := registerPluginSessionStatePrivacyContribution(registry, substrate, eraser.sessions); err != nil {
		t.Fatal(err)
	}
	if err := registerPluginSessionStatePrivacyContribution(nil, substrate, eraser.sessions); err == nil {
		t.Fatal("a nil registry was accepted")
	}
	if err := registerPluginSessionStatePrivacyContribution(registry, nil, eraser.sessions); err == nil {
		t.Fatal("a nil invalidator was accepted")
	}

	report, err := registry.EraseUser(context.Background(), "admin-1", strconv.Itoa(42))
	if err != nil {
		t.Fatal(err)
	}
	if report.Partial || len(report.Failures) != 0 {
		t.Fatalf("erase reported partial failure: %#v", report)
	}
	encodedReport, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encodedReport, []byte("SECRET-VALUE")) {
		t.Fatalf("erase report leaked a plugin value: %s", encodedReport)
	}
	encodedEvents, err := json.Marshal(auditor.events())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encodedEvents, []byte("SECRET-VALUE")) || bytes.Contains(encodedEvents, []byte("checkout/step")) {
		t.Fatalf("privacy audit ledger leaked plugin state: %s", encodedEvents)
	}
	if bytes.Contains(encodedReport, []byte("checkout/step")) {
		t.Fatalf("erase report leaked a namespace key: %s", encodedReport)
	}
	for _, sid := range []string{"sid-one", "sid-two"} {
		if _, found := pluginSessionStateValue(t, store, sid); found {
			t.Fatalf("registered erase hook did not clear %q", sid)
		}
	}
}
