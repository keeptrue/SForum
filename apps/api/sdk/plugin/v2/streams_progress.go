package pluginv2

import (
	"context"
	"errors"
	"io"
	"time"

	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// RuntimeStreams is the immutable handwritten dispatch surface for generated
// streaming RPCs that are not plugin-to-plugin services.
type RuntimeStreams struct {
	Lifecycle LifecycleStreamHandler
	Route     RouteStreamHandler
	Job       JobStreamHandler
	File      FileStreamHandler
}

type LifecycleStreamHandler func(context.Context, *protocolwire.LifecycleRequest, *ProgressStream) error
type JobStreamHandler func(context.Context, *pluginwire.JobRequest, *ProgressStream) error
type RouteStreamHandler func(*RouteStream) error
type FileStreamHandler func(*FileStream) error

// RuntimeStreamError is a stable, sanitized error a stream handler may return.
type RuntimeStreamError struct {
	Code       protocolwire.ErrorCode
	Reason     string
	Message    string
	Retryable  bool
	RetryAfter time.Time
	Metadata   map[string]string

	remote bool
}

func (e *RuntimeStreamError) Error() string {
	if e == nil {
		return ""
	}
	if e.Reason == "" {
		return e.Message
	}
	return e.Reason + ": " + e.Message
}

// ProgressStream attaches the authoritative runtime response context to every
// lifecycle or job progress update before it crosses the process boundary.
type ProgressStream struct {
	request *protocolwire.RequestContext
	now     func() time.Time
	send    func(*protocolwire.ProgressUpdate) error
}

func (s *ProgressStream) Send(update *protocolwire.ProgressUpdate) error {
	if s == nil || s.send == nil {
		return errors.New("plugin progress stream is nil")
	}
	if update == nil || update.GetState() == protocolwire.ProgressState_PROGRESS_STATE_UNSPECIFIED {
		return &RuntimeStreamError{
			Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: "runtime.progress_invalid",
			Message: "A progress update and explicit state are required.",
		}
	}
	copy := proto.Clone(update).(*protocolwire.ProgressUpdate)
	copy.Context = responseContext(s.request, s.now())
	return s.send(copy)
}

func (s *Server) RunLifecycle(request *protocolwire.LifecycleRequest, stream grpc.ServerStreamingServer[protocolwire.ProgressUpdate]) error {
	s.mu.RLock()
	handler := s.streams.Lifecycle
	s.mu.RUnlock()
	if handler == nil {
		return s.UnimplementedPluginRuntimeServiceServer.RunLifecycle(request, stream)
	}
	return s.dispatchProgress(stream.Context(), request.GetContext(), request.GetStepId(), stream.Send,
		func(progress *ProgressStream) error { return handler(stream.Context(), request, progress) })
}

func (s *Server) ExecuteJob(request *pluginwire.JobRequest, stream grpc.ServerStreamingServer[protocolwire.ProgressUpdate]) error {
	s.mu.RLock()
	handler := s.streams.Job
	// 显式 RuntimeStreams.Job 优先；否则回落到 JobRegistry 分发。
	if handler == nil && s.jobRegistry != nil {
		handler = s.jobRegistry.StreamHandler()
	}
	s.mu.RUnlock()
	if handler == nil {
		return s.UnimplementedPluginRuntimeServiceServer.ExecuteJob(request, stream)
	}
	return s.dispatchProgress(stream.Context(), request.GetContext(), request.GetJobId(), stream.Send,
		func(progress *ProgressStream) error { return handler(stream.Context(), request, progress) })
}

func (s *Server) dispatchProgress(
	ctx context.Context,
	request *protocolwire.RequestContext,
	stepID string,
	send func(*protocolwire.ProgressUpdate) error,
	handler func(*ProgressStream) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	progress := &ProgressStream{request: request, now: s.nowTime, send: send}
	if detail := validateRuntimeClientContext(request); detail != nil {
		return progress.Send(&protocolwire.ProgressUpdate{
			StepId: stepID, State: protocolwire.ProgressState_PROGRESS_STATE_FAILED, Error: detail,
		})
	}
	if detail := s.validateRuntimeContext(request); detail != nil {
		return progress.Send(&protocolwire.ProgressUpdate{
			StepId: stepID, State: protocolwire.ProgressState_PROGRESS_STATE_FAILED, Error: detail,
		})
	}
	err := handler(progress)
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var runtimeErr *RuntimeStreamError
	if errors.As(err, &runtimeErr) && runtimeErr.remote {
		return nil
	}
	state := protocolwire.ProgressState_PROGRESS_STATE_FAILED
	if errors.Is(err, context.Canceled) {
		state = protocolwire.ProgressState_PROGRESS_STATE_CANCELLED
	}
	return progress.Send(&protocolwire.ProgressUpdate{
		StepId: stepID, State: state, Error: runtimeStreamErrorDetail(err),
	})
}

func runtimeStreamErrorDetail(err error) *protocolwire.ErrorDetail {
	var runtimeErr *RuntimeStreamError
	if !errors.As(err, &runtimeErr) || runtimeErr == nil {
		switch {
		case errors.Is(err, context.Canceled):
			runtimeErr = &RuntimeStreamError{Code: protocolwire.ErrorCode_ERROR_CODE_CANCELLED, Reason: "runtime.stream_cancelled", Message: "The runtime stream was cancelled.", Retryable: true}
		case errors.Is(err, context.DeadlineExceeded):
			runtimeErr = &RuntimeStreamError{Code: protocolwire.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED, Reason: "runtime.stream_deadline_exceeded", Message: "The runtime stream deadline expired.", Retryable: true}
		default:
			runtimeErr = &RuntimeStreamError{Code: protocolwire.ErrorCode_ERROR_CODE_INTERNAL, Reason: "runtime.stream_handler_failed", Message: "The runtime stream handler failed."}
		}
	}
	code := runtimeErr.Code
	if code == protocolwire.ErrorCode_ERROR_CODE_UNSPECIFIED {
		code = protocolwire.ErrorCode_ERROR_CODE_INTERNAL
	}
	detail := &protocolwire.ErrorDetail{
		Code: code, Reason: runtimeErr.Reason, Message: runtimeErr.Message, Retryable: runtimeErr.Retryable,
	}
	if !runtimeErr.RetryAfter.IsZero() {
		detail.RetryAfter = timestamppb.New(runtimeErr.RetryAfter)
	}
	if len(runtimeErr.Metadata) > 0 {
		detail.Metadata = make(map[string]string, len(runtimeErr.Metadata))
		for key, value := range runtimeErr.Metadata {
			detail.Metadata[key] = value
		}
	}
	return detail
}

func runtimeStreamErrorFromDetail(detail *protocolwire.ErrorDetail) *RuntimeStreamError {
	if detail == nil {
		return &RuntimeStreamError{Code: protocolwire.ErrorCode_ERROR_CODE_INTERNAL, Reason: "runtime.stream_remote_error_invalid", Message: "The remote stream returned an invalid error.", remote: true}
	}
	result := &RuntimeStreamError{
		Code: detail.GetCode(), Reason: detail.GetReason(), Message: detail.GetMessage(),
		Retryable: detail.GetRetryable(), Metadata: make(map[string]string, len(detail.GetMetadata())), remote: true,
	}
	if retry := detail.GetRetryAfter(); retry != nil && retry.IsValid() {
		result.RetryAfter = retry.AsTime()
	}
	for key, value := range detail.GetMetadata() {
		result.Metadata[key] = value
	}
	return result
}

func invalidRuntimeStream(reason, message string) error {
	return &RuntimeStreamError{Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: reason, Message: message}
}

func cloneDataChunk(chunk *protocolwire.DataChunk) *protocolwire.DataChunk {
	if chunk == nil {
		return nil
	}
	return proto.Clone(chunk).(*protocolwire.DataChunk)
}
