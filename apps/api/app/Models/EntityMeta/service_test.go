package entitymeta

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

type memoryStore struct {
	mu        sync.Mutex
	defs      map[string]fieldRow
	values    map[string]valueRow
	users     map[int64]bool
	topics    map[int64]int64 // topicID -> author
	nextDefID int64
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		defs:      map[string]fieldRow{},
		values:    map[string]valueRow{},
		users:     map[int64]bool{1: true, 2: true},
		topics:    map[int64]int64{10: 1},
		nextDefID: 1,
	}
}

func valueKey(entityType string, entityID int64, fieldKey string) string {
	return entityType + ":" + strconv.FormatInt(entityID, 10) + ":" + fieldKey
}

func (s *memoryStore) ListDefinitions(_ context.Context, entityType string) ([]fieldRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]fieldRow, 0, len(s.defs))
	for _, row := range s.defs {
		if entityType != "" && row.EntityType != entityType {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *memoryStore) GetDefinitionByKey(_ context.Context, fieldKey string) (fieldRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.defs[fieldKey]
	if !ok {
		return fieldRow{}, ErrNotFound
	}
	return row, nil
}

func (s *memoryStore) CreateDefinition(_ context.Context, row fieldRow) (fieldRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.defs[row.FieldKey]; exists {
		return fieldRow{}, ErrInvalid
	}
	row.ID = s.nextDefID
	s.nextDefID++
	now := time.Now().UTC()
	row.CreatedAt = now
	row.UpdatedAt = now
	s.defs[row.FieldKey] = row
	return row, nil
}

func (s *memoryStore) UpdateDefinition(_ context.Context, fieldKey string, row fieldRow) (fieldRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.defs[fieldKey]
	if !ok {
		return fieldRow{}, ErrNotFound
	}
	row.ID = existing.ID
	row.FieldKey = existing.FieldKey
	row.EntityType = existing.EntityType
	row.ValueType = existing.ValueType
	row.CreatedAt = existing.CreatedAt
	row.UpdatedAt = time.Now().UTC()
	s.defs[fieldKey] = row
	return row, nil
}

func (s *memoryStore) DeleteDefinition(_ context.Context, fieldKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.defs[fieldKey]; !ok {
		return ErrNotFound
	}
	delete(s.defs, fieldKey)
	for k, v := range s.values {
		if v.FieldKey == fieldKey {
			delete(s.values, k)
		}
	}
	return nil
}

func (s *memoryStore) ListValues(_ context.Context, entityType string, entityID int64) ([]valueRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []valueRow
	for _, v := range s.values {
		if v.EntityType == entityType && v.EntityID == entityID {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *memoryStore) UpsertValue(_ context.Context, row valueRow) (valueRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row.UpdatedAt = time.Now().UTC()
	s.values[valueKey(row.EntityType, row.EntityID, row.FieldKey)] = row
	return row, nil
}

func (s *memoryStore) DeleteValue(_ context.Context, entityType string, entityID int64, fieldKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, valueKey(entityType, entityID, fieldKey))
	return nil
}

func (s *memoryStore) EntityExists(_ context.Context, entityType string, entityID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch entityType {
	case EntityUser:
		return s.users[entityID], nil
	case EntityTopic:
		_, ok := s.topics[entityID]
		return ok, nil
	default:
		return false, ErrInvalid
	}
}

func (s *memoryStore) TopicAuthorID(_ context.Context, topicID int64) (int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	author, ok := s.topics[topicID]
	return author, ok, nil
}

type hookCapture struct {
	mu    sync.Mutex
	name  string
	count int
}

func (h *hookCapture) Emit(_ context.Context, envelope appevents.Envelope) appevents.Result {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.name = envelope.Name
	h.count++
	return appevents.Result{}
}

func managerActor(id int64) identity.Actor {
	return identity.Actor{
		ID:     id,
		Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionEntityMetaManage: true,
		},
	}
}

func ownerActor(id int64) identity.Actor {
	return identity.Actor{
		ID:     id,
		Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionTopicEditOwn: true,
		},
	}
}

