package extensions

import (
	"context"
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func TestServiceProviderSlotInspectionRequiresViewAuthorityAndRuntimeSource(t *testing.T) {
	want := ProviderSlotInspection{Revision: 7, Slots: []ProviderSlotInspectionItem{}}
	service := NewServiceWithRuntime(nil, t.TempDir(), providerSlotInspectionRuntime{
		LocalRuntimeManager: LocalRuntimeManager{}, inspection: want,
	})
	viewer := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionExtensionView: true,
	}}
	got, err := service.InspectProviderSlots(context.Background(), viewer)
	if err != nil || got.Revision != want.Revision || got.Slots == nil {
		t.Fatalf("viewer inspection = %#v, %v", got, err)
	}
	if _, err := service.InspectProviderSlots(context.Background(), identity.Actor{ID: 2, Status: identity.UserStatusActive}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("denied inspection = %v", err)
	}
	unavailable := NewService(nil, t.TempDir())
	if _, err := unavailable.InspectProviderSlots(context.Background(), viewer); !errors.Is(err, ErrProviderSlotInspectionUnavailable) {
		t.Fatalf("unavailable inspection = %v", err)
	}
}

type providerSlotInspectionRuntime struct {
	LocalRuntimeManager
	inspection ProviderSlotInspection
}

func (r providerSlotInspectionRuntime) ProviderSlotInspection(context.Context) (ProviderSlotInspection, error) {
	return r.inspection, nil
}
