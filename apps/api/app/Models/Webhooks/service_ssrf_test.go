package webhooks

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func TestCreateEndpointRejectsPrivateTargets(t *testing.T) {
	svc := NewService(&memStore{}, nil, nil).WithAllowHTTP(true)
	actor := identity.Actor{
		ID: 1, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionSettingsManage: true},
	}
	cases := []string{
		"http://127.0.0.1/hook",
		"http://10.0.0.1/hook",
		"http://169.254.169.254/latest",
		"http://user:pass@example.com/h",
		"https://192.168.1.1/h",
	}
	for _, raw := range cases {
		_, err := svc.CreateEndpoint(context.Background(), actor, CreateEndpointInput{
			Name: "x", TargetURL: raw,
		})
		if !errors.Is(err, ErrInvalidURL) {
			t.Fatalf("%q: want ErrInvalidURL, got %v", raw, err)
		}
	}
}

func TestCreateEndpointAllowsPublicHTTPS(t *testing.T) {
	store := &memStore{}
	svc := NewService(store, nil, nil)
	actor := identity.Actor{
		ID: 1, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionSettingsManage: true},
	}
	ep, err := svc.CreateEndpoint(context.Background(), actor, CreateEndpointInput{
		Name: "public", TargetURL: "https://8.8.8.8/hook", Secret: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ep.TargetURL != "https://8.8.8.8/hook" {
		t.Fatalf("url=%s", ep.TargetURL)
	}
}

func TestCreateEndpointRejectsHTTPInProductionMode(t *testing.T) {
	svc := NewService(&memStore{}, nil, nil) // allowHTTP=false
	actor := identity.Actor{
		ID: 1, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionSettingsManage: true},
	}
	_, err := svc.CreateEndpoint(context.Background(), actor, CreateEndpointInput{
		Name: "http", TargetURL: "http://8.8.8.8/hook",
	})
	if !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("got %v", err)
	}
}

// memStore 最小 store，仅支持 CreateEndpoint 测试。
type memStore struct {
	next int64
	rows map[int64]EndpointRecord
}

func (m *memStore) ListEndpoints(context.Context) ([]EndpointRecord, error) { return nil, nil }
func (m *memStore) GetEndpoint(context.Context, int64) (EndpointRecord, error) {
	return EndpointRecord{}, ErrEndpointNotFound
}
func (m *memStore) CreateEndpoint(_ context.Context, input CreateEndpointInput) (EndpointRecord, error) {
	if m.rows == nil {
		m.rows = map[int64]EndpointRecord{}
	}
	m.next++
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	rec := EndpointRecord{
		Endpoint: Endpoint{
			ID: m.next, Name: input.Name, TargetURL: input.TargetURL,
			Events: input.Events, Enabled: enabled, Description: input.Description,
		},
		Secret: input.Secret,
	}
	m.rows[m.next] = rec
	return rec, nil
}
func (m *memStore) UpdateEndpoint(context.Context, int64, UpdateEndpointInput) (EndpointRecord, error) {
	return EndpointRecord{}, ErrEndpointNotFound
}
func (m *memStore) DeleteEndpoint(context.Context, int64) error { return nil }
func (m *memStore) ListEnabledEndpointsForEvent(context.Context, string) ([]EndpointRecord, error) {
	return nil, nil
}
func (m *memStore) CreateDeliveryTx(context.Context, pgx.Tx, CreateDeliveryInput) (Delivery, error) {
	return Delivery{}, nil
}
func (m *memStore) GetDelivery(context.Context, int64) (Delivery, error) {
	return Delivery{}, ErrDeliveryNotFound
}
func (m *memStore) UpdateDelivery(context.Context, DeliveryUpdate) error { return nil }
func (m *memStore) ListDeliveries(context.Context, int64, int) ([]Delivery, error) {
	return nil, nil
}
