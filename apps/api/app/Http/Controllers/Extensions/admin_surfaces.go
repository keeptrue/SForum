package extensionscontroller

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	CodeAdminSurfaceInvalid      = "extensions.admin_surface_invalid"
	CodeAdminSurfaceNotFound     = "extensions.admin_surface_not_found"
	CodeAdminSurfaceStale        = "extensions.admin_surface_stale"
	CodeAdminSurfaceNotInvokable = "extensions.admin_surface_not_invokable"
	CodeAdminSurfaceUnavailable  = "extensions.admin_surface_unavailable"
)

type AdminSurfaceRuntime interface {
	AdminSurfaceSnapshot(string) extensionsruntime.AdminSurfaceRegistrySnapshot
	ResolveAdminSurface(string) (extensionsruntime.AdminSurfaceContract, error)
	InvokeAdminSurface(context.Context, extensionsruntime.AdminSurfaceInvocation) (extensionsruntime.AdminSurfaceInvocationResult, error)
}

type AdminSurfaceView struct {
	ID                       string `json:"id"`
	ContractVersion          string `json:"contractVersion"`
	ExtensionID              string `json:"extensionId"`
	ExtensionVersion         string `json:"extensionVersion"`
	Kind                     string `json:"kind"`
	Action                   string `json:"action"`
	TargetID                 string `json:"targetId,omitempty"`
	PlacementID              string `json:"placementId,omitempty"`
	PlacementContractVersion string `json:"placementContractVersion,omitempty"`
	Label                    string `json:"label"`
	PropsSchema              string `json:"propsSchema,omitempty"`
	PropsSchemaDigest        string `json:"propsSchemaDigest,omitempty"`
	ResultSchema             string `json:"resultSchema,omitempty"`
	ResultSchemaDigest       string `json:"resultSchemaDigest,omitempty"`
	Operation                string `json:"operation"`
	Schema                   string `json:"schema,omitempty"`
	SchemaDigest             string `json:"schemaDigest,omitempty"`
	Priority                 int    `json:"priority"`
	Invokable                bool   `json:"invokable"`
}

type AdminSurfaceCatalogView struct {
	Revision uint64             `json:"revision"`
	Surfaces []AdminSurfaceView `json:"surfaces"`
}

type adminSurfaceInvokeRequest struct {
	ContractVersion string         `json:"contractVersion"`
	Input           map[string]any `json:"input"`
}

type AdminSurfaceInvocationView struct {
	Surface AdminSurfaceView `json:"surface"`
	Output  map[string]any   `json:"output"`
}

func (h *Controller) WithAdminSurfaces(runtime AdminSurfaceRuntime, auditor audit.Writer) *Controller {
	if h != nil {
		h.adminSurfaces = runtime
		h.adminAuditor = auditor
	}
	return h
}

func (h *Controller) listAdminSurfaces(c fiber.Ctx) error {
	actor, err := h.adminSurfaceActor(c)
	if err != nil {
		return err
	}
	if h.adminSurfaces == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, CodeAdminSurfaceUnavailable)
	}
	kind := strings.TrimSpace(c.Query("kind"))
	if kind != "" && !validAdminSurfaceKind(kind) {
		return fiber.NewError(fiber.StatusUnprocessableEntity, CodeAdminSurfaceInvalid)
	}
	snapshot := h.adminSurfaces.AdminSurfaceSnapshot(kind)
	placementID := strings.TrimSpace(c.Query("placementId"))
	allowed := make(map[string]bool, len(snapshot.Surfaces))
	for _, surface := range snapshot.Surfaces {
		allowed[surface.ID] = surface.Permission == "" || actor.Can(surface.Permission)
	}
	result := AdminSurfaceCatalogView{
		Revision: snapshot.Revision,
		Surfaces: make([]AdminSurfaceView, 0, len(snapshot.Surfaces)),
	}
	for _, surface := range snapshot.Surfaces {
		if placementID != "" && surface.PlacementID != placementID {
			continue
		}
		if !allowed[surface.ID] || surface.Action != "add" && !allowed[surface.TargetID] {
			continue
		}
		result.Surfaces = append(result.Surfaces, adminSurfaceView(surface))
	}
	return apphttp.OK(c, result)
}

