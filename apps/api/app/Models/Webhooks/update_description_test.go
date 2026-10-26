package webhooks

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// descStore 仅覆盖 UpdateEndpoint 的 description 合并语义。
type descStore struct {
	current EndpointRecord
	last    UpdateEndpointInput
}

func (d *descStore) ListEndpoints(context.Context) ([]EndpointRecord, error) { return nil, nil }
func (d *descStore) GetEndpoint(context.Context, int64) (EndpointRecord, error) {
	return d.current, nil
}
func (d *descStore) CreateEndpoint(context.Context, CreateEndpointInput) (EndpointRecord, error) {
	return EndpointRecord{}, nil
}
func (d *descStore) UpdateEndpoint(_ context.Context, _ int64, input UpdateEndpointInput) (EndpointRecord, error) {
	d.last = input
	out := d.current
	if input.Description != nil {
		out.Description = *input.Description
	}
	if input.Name != nil {
		out.Name = *input.Name
	}
	return out, nil
}
func (d *descStore) DeleteEndpoint(context.Context, int64) error { return nil }
func (d *descStore) CreateDeliveryTx(context.Context, pgx.Tx, CreateDeliveryInput) (Delivery, error) {
	return Delivery{}, nil
}
func (d *descStore) GetDelivery(context.Context, int64) (Delivery, error) {
	return Delivery{}, ErrDeliveryNotFound
}
func (d *descStore) UpdateDelivery(context.Context, DeliveryUpdate) error { return nil }
func (d *descStore) ListDeliveries(context.Context, int64, int) ([]Delivery, error) {
	return nil, nil
}
func (d *descStore) ListEnabledEndpointsForEvent(context.Context, string) ([]EndpointRecord, error) {
	return nil, nil
}

func TestUpdateEndpointPreservesOmittedDescription(t *testing.T) {
	store := &descStore{current: EndpointRecord{
		Endpoint: Endpoint{ID: 1, Name: "hook", Description: "keep me", Enabled: true, CreatedAt: time.Now().UTC()},
	}}
	svc := NewService(store, nil, nil)
	actor := identity.Actor{
		ID: 1, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionSettingsManage: true},
	}
	item, err := svc.UpdateEndpoint(context.Background(), actor, 1, UpdateEndpointInput{
		Name: strPtr("renamed"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.last.Description != nil {
		t.Fatalf("expected nil description pointer, got %#v", store.last.Description)
	}
	if item.Description != "keep me" {
		t.Fatalf("description=%q", item.Description)
	}
}

func TestUpdateEndpointClearsExplicitEmptyDescription(t *testing.T) {
	store := &descStore{current: EndpointRecord{
		Endpoint: Endpoint{ID: 1, Name: "hook", Description: "old", Enabled: true, CreatedAt: time.Now().UTC()},
	}}
	svc := NewService(store, nil, nil)
	actor := identity.Actor{
		ID: 1, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionSettingsManage: true},
	}
	empty := ""
	item, err := svc.UpdateEndpoint(context.Background(), actor, 1, UpdateEndpointInput{
		Description: &empty,
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.last.Description == nil || *store.last.Description != "" {
		t.Fatalf("expected explicit empty, got %#v", store.last.Description)
	}
	if item.Description != "" {
		t.Fatalf("description=%q", item.Description)
	}
}

func TestUpdateEndpointDeniedWithoutPermission(t *testing.T) {
	store := &descStore{current: EndpointRecord{Endpoint: Endpoint{ID: 1, Name: "hook"}}}
	svc := NewService(store, nil, nil)
	actor := identity.Actor{ID: 2, Status: identity.UserStatusActive}
	if _, err := svc.UpdateEndpoint(context.Background(), actor, 1, UpdateEndpointInput{}); err != identity.ErrPermissionDenied {
		t.Fatalf("want permission denied, got %v", err)
	}
}

func strPtr(s string) *string { return &s }
