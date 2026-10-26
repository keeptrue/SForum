package extensionsruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestManagerGuardTransportPreservesTypedCallFailure(t *testing.T) {
	starter := &managerGuardFailureStarter{failure: NewProtocolV2GuardCallFailure(
		ProtocolV2GuardFailureCrash, errors.New("plugin-reason-secret"),
	)}
	manager := NewManager(ManagerConfig{Starter: starter})
	err := manager.InvokeGuardInstance(context.Background(), RuntimeInstanceIdentity{
		ExtensionID: "demo.plugin", InstanceID: "runtime-1",
	}, ProtocolV2GuardRequest{})
	var failure *ProtocolV2GuardCallFailure
	if !errors.As(err, &failure) || failure.Kind() != ProtocolV2GuardFailureCrash ||
		!failure.RuntimeExecutionObserved() || !errors.Is(err, ErrProtocolV2GuardRuntimeFailed) ||
		strings.Contains(err.Error(), "plugin-reason-secret") ||
		strings.Contains(errors.Unwrap(failure).Error(), "plugin-reason-secret") {
		t.Fatalf("manager guard failure=%v", err)
	}
}

type managerGuardFailureStarter struct{ failure error }

func (*managerGuardFailureStarter) Start(context.Context, extensions.Extension) (RouteTarget, error) {
	return RouteTarget{}, nil
}

func (*managerGuardFailureStarter) Stop(context.Context, extensions.Extension) error { return nil }

func (s *managerGuardFailureStarter) InvokeGuardInstance(
	context.Context,
	RuntimeInstanceIdentity,
	ProtocolV2GuardRequest,
) error {
	return s.failure
}

var _ Starter = (*managerGuardFailureStarter)(nil)
var _ exactGuardInstanceInvoker = (*managerGuardFailureStarter)(nil)
