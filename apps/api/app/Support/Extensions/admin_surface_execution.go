package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

var (
	ErrAdminSurfaceNotInvokable = errors.New("admin surface has no typed runtime handler")
	ErrAdminSurfaceRuntimeStale = errors.New("admin surface runtime contract is stale")
)

type AdminSurfaceInvocation struct {
	ExpectedContract AdminSurfaceContract
	ContractVersion  string
	Input            map[string]any
	Actor            *ProtocolV2InvocationActor
	IdempotencyKey   string
}

type AdminSurfaceInvocationResult struct {
	Contract AdminSurfaceContract
	Output   map[string]any
}

type adminSurfaceRuntime interface {
	InvokeAdminSurface(
		context.Context,
		RuntimeInstanceIdentity,
		AdminSurfaceContract,
		map[string]any,
		*ProtocolV2InvocationActor,
		string,
	) (map[string]any, error)
}

// InvokeAdminSurface resolves one exact Registry contract and holds runtime
// admission while the typed props/result round trip crosses Protocol V2.
func (m *RuntimeInvoker) InvokeAdminSurface(
	ctx context.Context,
	input AdminSurfaceInvocation,
) (AdminSurfaceInvocationResult, error) {
	if m == nil || ctx == nil || m.hooks == nil || m.hooks.adminSurfaces == nil {
		return AdminSurfaceInvocationResult{}, ErrAdminSurfaceRegistryInvalid
	}
	contract, err := m.hooks.adminSurfaces.resolveRuntimeContract(input.ExpectedContract.ID)
	if err != nil {
		return AdminSurfaceInvocationResult{}, err
	}
	// HTTP consumers audit the resolved exact artifact before entering plugin
	// code. Reject a compatible publication swap instead of silently executing
	// a newer artifact under the older audit identity.
	if !sameAdminSurfaceRuntimeContract(contract, input.ExpectedContract) {
		return AdminSurfaceInvocationResult{}, ErrAdminSurfaceRuntimeStale
	}
	if version := strings.TrimSpace(input.ContractVersion); version == "" || version != contract.ContractVersion {
		return AdminSurfaceInvocationResult{}, ErrAdminSurfaceRuntimeStale
	}
	if strings.TrimSpace(contract.Handler) == "" || strings.TrimSpace(contract.PropsSchema) == "" || strings.TrimSpace(contract.ResultSchema) == "" {
		return AdminSurfaceInvocationResult{}, ErrAdminSurfaceNotInvokable
	}
	if contract.Operation == extensions.AdminSurfaceOperationCommand {
		if input.Actor == nil || input.Actor.UserID <= 0 || strings.TrimSpace(input.IdempotencyKey) == "" || ValidateProtocolV2InvocationIdempotencyKey(input.IdempotencyKey) != nil {
			return AdminSurfaceInvocationResult{}, ErrProtocolV2ActorDelegationInvalid
		}
	}
	if err := m.hooks.adminSurfaces.ValidateProps(contract, input.Input); err != nil {
		return AdminSurfaceInvocationResult{}, err
	}
	runtime, ok := m.starter.(adminSurfaceRuntime)
	if !ok {
		return AdminSurfaceInvocationResult{}, ErrProtocolInstanceUnsupported
	}
	identity := RuntimeInstanceIdentity{ExtensionID: contract.ExtensionID, InstanceID: contract.InstanceID}
	lease, err := m.AcquireRuntimeCall(ctx, identity, RuntimeCallAdminSurface)
	if err != nil {
		return AdminSurfaceInvocationResult{}, err
	}
	defer lease.Release()
	props, err := cloneHookDocument(input.Input)
	if err != nil {
		return AdminSurfaceInvocationResult{}, fmt.Errorf("clone admin surface props: %w", err)
	}
	output, err := runtime.InvokeAdminSurface(
		lease.Context, identity, contract, props, input.Actor, input.IdempotencyKey,
	)
	if err != nil {
		return AdminSurfaceInvocationResult{}, err
	}
	if err := m.hooks.adminSurfaces.ValidateResult(contract, output); err != nil {
		return AdminSurfaceInvocationResult{}, err
	}
	result, err := cloneHookDocument(output)
	if err != nil {
		return AdminSurfaceInvocationResult{}, fmt.Errorf("clone admin surface result: %w", err)
	}
	return AdminSurfaceInvocationResult{Contract: contract, Output: result}, nil
}

func validateFrozenAdminSurface(
	contract AdminSurfaceContract,
	declarations []extensions.ManifestAdminSurface,
) error {
	contractPropsSchema, contractResultSchema := adminSurfaceContractSchemaReferences(contract)
	contractOperation := contract.Operation
	if contractOperation == "" {
		contractOperation = defaultAdminSurfaceOperationForKind(contract.Kind)
	}
	for _, declaration := range declarations {
		if declaration.ID != contract.ID {
			continue
		}
		propsSchema, resultSchema := adminSurfaceSchemaReferences(declaration)
		if declaration.ContractVersion != contract.ContractVersion || declaration.Kind != contract.Kind ||
			declaration.Action != contract.Action || declaration.TargetID != contract.TargetID ||
			declaration.PlacementID != contract.PlacementID || declaration.PlacementContractVersion != contract.PlacementContractVersion ||
			declaration.Label != contract.Label || declaration.Handler != contract.Handler ||
			propsSchema != contractPropsSchema || resultSchema != contractResultSchema ||
			adminSurfaceOperation(declaration) != contractOperation || declaration.Schema != contract.Schema || declaration.Permission != contract.Permission ||
			declaration.Priority != contract.Priority {
			return ErrAdminSurfaceRuntimeStale
		}
		return nil
	}
	return fmt.Errorf("%w: surface %s", ErrAdminSurfaceRuntimeStale, strings.TrimSpace(contract.ID))
}

func adminSurfaceContractSchemaReferences(contract AdminSurfaceContract) (string, string) {
	propsSchema, resultSchema := contract.PropsSchema, contract.ResultSchema
	if contract.Schema != "" {
		if propsSchema == "" {
			propsSchema = contract.Schema
		}
		if resultSchema == "" {
			resultSchema = contract.Schema
		}
	}
	return propsSchema, resultSchema
}

// Compatibility facade: runtime logic is owned by focused collaborators.

func (m *Manager) InvokeAdminSurface(
	ctx context.Context,
	input AdminSurfaceInvocation,
) (AdminSurfaceInvocationResult, error) {
	return m.invoker.InvokeAdminSurface(ctx, input)
}