func (h *Controller) invokeAdminSurface(c fiber.Ctx) error {
	actor, err := h.adminSurfaceActor(c)
	if err != nil {
		return err
	}
	if h.adminSurfaces == nil || h.adminAuditor == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, CodeAdminSurfaceUnavailable)
	}
	contract, err := h.adminSurfaces.ResolveAdminSurface(c.Params("surfaceId"))
	if err != nil {
		return mapAdminSurfaceError(err)
	}
	if contract.Permission != "" && !actor.Can(contract.Permission) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	var body adminSurfaceInvokeRequest
	if err := c.Bind().Body(&body); err != nil || strings.TrimSpace(body.ContractVersion) == "" || body.Input == nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, CodeAdminSurfaceInvalid)
	}
	body.ContractVersion = strings.TrimSpace(body.ContractVersion)
	idempotencyKey := c.Get("Idempotency-Key")
	if err := extensionsruntime.ValidateProtocolV2InvocationIdempotencyKey(idempotencyKey); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, CodeAdminSurfaceInvalid)
	}
	if contract.Operation == extensions.AdminSurfaceOperationCommand && strings.TrimSpace(idempotencyKey) == "" {
		return fiber.NewError(fiber.StatusUnprocessableEntity, CodeAdminSurfaceInvalid)
	}
	metadata := map[string]any{
		"surfaceId": contract.ID, "contractVersion": contract.ContractVersion,
		"extensionId": contract.ExtensionID, "extensionVersion": contract.ExtensionVersion,
		"artifactDigest": contract.ArtifactDigest, "runtimeInstanceId": contract.InstanceID,
		"kind": contract.Kind, "operation": contract.Operation, "placementId": contract.PlacementID, "status": "attempted",
	}
	if err := h.adminAuditor.Append(c.Context(), audit.Event{
		ActorUserID: actor.ID, Action: audit.ActionExtensionAdminSurface, Metadata: metadata,
	}); err != nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, CodeAdminSurfaceUnavailable)
	}
	status := "failed"
	defer func() {
		completion := make(map[string]any, len(metadata))
		for key, value := range metadata {
			completion[key] = value
		}
		completion["status"] = status
		auditCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.adminAuditor.Append(auditCtx, audit.Event{
			ActorUserID: actor.ID, Action: audit.ActionExtensionAdminSurface, Metadata: completion,
		})
	}()
	result, err := h.adminSurfaces.InvokeAdminSurface(c.Context(), extensionsruntime.AdminSurfaceInvocation{
		ExpectedContract: contract, ContractVersion: body.ContractVersion, Input: body.Input,
		Actor: adminSurfaceInvocationActor(actor), IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return mapAdminSurfaceError(err)
	}
	status = "succeeded"
	return apphttp.OK(c, AdminSurfaceInvocationView{
		Surface: adminSurfaceView(result.Contract), Output: result.Output,
	})
}

func adminSurfaceInvocationActor(actor identity.Actor) *extensionsruntime.ProtocolV2InvocationActor {
	permissions := make(map[string]bool, len(actor.Permissions)+1)
	for key, allowed := range actor.Permissions {
		permissions[key] = allowed
	}
	if actor.IsSuperAdmin() {
		permissions["*"] = true
	}
	return extensionsruntime.NewProtocolV2InvocationActor(actor.ID, actor.IsActive(), permissions)
}