func TestServiceCreateDefinitionAndUpsertValues(t *testing.T) {
	store := newMemoryStore()
	hooks := &hookCapture{}
	service := NewService(store).WithPublisher(hooks)
	ctx := context.Background()
	admin := managerActor(99)

	def, err := service.CreateDefinition(ctx, admin, CreateFieldInput{
		FieldKey:   "demo.bio_extra",
		EntityType: EntityUser,
		ValueType:  ValueString,
		Visibility: VisibilityPublic,
		LabelENUS:  "Extra bio",
		LabelZHCN:  "额外简介",
	})
	if err != nil {
		t.Fatalf("create definition: %v", err)
	}
	if def.FieldKey != "demo.bio_extra" || def.EntityType != EntityUser {
		t.Fatalf("unexpected definition: %#v", def)
	}

	owner := ownerActor(1)
	values, err := service.UpsertValues(ctx, owner, EntityUser, 1, []UpsertValueInput{
		{FieldKey: "demo.bio_extra", Value: "hello"},
	})
	if err != nil {
		t.Fatalf("upsert values: %v", err)
	}
	if len(values) != 1 || values[0].Value != "hello" {
		t.Fatalf("unexpected values: %#v", values)
	}
	if hooks.count != 1 || hooks.name != appevents.EntityMetaUpdated {
		t.Fatalf("expected entity_meta.updated event, got name=%q count=%d", hooks.name, hooks.count)
	}

	// 匿名可读 public 字段。
	guest := identity.Actor{}
	listed, err := service.ListValues(ctx, guest, EntityUser, 1)
	if err != nil || len(listed) != 1 {
		t.Fatalf("guest list: err=%v values=%#v", err, listed)
	}

	// 他人不能写 user 字段。
	other := ownerActor(2)
	if _, err := service.UpsertValues(ctx, other, EntityUser, 1, []UpsertValueInput{
		{FieldKey: "demo.bio_extra", Value: "nope"},
	}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}
}

func TestServiceVisibilityAndAdminFields(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()
	admin := managerActor(99)

	_, err := service.CreateDefinition(ctx, admin, CreateFieldInput{
		FieldKey:   "demo.private_note",
		EntityType: EntityUser,
		ValueType:  ValueText,
		Visibility: VisibilityAdmin,
		LabelENUS:  "Private note",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = service.UpsertValues(ctx, admin, EntityUser, 1, []UpsertValueInput{
		{FieldKey: "demo.private_note", Value: "secret"},
	})
	if err != nil {
		t.Fatalf("admin upsert: %v", err)
	}

	owner := ownerActor(1)
	// owner 不可读 admin 字段。
	listed, err := service.ListValues(ctx, owner, EntityUser, 1)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("owner should not see admin fields: %#v", listed)
	}
	// owner 不可写 admin 字段。
	if _, err := service.UpsertValues(ctx, owner, EntityUser, 1, []UpsertValueInput{
		{FieldKey: "demo.private_note", Value: "x"},
	}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied for owner writing admin field, got %v", err)
	}

	// 公开目录不暴露 admin 字段。
	publicDefs, err := service.ListPublicDefinitions(ctx, EntityUser)
	if err != nil {
		t.Fatalf("public defs: %v", err)
	}
	for _, d := range publicDefs {
		if d.FieldKey == "demo.private_note" {
			t.Fatal("admin field leaked into public definitions")
		}
	}
}

func TestServiceRejectsInvalidFieldKey(t *testing.T) {
	service := NewService(newMemoryStore())
	admin := managerActor(1)
	_, err := service.CreateDefinition(context.Background(), admin, CreateFieldInput{
		FieldKey:   "Bad Key",
		EntityType: EntityUser,
		ValueType:  ValueString,
		LabelENUS:  "x",
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected invalid, got %v", err)
	}
}

func TestServiceTopicAuthorWrite(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store)
	ctx := context.Background()
	admin := managerActor(99)
	_, err := service.CreateDefinition(ctx, admin, CreateFieldInput{
		FieldKey:   "demo.topic_flag",
		EntityType: EntityTopic,
		ValueType:  ValueBoolean,
		Visibility: VisibilityPublic,
		LabelENUS:  "Flag",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	author := ownerActor(1)
	values, err := service.UpsertValues(ctx, author, EntityTopic, 10, []UpsertValueInput{
		{FieldKey: "demo.topic_flag", Value: true},
	})
	if err != nil {
		t.Fatalf("author write: %v", err)
	}
	if len(values) != 1 {
		t.Fatalf("expected one value, got %#v", values)
	}
	if v, ok := values[0].Value.(bool); !ok || !v {
		t.Fatalf("expected true, got %#v", values[0].Value)
	}
}
