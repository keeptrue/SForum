package pluginv2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	semver "github.com/Masterminds/semver/v3"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	ErrInvalidServiceDefinition = errors.New("invalid plugin service definition")

	serviceIDPattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,80}$`)
	serviceOperationPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	serviceSchemaPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
)

// ServiceDefinition declares one exact service version and its callable
// operations. Request and response schemas apply to every operation because
// the wire ServiceDescriptor publishes schemas at service scope.
type ServiceDefinition struct {
	ServiceID string
	// Version 必须是严格 SemVer；Host 还会要求 major 与 Manifest
	// service contractVersion 的 @N 一致。
	Version          string
	RequestSchemaID  string
	ResponseSchemaID string
	// RequiredAuthority is published for the Host broker to enforce against
	// the consumer. Provider requests carry the provider runtime grant instead.
	RequiredAuthority []string
	Operations        []ServiceOperation
}

// ServiceOperation can expose a unary handler, a bidirectional stream handler,
// or both under one stable operation name.
type ServiceOperation struct {
	Name   string
	Unary  ServiceUnaryHandler
	Stream ServiceStreamHandler
}

// ServiceCall is the validated unary input passed to plugin business code.
type ServiceCall struct {
	Context        *protocolwire.RequestContext
	ServiceID      string
	ServiceVersion string
	Operation      string
	Input          *protocolwire.TypedDocument
}

type ServiceUnaryHandler func(context.Context, *ServiceCall) (*protocolwire.TypedDocument, error)
type ServiceStreamHandler func(*ServiceStream) error

// ServiceError lets handlers return a stable application error without
// exposing Go error text or converting an expected failure to a transport
// failure.
type ServiceError struct {
	Code       protocolwire.ErrorCode
	Reason     string
	Message    string
	Retryable  bool
	RetryAfter time.Time
	Metadata   map[string]string

	remote bool
}

func (e *ServiceError) Error() string {
	if e == nil {
		return ""
	}
	if e.Reason != "" {
		return e.Reason + ": " + e.Message
	}
	return e.Message
}

type registeredService struct {
	descriptor *protocolwire.ServiceDescriptor
	operations map[string]ServiceOperation
}

// ServiceRegistry is immutable after construction, so handshake declarations
// and dispatch behavior cannot diverge while a runtime is active.
type ServiceRegistry struct {
	services    map[string]registeredService
	descriptors []*protocolwire.ServiceDescriptor
}

func NewServiceRegistry(definitions ...ServiceDefinition) (*ServiceRegistry, error) {
	registry := &ServiceRegistry{services: make(map[string]registeredService, len(definitions))}
	for _, definition := range definitions {
		service, err := prepareServiceDefinition(definition)
		if err != nil {
			return nil, err
		}
		key := serviceRegistryKey(service.descriptor.GetServiceId(), service.descriptor.GetVersion())
		if _, exists := registry.services[key]; exists {
			return nil, fmt.Errorf("%w: duplicate service %s@%s", ErrInvalidServiceDefinition,
				service.descriptor.GetServiceId(), service.descriptor.GetVersion())
		}
		registry.services[key] = service
		registry.descriptors = append(registry.descriptors, cloneServiceDescriptor(service.descriptor))
	}
	sort.Slice(registry.descriptors, func(i, j int) bool {
		left, right := registry.descriptors[i], registry.descriptors[j]
		if left.GetServiceId() != right.GetServiceId() {
			return left.GetServiceId() < right.GetServiceId()
		}
		leftVersion, _ := semver.StrictNewVersion(left.GetVersion())
		rightVersion, _ := semver.StrictNewVersion(right.GetVersion())
		if compared := leftVersion.Compare(rightVersion); compared != 0 {
			return compared < 0
		}
		return left.GetVersion() < right.GetVersion()
	})
	return registry, nil
}

// Descriptors returns detached handshake declarations in deterministic order.
func (r *ServiceRegistry) Descriptors() []*protocolwire.ServiceDescriptor {
	if r == nil {
		return nil
	}
	return cloneServices(r.descriptors)
}

// InvokeService validates and dispatches one unary service call. Expected
// failures are returned in ServiceResponse.Error; native errors are reserved
// for context cancellation and gRPC transport failure.
func (r *ServiceRegistry) InvokeService(ctx context.Context, request *pluginwire.ServiceRequest) (*pluginwire.ServiceResponse, error) {
	response := &pluginwire.ServiceResponse{Context: responseContext(serviceRequestContext(request), time.Now().UTC())}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	service, operation, detail := r.resolveUnary(request)
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	handlerCtx, cancel := bindRequestContextDeadline(ctx, request.GetContext())
	defer cancel()
	output, err := operation.Unary(handlerCtx, &ServiceCall{
		Context:        cloneRequestContext(request.GetContext()),
		ServiceID:      service.descriptor.GetServiceId(),
		ServiceVersion: service.descriptor.GetVersion(),
		Operation:      request.GetOperation(),
		Input:          cloneTypedDocument(request.GetInput()),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		response.Error = serviceErrorDetail(err)
		return response, nil
	}
	if schemaErr := validateServiceDocument(output, service.descriptor.GetResponseSchemaId(), "output"); schemaErr != nil {
		response.Error = serviceErrorDetail(schemaErr)
		return response, nil
	}
	response.Output = cloneTypedDocument(output)
	return response, nil
}

// StreamService consumes and validates the open frame before plugin code sees
// the stream. Known failures are sent as one typed terminal error frame.
func (r *ServiceRegistry) StreamService(stream grpc.BidiStreamingServer[pluginwire.ServiceStreamFrame, pluginwire.ServiceStreamFrame]) error {
	return r.streamService(stream, nil)
}

func (r *ServiceRegistry) streamService(
	stream grpc.BidiStreamingServer[pluginwire.ServiceStreamFrame, pluginwire.ServiceStreamFrame],
	validateRuntime func(*protocolwire.RequestContext) *protocolwire.ErrorDetail,
) error {
	if stream == nil {
		return errors.New("plugin service stream is nil")
	}
	if err := stream.Context().Err(); err != nil {
		return err
	}
	first, err := stream.Recv()
	if err != nil {
		if stream.Context().Err() != nil {
			return stream.Context().Err()
		}
		return sendServiceStreamError(stream, newServiceError(protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"service.open_required", "The first service stream frame must be an open frame."))
	}
	open := first.GetOpen()
	if open == nil {
		return sendServiceStreamError(stream, newServiceError(protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"service.open_required", "The first service stream frame must be an open frame."))
	}
	if validateRuntime != nil {
		if detail := validateRuntime(open.GetContext()); detail != nil {
			return sendServiceStreamDetail(stream, detail)
		}
	}
	service, operation, detail := r.resolveStream(open)
	if detail != nil {
		return sendServiceStreamDetail(stream, detail)
	}
	serviceStream := &ServiceStream{
		stream:         stream,
		open:           proto.Clone(open).(*pluginwire.ServiceStreamOpen),
		requestSchema:  service.descriptor.GetRequestSchemaId(),
		responseSchema: service.descriptor.GetResponseSchemaId(),
		idleTimeout:    open.GetIdleTimeout().AsDuration(),
	}
	err = operation.Stream(serviceStream)
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	if stream.Context().Err() != nil {
		return stream.Context().Err()
	}
	var serviceErr *ServiceError
	if errors.As(err, &serviceErr) && serviceErr.remote {
		return nil
	}
	return sendServiceStreamDetail(stream, serviceErrorDetail(err))
}

// ServiceStream exposes only typed documents after the SDK has consumed the
// opening frame. One goroutine may call Send while another calls Recv, matching
// grpc-go's stream concurrency contract.
type ServiceStream struct {
	stream         grpc.BidiStreamingServer[pluginwire.ServiceStreamFrame, pluginwire.ServiceStreamFrame]
	open           *pluginwire.ServiceStreamOpen
	requestSchema  string
	responseSchema string
	idleTimeout    time.Duration
}

func (s *ServiceStream) Context() context.Context { return s.stream.Context() }

func (s *ServiceStream) Open() *pluginwire.ServiceStreamOpen {
	if s == nil || s.open == nil {
		return nil
	}
	return proto.Clone(s.open).(*pluginwire.ServiceStreamOpen)
}

func (s *ServiceStream) Recv() (*protocolwire.TypedDocument, error) {
	if s == nil || s.stream == nil {
		return nil, errors.New("plugin service stream is nil")
	}
	var frame *pluginwire.ServiceStreamFrame
	var err error
	if s.idleTimeout <= 0 {
		frame, err = s.stream.Recv()
	} else {
		type receiveResult struct {
			frame *pluginwire.ServiceStreamFrame
			err   error
		}
		result := make(chan receiveResult, 1)
		go func() {
			frame, err := s.stream.Recv()
			result <- receiveResult{frame: frame, err: err}
		}()
		timer := time.NewTimer(s.idleTimeout)
		defer timer.Stop()
		select {
		case received := <-result:
			frame, err = received.frame, received.err
		case <-timer.C:
			return nil, newServiceError(protocolwire.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED,
				"service.idle_timeout_exceeded", "Plugin service stream exceeded its idle timeout.")
		case <-s.Context().Done():
			return nil, s.Context().Err()
		}
	}
	if err != nil {
		return nil, err
	}
	if message := frame.GetMessage(); message != nil {
		if schemaErr := validateServiceDocument(message, s.requestSchema, "stream input"); schemaErr != nil {
			return nil, schemaErr
		}
		return cloneTypedDocument(message), nil
	}
	if detail := frame.GetError(); detail != nil {
		return nil, serviceErrorFromDetail(detail, true)
	}
	return nil, newServiceError(protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
		"service.stream_frame_invalid", "Only typed message or error frames are allowed after service stream open.")
}

func (s *ServiceStream) Send(message *protocolwire.TypedDocument) error {
	if s == nil || s.stream == nil {
		return errors.New("plugin service stream is nil")
	}
	if err := s.Context().Err(); err != nil {
		return err
	}
	if schemaErr := validateServiceDocument(message, s.responseSchema, "stream output"); schemaErr != nil {
		return schemaErr
	}
	return s.stream.Send(&pluginwire.ServiceStreamFrame{Frame: &pluginwire.ServiceStreamFrame_Message{
		Message: cloneTypedDocument(message),
	}})
}

func prepareServiceDefinition(definition ServiceDefinition) (registeredService, error) {
	if !serviceIDPattern.MatchString(definition.ServiceID) {
		return registeredService{}, fmt.Errorf("%w: invalid service id %q", ErrInvalidServiceDefinition, definition.ServiceID)
	}
	if _, err := semver.StrictNewVersion(definition.Version); err != nil {
		return registeredService{}, fmt.Errorf("%w: service %q version %q is not strict SemVer: %v",
			ErrInvalidServiceDefinition, definition.ServiceID, definition.Version, err)
	}
	if !serviceSchemaPattern.MatchString(definition.RequestSchemaID) || !serviceSchemaPattern.MatchString(definition.ResponseSchemaID) {
		return registeredService{}, fmt.Errorf("%w: service %q requires versioned request and response schema ids",
			ErrInvalidServiceDefinition, definition.ServiceID)
	}
	authority, err := validateServiceAuthority(definition.RequiredAuthority)
	if err != nil {
		return registeredService{}, err
	}
	if len(definition.Operations) == 0 {
		return registeredService{}, fmt.Errorf("%w: service %q has no operations", ErrInvalidServiceDefinition, definition.ServiceID)
	}
	operations := make(map[string]ServiceOperation, len(definition.Operations))
	clientStreaming, serverStreaming := false, false
	for _, operation := range definition.Operations {
		if !serviceOperationPattern.MatchString(operation.Name) {
			return registeredService{}, fmt.Errorf("%w: service %q has invalid operation %q",
				ErrInvalidServiceDefinition, definition.ServiceID, operation.Name)
		}
		if operation.Unary == nil && operation.Stream == nil {
			return registeredService{}, fmt.Errorf("%w: service %q operation %q has no handler",
				ErrInvalidServiceDefinition, definition.ServiceID, operation.Name)
		}
		if _, exists := operations[operation.Name]; exists {
			return registeredService{}, fmt.Errorf("%w: service %q declares operation %q more than once",
				ErrInvalidServiceDefinition, definition.ServiceID, operation.Name)
		}
		operations[operation.Name] = operation
		if operation.Stream != nil {
			clientStreaming, serverStreaming = true, true
		}
	}
	return registeredService{
		descriptor: &protocolwire.ServiceDescriptor{
			ServiceId: definition.ServiceID, Version: definition.Version,
			RequestSchemaId: definition.RequestSchemaID, ResponseSchemaId: definition.ResponseSchemaID,
			ClientStreaming: clientStreaming, ServerStreaming: serverStreaming,
			RequiredAuthority: authority,
		},
		operations: operations,
	}, nil
}

func validateServiceAuthority(values []string) ([]string, error) {
	result := append([]string(nil), values...)
	seen := make(map[string]struct{}, len(result))
	for _, authority := range result {
		if !serviceIDPattern.MatchString(authority) {
			return nil, fmt.Errorf("%w: invalid required authority %q", ErrInvalidServiceDefinition, authority)
		}
		if _, exists := seen[authority]; exists {
			return nil, fmt.Errorf("%w: required authority %q is declared more than once", ErrInvalidServiceDefinition, authority)
		}
		seen[authority] = struct{}{}
	}
	sort.Strings(result)
	return result, nil
}

func (r *ServiceRegistry) resolveUnary(request *pluginwire.ServiceRequest) (registeredService, ServiceOperation, *protocolwire.ErrorDetail) {
	if request == nil {
		return registeredService{}, ServiceOperation{}, serviceErrorDetail(newServiceError(protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"service.request_required", "A service request is required."))
	}
	if detail := validateServiceRequestContext(request.GetContext()); detail != nil {
		return registeredService{}, ServiceOperation{}, detail
	}
	service, operation, detail := r.resolve(request.GetServiceId(), request.GetServiceVersion(), request.GetOperation())
	if detail != nil {
		return registeredService{}, ServiceOperation{}, detail
	}
	if operation.Unary == nil {
		return registeredService{}, ServiceOperation{}, serviceErrorDetail(newServiceError(protocolwire.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
			"service.unary_not_supported", "The selected operation does not support unary calls."))
	}
	if err := validateServiceDocument(request.GetInput(), service.descriptor.GetRequestSchemaId(), "input"); err != nil {
		return registeredService{}, ServiceOperation{}, serviceErrorDetail(err)
	}
	return service, operation, nil
}

func (r *ServiceRegistry) resolveStream(open *pluginwire.ServiceStreamOpen) (registeredService, ServiceOperation, *protocolwire.ErrorDetail) {
	if open == nil {
		return registeredService{}, ServiceOperation{}, serviceErrorDetail(newServiceError(protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"service.open_required", "A service stream open frame is required."))
	}
	if detail := validateServiceRequestContext(open.GetContext()); detail != nil {
		return registeredService{}, ServiceOperation{}, detail
	}
	if timeout := open.GetIdleTimeout(); timeout != nil {
		if err := timeout.CheckValid(); err != nil || timeout.AsDuration() <= 0 {
			return registeredService{}, ServiceOperation{}, serviceErrorDetail(newServiceError(protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
				"service.idle_timeout_invalid", "Service stream idle timeout must be a positive duration."))
		}
	}
	service, operation, detail := r.resolve(open.GetServiceId(), open.GetServiceVersion(), open.GetOperation())
	if detail != nil {
		return registeredService{}, ServiceOperation{}, detail
	}
	if operation.Stream == nil {
		return registeredService{}, ServiceOperation{}, serviceErrorDetail(newServiceError(protocolwire.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
			"service.stream_not_supported", "The selected operation does not support streaming calls."))
	}
	return service, operation, nil
}

func (r *ServiceRegistry) resolve(serviceID, version, operation string) (registeredService, ServiceOperation, *protocolwire.ErrorDetail) {
	if r == nil {
		return registeredService{}, ServiceOperation{}, serviceErrorDetail(newServiceError(protocolwire.ErrorCode_ERROR_CODE_UNAVAILABLE,
			"service.registry_unavailable", "Plugin service registry is unavailable."))
	}
	if !serviceIDPattern.MatchString(serviceID) || operation == "" || !serviceOperationPattern.MatchString(operation) {
		return registeredService{}, ServiceOperation{}, serviceErrorDetail(newServiceError(protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"service.identity_invalid", "Service id and operation are required and must use stable identifiers."))
	}
	if _, err := semver.StrictNewVersion(version); err != nil {
		return registeredService{}, ServiceOperation{}, serviceErrorDetail(newServiceError(protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"service.version_invalid", "Service version must be strict SemVer."))
	}
	service, exists := r.services[serviceRegistryKey(serviceID, version)]
	if !exists {
		return registeredService{}, ServiceOperation{}, serviceErrorDetail(newServiceError(protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND,
			"service.not_found", "The requested plugin service version is not registered."))
	}
	handler, exists := service.operations[operation]
	if !exists {
		return registeredService{}, ServiceOperation{}, serviceErrorDetail(newServiceError(protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND,
			"service.operation_not_found", "The requested plugin service operation is not registered."))
	}
	return service, handler, nil
}

func validateServiceRequestContext(request *protocolwire.RequestContext) *protocolwire.ErrorDetail {
	if request == nil || request.GetRequestId() == "" || request.GetExtension() == nil {
		return serviceErrorDetail(newServiceError(protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"service.context_required", "Request id and exact extension identity are required."))
	}
	deadline := request.GetDeadline()
	if deadline == nil || !deadline.IsValid() {
		return serviceErrorDetail(newServiceError(protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"service.deadline_required", "A valid request deadline is required."))
	}
	if !deadline.AsTime().After(time.Now()) {
		return serviceErrorDetail(newServiceError(protocolwire.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED,
			"service.deadline_expired", "The service request deadline has expired."))
	}
	return nil
}

func validateServiceDocument(document *protocolwire.TypedDocument, schemaRef, label string) *ServiceError {
	separator := strings.LastIndexByte(schemaRef, '@')
	if document == nil || separator < 1 || document.GetSchemaId() != schemaRef[:separator] || document.GetSchemaVersion() != schemaRef[separator+1:] {
		code := protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
		if strings.Contains(label, "output") {
			code = protocolwire.ErrorCode_ERROR_CODE_INTERNAL
		}
		return newServiceError(code, "service.schema_mismatch",
			fmt.Sprintf("Plugin service %s must match schema %s.", label, schemaRef))
	}
	return nil
}

func serviceErrorDetail(err error) *protocolwire.ErrorDetail {
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || serviceErr == nil {
		serviceErr = newServiceError(protocolwire.ErrorCode_ERROR_CODE_INTERNAL,
			"service.handler_failed", "Plugin service handler failed.")
	}
	code := serviceErr.Code
	if code == protocolwire.ErrorCode_ERROR_CODE_UNSPECIFIED {
		code = protocolwire.ErrorCode_ERROR_CODE_INTERNAL
	}
	detail := &protocolwire.ErrorDetail{
		Code: code, Reason: serviceErr.Reason, Message: serviceErr.Message,
		Retryable: serviceErr.Retryable,
	}
	if !serviceErr.RetryAfter.IsZero() {
		detail.RetryAfter = timestamppb.New(serviceErr.RetryAfter)
	}
	if len(serviceErr.Metadata) > 0 {
		detail.Metadata = make(map[string]string, len(serviceErr.Metadata))
		for key, value := range serviceErr.Metadata {
			detail.Metadata[key] = value
		}
	}
	return detail
}

func serviceErrorFromDetail(detail *protocolwire.ErrorDetail, remote bool) *ServiceError {
	if detail == nil {
		return newServiceError(protocolwire.ErrorCode_ERROR_CODE_INTERNAL,
			"service.remote_error_invalid", "Remote service stream returned an invalid error.")
	}
	result := &ServiceError{
		Code: detail.GetCode(), Reason: detail.GetReason(), Message: detail.GetMessage(),
		Retryable: detail.GetRetryable(), Metadata: make(map[string]string, len(detail.GetMetadata())), remote: remote,
	}
	if retry := detail.GetRetryAfter(); retry != nil && retry.IsValid() {
		result.RetryAfter = retry.AsTime()
	}
	for key, value := range detail.GetMetadata() {
		result.Metadata[key] = value
	}
	return result
}

func newServiceError(code protocolwire.ErrorCode, reason, message string) *ServiceError {
	return &ServiceError{Code: code, Reason: reason, Message: message}
}

func sendServiceStreamError(
	stream grpc.BidiStreamingServer[pluginwire.ServiceStreamFrame, pluginwire.ServiceStreamFrame],
	err error,
) error {
	return sendServiceStreamDetail(stream, serviceErrorDetail(err))
}

func sendServiceStreamDetail(
	stream grpc.BidiStreamingServer[pluginwire.ServiceStreamFrame, pluginwire.ServiceStreamFrame],
	detail *protocolwire.ErrorDetail,
) error {
	if err := stream.Context().Err(); err != nil {
		return err
	}
	return stream.Send(&pluginwire.ServiceStreamFrame{Frame: &pluginwire.ServiceStreamFrame_Error{
		Error: proto.Clone(detail).(*protocolwire.ErrorDetail),
	}})
}

func serviceRequestContext(request *pluginwire.ServiceRequest) *protocolwire.RequestContext {
	if request == nil {
		return nil
	}
	return request.GetContext()
}

func serviceRegistryKey(serviceID, version string) string { return serviceID + "\x00" + version }

func cloneServiceDescriptor(value *protocolwire.ServiceDescriptor) *protocolwire.ServiceDescriptor {
	if value == nil {
		return nil
	}
	return proto.Clone(value).(*protocolwire.ServiceDescriptor)
}

func cloneRequestContext(value *protocolwire.RequestContext) *protocolwire.RequestContext {
	if value == nil {
		return nil
	}
	return proto.Clone(value).(*protocolwire.RequestContext)
}

func cloneTypedDocument(value *protocolwire.TypedDocument) *protocolwire.TypedDocument {
	if value == nil {
		return nil
	}
	return proto.Clone(value).(*protocolwire.TypedDocument)
}

func serviceHandshakeDescriptors(manual []*protocolwire.ServiceDescriptor, registry *ServiceRegistry) []*protocolwire.ServiceDescriptor {
	if registry == nil {
		return cloneServices(manual)
	}
	result := registry.Descriptors()
	seen := make(map[string]struct{}, len(result))
	for _, descriptor := range result {
		seen[serviceRegistryKey(descriptor.GetServiceId(), descriptor.GetVersion())] = struct{}{}
	}
	for _, descriptor := range manual {
		if descriptor == nil {
			continue
		}
		if _, exists := seen[serviceRegistryKey(descriptor.GetServiceId(), descriptor.GetVersion())]; exists {
			continue
		}
		result = append(result, cloneServiceDescriptor(descriptor))
	}
	return result
}
