package pluginv2

import (
	"context"
	"errors"
	"strings"
	"time"

	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// ProgressReader is shared by generated lifecycle and job server streams.
type ProgressReader struct {
	stream progressClientStream
}

type progressClientStream interface {
	Context() context.Context
	Recv() (*protocolwire.ProgressUpdate, error)
}

func (r *ProgressReader) Context() context.Context {
	if r == nil || r.stream == nil {
		return context.Background()
	}
	return r.stream.Context()
}

func (r *ProgressReader) Recv() (*protocolwire.ProgressUpdate, error) {
	if r == nil || r.stream == nil {
		return nil, errors.New("plugin progress client is nil")
	}
	update, err := r.stream.Recv()
	if err != nil {
		return nil, err
	}
	if update == nil || update.GetState() == protocolwire.ProgressState_PROGRESS_STATE_UNSPECIFIED {
		return nil, invalidRuntimeStream("runtime.progress_invalid", "The remote progress update has no explicit state.")
	}
	return proto.Clone(update).(*protocolwire.ProgressUpdate), nil
}

func RunLifecycleStream(
	ctx context.Context,
	client pluginwire.PluginRuntimeServiceClient,
	request *protocolwire.LifecycleRequest,
	options ...grpc.CallOption,
) (*ProgressReader, error) {
	if client == nil || request == nil {
		return nil, invalidRuntimeStream("runtime.lifecycle_request_required", "A runtime client and lifecycle request are required.")
	}
	if detail := validateRuntimeClientContext(request.GetContext()); detail != nil {
		return nil, runtimeStreamErrorFromDetail(detail)
	}
	stream, err := client.RunLifecycle(ctx, request, options...)
	if err != nil {
		return nil, err
	}
	return &ProgressReader{stream: stream}, nil
}

func ExecuteJobStream(
	ctx context.Context,
	client pluginwire.PluginRuntimeServiceClient,
	request *pluginwire.JobRequest,
	options ...grpc.CallOption,
) (*ProgressReader, error) {
	if client == nil || request == nil {
		return nil, invalidRuntimeStream("runtime.job_request_required", "A runtime client and job request are required.")
	}
	if detail := validateRuntimeClientContext(request.GetContext()); detail != nil {
		return nil, runtimeStreamErrorFromDetail(detail)
	}
	stream, err := client.ExecuteJob(ctx, request, options...)
	if err != nil {
		return nil, err
	}
	return &ProgressReader{stream: stream}, nil
}

// RouteClientStream sends the required open frame during construction and
// then shares the bounded chunk/close behavior with the server helper.
type RouteClientStream struct {
	*RouteStream
	client grpc.BidiStreamingClient[pluginwire.RouteStreamFrame, pluginwire.RouteStreamFrame]
}

func OpenRouteStream(
	ctx context.Context,
	client pluginwire.PluginRuntimeServiceClient,
	open *pluginwire.RouteStreamOpen,
	options ...grpc.CallOption,
) (*RouteClientStream, error) {
	if client == nil || open == nil {
		return nil, invalidRuntimeStream("runtime.route_open_required", "A runtime client and route open frame are required.")
	}
	if detail := validateRuntimeClientContext(open.GetContext()); detail != nil {
		return nil, runtimeStreamErrorFromDetail(detail)
	}
	if strings.TrimSpace(open.GetRouteId()) == "" || strings.TrimSpace(open.GetContractVersion()) == "" ||
		strings.TrimSpace(open.GetMethod()) == "" || strings.TrimSpace(open.GetPath()) == "" {
		return nil, invalidRuntimeStream("runtime.route_open_invalid", "Route id, contract version, method, and path are required.")
	}
	stream, err := client.StreamRoute(ctx, options...)
	if err != nil {
		return nil, err
	}
	copy := cloneRouteOpen(open)
	if err := stream.Send(&pluginwire.RouteStreamFrame{Frame: &pluginwire.RouteStreamFrame_Open{Open: copy}}); err != nil {
		_ = stream.CloseSend()
		return nil, err
	}
	return &RouteClientStream{RouteStream: &RouteStream{stream: stream, open: copy}, client: stream}, nil
}

func (s *RouteClientStream) CloseSend() error {
	if s == nil || s.client == nil {
		return errors.New("plugin route client stream is nil")
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.sendClosed = true
	return s.client.CloseSend()
}

// Close sends the typed terminal frame and half-closes the client direction.
func (s *RouteClientStream) Close(closeFrame *pluginwire.RouteStreamClose) error {
	if s == nil || s.RouteStream == nil {
		return errors.New("plugin route client stream is nil")
	}
	if err := s.RouteStream.Close(closeFrame); err != nil {
		return err
	}
	return s.CloseSend()
}

// FileClientStream sends the FileOpen frame before exposing chunk methods.
type FileClientStream struct {
	*FileStream
	client grpc.BidiStreamingClient[pluginwire.FileFrame, pluginwire.FileFrame]
}

func OpenFileStream(
	ctx context.Context,
	client pluginwire.PluginRuntimeServiceClient,
	open *pluginwire.FileOpen,
	options ...grpc.CallOption,
) (*FileClientStream, error) {
	if client == nil || open == nil {
		return nil, invalidRuntimeStream("runtime.file_open_required", "A runtime client and file open frame are required.")
	}
	if detail := validateRuntimeClientContext(open.GetContext()); detail != nil {
		return nil, runtimeStreamErrorFromDetail(detail)
	}
	if strings.TrimSpace(open.GetOperation()) == "" || strings.TrimSpace(open.GetFileId()) == "" {
		return nil, invalidRuntimeStream("runtime.file_open_invalid", "File operation and file id are required.")
	}
	stream, err := client.TransferFile(ctx, options...)
	if err != nil {
		return nil, err
	}
	copy := cloneFileOpen(open)
	if err := stream.Send(&pluginwire.FileFrame{Frame: &pluginwire.FileFrame_Open{Open: copy}}); err != nil {
		_ = stream.CloseSend()
		return nil, err
	}
	return &FileClientStream{FileStream: &FileStream{stream: stream, open: copy}, client: stream}, nil
}

func (s *FileClientStream) CloseSend() error {
	if s == nil || s.client == nil {
		return errors.New("plugin file client stream is nil")
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.sendClosed = true
	return s.client.CloseSend()
}

// Close sends final integrity data and half-closes the client direction.
func (s *FileClientStream) Close(closeFrame *pluginwire.FileClose) error {
	if s == nil || s.FileStream == nil {
		return errors.New("plugin file client stream is nil")
	}
	if err := s.FileStream.Close(closeFrame); err != nil {
		return err
	}
	return s.CloseSend()
}

func validateRuntimeClientContext(request *protocolwire.RequestContext) *protocolwire.ErrorDetail {
	if request == nil || request.GetRequestId() == "" || request.GetExtension() == nil {
		return &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: "runtime.stream_context_required",
			Message: "Request id and exact extension identity are required.",
		}
	}
	deadline := request.GetDeadline()
	if deadline == nil || !deadline.IsValid() {
		return &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: "runtime.stream_deadline_required",
			Message: "A valid stream deadline is required.",
		}
	}
	if !deadline.AsTime().After(time.Now()) {
		return &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED, Reason: "runtime.stream_deadline_expired",
			Message: "The stream deadline has expired.",
		}
	}
	return nil
}
