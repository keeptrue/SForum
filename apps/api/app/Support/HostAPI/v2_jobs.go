package hostapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
)

type protocolV2JobServer struct {
	hostv2.UnimplementedJobServiceServer
	core *protocolV2Core
}

func (s *protocolV2JobServer) Enqueue(ctx context.Context, request *hostv2.JobEnqueueRequest) (*hostv2.JobEnqueueResponse, error) {
	response := &hostv2.JobEnqueueResponse{Context: protocolV2ResponseContext(request.GetContext())}
	if request.GetIdempotencyKey() != "" || request.GetDelay().AsDuration() != 0 {
		response.Error = protocolV2Unsupported("host.job_options_unsupported", "The legacy Host queue adapter cannot honor idempotency keys or delayed delivery.")
		return response, nil
	}
	if request.GetPayload() == nil || request.GetPayload().GetSchemaId() == "" || request.GetPayloadVersion() == "" {
		response.Error = &protocolv2.ErrorDetail{
			Code: protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: "host.job_payload_contract_required", Message: "Job payload schema id and version are required.",
		}
		return response, nil
	}
	if request.GetPayload().GetSchemaVersion() != request.GetPayloadVersion() {
		response.Error = protocolV2Unsupported("host.job_payload_version_mismatch", "Job payload versions in the request and typed document must match.")
		return response, nil
	}
	result := s.enqueueVersioned(ctx, request)
	if !result.OK {
		response.Error = protocolV2Failure(result.Reason, result.Message)
	}
	return response, nil
}

func (s *protocolV2JobServer) enqueueVersioned(ctx context.Context, request *hostv2.JobEnqueueRequest) Response {
	if s == nil || s.core == nil || s.core.service == nil {
		return fail("host.unavailable", "Host API is not configured.")
	}
	identity := request.GetContext().GetExtension()
	extensionID := identity.GetExtensionId()
	caps, err := s.core.service.loadCaps(ctx, extensionID)
	if err != nil {
		return fail("host.extension_unavailable", err.Error())
	}
	if err := caps.Require(capabilities.JobsEnqueue); err != nil {
		return denied(capabilities.JobsEnqueue)
	}
	source, ok := s.core.service.capabilities.(PluginJobContractSource)
	if !ok {
		return fail("host.job_contract_unavailable", "The authoritative plugin job contract source is not configured.")
	}
	contract, err := source.PluginJobContract(ctx, extensionID, request.GetJobKind())
	if err != nil {
		return fail("host.job_contract_failed", err.Error())
	}
	if contract.ExtensionVersion != identity.GetExtensionVersion() || contract.ArtifactDigest != identity.GetArtifactDigest() {
		return fail("host.job_runtime_stale", "The runtime artifact no longer matches the installed plugin job contract.")
	}
	if contract.PayloadSchemaID != request.GetPayload().GetSchemaId() || contract.PayloadSchemaVersion != request.GetPayloadVersion() {
		return fail("host.job_payload_contract_mismatch", "The payload schema does not match the declared plugin job contract.")
	}
	if s.core.service.jobAdmission == nil {
		return fail("host.job_admission_unavailable", "Plugin job runtime admission is not configured.")
	}
	admissionIdentity := PluginJobEnqueueIdentity{
		ExtensionID: extensionID, ExtensionVersion: identity.GetExtensionVersion(),
		ArtifactDigest: identity.GetArtifactDigest(), InstanceID: identity.GetInstanceId(),
	}
	if !admissionIdentity.valid() {
		return fail("host.job_runtime_stale", "The authenticated exact runtime identity is incomplete.")
	}
	lease, err := s.core.service.jobAdmission.AcquirePluginJobEnqueue(ctx, admissionIdentity)
	if err != nil {
		return pluginJobAdmissionFailure(err)
	}
	if lease == nil || lease.Context() == nil {
		if lease != nil {
			lease.Release()
		}
		return fail("host.job_admission_unavailable", "Plugin job runtime admission returned no lease.")
	}
	defer lease.Release()
	if err := lease.Context().Err(); err != nil {
		return pluginJobAdmissionFailure(pluginJobLeaseFailure(lease))
	}
	enqueuer, ok := s.core.service.jobs.(VersionedPluginJobEnqueuer)
	if !ok {
		return fail("host.job_versioned_queue_unavailable", "The versioned plugin job queue is not configured.")
	}
	if err := enqueuer.EnqueueVersionedPluginJob(
		lease.Context(), contract, identity.GetTrustGrantId(), protocolV2DocumentValues(request.GetPayload()),
	); err != nil {
		if lease.Context().Err() != nil {
			return pluginJobAdmissionFailure(pluginJobLeaseFailure(lease))
		}
		return fail("host.enqueue_failed", fmt.Sprintf("enqueue versioned plugin job: %v", err))
	}
	return success(map[string]any{
		"kind": request.GetJobKind(), "contractVersion": contract.JobContract,
		"payloadSchemaId": contract.PayloadSchemaID, "payloadVersion": contract.PayloadSchemaVersion,
		"enqueued": true,
	})
}

func pluginJobAdmissionFailure(err error) Response {
	switch {
	case errors.Is(err, ErrPluginJobEnqueueStale):
		return fail("host.job_runtime_stale", "The runtime is no longer the active exact plugin instance.")
	case errors.Is(err, ErrPluginJobEnqueueDraining), errors.Is(err, context.Canceled):
		if errors.Is(err, ErrPluginJobEnqueueDraining) {
			return fail("host.job_runtime_draining", "The runtime is draining and no longer accepts new plugin jobs.")
		}
		return fail("host.job_enqueue_cancelled", "The plugin job enqueue request was cancelled.")
	case errors.Is(err, context.DeadlineExceeded):
		return fail("host.job_enqueue_timeout", "The plugin job enqueue request exceeded its deadline.")
	default:
		return fail("host.job_admission_unavailable", fmt.Sprintf("Plugin job runtime admission failed: %v", err))
	}
}

func pluginJobLeaseFailure(lease PluginJobEnqueueLease) error {
	if source, ok := lease.(PluginJobEnqueueLeaseFailure); ok {
		if err := source.PluginJobEnqueueFailure(); err != nil {
			return err
		}
	}
	return context.Cause(lease.Context())
}

func (s *protocolV2JobServer) Cancel(_ context.Context, request *hostv2.JobCancelRequest) (*hostv2.JobCancelResponse, error) {
	return &hostv2.JobCancelResponse{
		Context: protocolV2ResponseContext(request.GetContext()),
		Error:   protocolV2Unsupported("host.job_cancel_unavailable", "Job cancellation has no legacy Host adapter."),
	}, nil
}

func (s *protocolV2JobServer) Watch(request *hostv2.JobWatchRequest, stream grpc.ServerStreamingServer[protocolv2.ProgressUpdate]) error {
	return stream.Send(&protocolv2.ProgressUpdate{
		Context: protocolV2ResponseContext(request.GetContext()),
		Error:   protocolV2Unsupported("host.job_watch_unavailable", "Job progress has no legacy Host adapter."),
	})
}