func (h *Controller) adminSurfaceActor(c fiber.Ctx) (identity.Actor, error) {
	actor, err := h.actor(c)
	if err != nil {
		return identity.Actor{}, err
	}
	if !actor.Can(identity.PermissionAdminAccess) {
		return identity.Actor{}, fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	return actor, nil
}

func adminSurfaceView(contract extensionsruntime.AdminSurfaceContract) AdminSurfaceView {
	return AdminSurfaceView{
		ID: contract.ID, ContractVersion: contract.ContractVersion,
		ExtensionID: contract.ExtensionID, ExtensionVersion: contract.ExtensionVersion,
		Kind: contract.Kind, Action: contract.Action, TargetID: contract.TargetID,
		PlacementID: contract.PlacementID, PlacementContractVersion: contract.PlacementContractVersion,
		Label: contract.Label, PropsSchema: contract.PropsSchema, PropsSchemaDigest: contract.PropsSchemaDigest,
		ResultSchema: contract.ResultSchema, ResultSchemaDigest: contract.ResultSchemaDigest, Operation: contract.Operation,
		Schema: contract.Schema, SchemaDigest: contract.SchemaDigest,
		Priority: contract.Priority, Invokable: contract.Handler != "" && contract.PropsSchema != "" && contract.ResultSchema != "",
	}
}

func validAdminSurfaceKind(kind string) bool {
	switch kind {
	case "navigation", "dashboard", "list_column", "list_filter", "row_action", "bulk_action",
		"form", "notice", "editor_panel", "detail_region", "importer", "exporter":
		return true
	default:
		return false
	}
}

func mapAdminSurfaceError(err error) error {
	switch {
	case errors.Is(err, extensionsruntime.ErrAdminSurfaceNotFound):
		return fiber.NewError(fiber.StatusNotFound, CodeAdminSurfaceNotFound)
	case errors.Is(err, extensionsruntime.ErrAdminSurfaceRegistryInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, CodeAdminSurfaceInvalid)
	case errors.Is(err, extensionsruntime.ErrProtocolV2ActorDelegationInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, CodeAdminSurfaceInvalid)
	case errors.Is(err, extensionsruntime.ErrProtocolV2ActorDelegationUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, CodeAdminSurfaceUnavailable)
	case errors.Is(err, extensionsruntime.ErrAdminSurfaceRuntimeStale):
		return fiber.NewError(fiber.StatusConflict, CodeAdminSurfaceStale)
	case errors.Is(err, extensionsruntime.ErrAdminSurfaceNotInvokable):
		return fiber.NewError(fiber.StatusUnprocessableEntity, CodeAdminSurfaceNotInvokable)
	case errors.Is(err, context.DeadlineExceeded):
		return fiber.NewError(fiber.StatusGatewayTimeout, CodeAdminSurfaceUnavailable)
	case errors.Is(err, context.Canceled):
		return fiber.NewError(fiber.StatusServiceUnavailable, CodeAdminSurfaceUnavailable)
	case errors.Is(err, extensions.ErrRuntimeUnavailable),
		errors.Is(err, extensionsruntime.ErrProtocolInstanceUnsupported),
		errors.Is(err, extensionsruntime.ErrRuntimeAdmissionInvalid),
		errors.Is(err, extensionsruntime.ErrRuntimeAdmissionDraining),
		errors.Is(err, extensionsruntime.ErrRuntimeAdmissionForced),
		errors.Is(err, extensionsruntime.ErrRuntimeInstanceNotFound),
		errors.Is(err, extensionsruntime.ErrRuntimeInstanceNotActive),
		errors.Is(err, extensionsruntime.ErrRuntimeInstanceBusy):
		return fiber.NewError(fiber.StatusServiceUnavailable, CodeAdminSurfaceUnavailable)
	}
	var remote *extensionsruntime.ProtocolV2Error
	if errors.As(err, &remote) {
		switch remote.Code {
		case protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			protocolwire.ErrorCode_ERROR_CODE_MESSAGE_TOO_LARGE:
			return fiber.NewError(fiber.StatusUnprocessableEntity, CodeAdminSurfaceInvalid)
		case protocolwire.ErrorCode_ERROR_CODE_UNAUTHENTICATED,
			protocolwire.ErrorCode_ERROR_CODE_PERMISSION_DENIED:
			return fiber.NewError(fiber.StatusForbidden, "permission.denied")
		case protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND:
			return fiber.NewError(fiber.StatusNotFound, CodeAdminSurfaceNotFound)
		case protocolwire.ErrorCode_ERROR_CODE_CONFLICT,
			protocolwire.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
			protocolwire.ErrorCode_ERROR_CODE_PROTOCOL_MISMATCH,
			protocolwire.ErrorCode_ERROR_CODE_STALE_RUNTIME:
			return fiber.NewError(fiber.StatusConflict, CodeAdminSurfaceStale)
		case protocolwire.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED:
			return fiber.NewError(fiber.StatusGatewayTimeout, CodeAdminSurfaceUnavailable)
		default:
			return fiber.NewError(fiber.StatusServiceUnavailable, CodeAdminSurfaceUnavailable)
		}
	}
	if remoteStatus, ok := status.FromError(err); ok {
		switch remoteStatus.Code() {
		case codes.InvalidArgument, codes.OutOfRange, codes.ResourceExhausted:
			return fiber.NewError(fiber.StatusUnprocessableEntity, CodeAdminSurfaceInvalid)
		case codes.Unauthenticated, codes.PermissionDenied:
			return fiber.NewError(fiber.StatusForbidden, "permission.denied")
		case codes.NotFound:
			return fiber.NewError(fiber.StatusNotFound, CodeAdminSurfaceNotFound)
		case codes.AlreadyExists, codes.Aborted, codes.FailedPrecondition:
			return fiber.NewError(fiber.StatusConflict, CodeAdminSurfaceStale)
		case codes.DeadlineExceeded:
			return fiber.NewError(fiber.StatusGatewayTimeout, CodeAdminSurfaceUnavailable)
		default:
			return fiber.NewError(fiber.StatusServiceUnavailable, CodeAdminSurfaceUnavailable)
		}
	}
	return err
}
