package pluginv2

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// RouteStream exposes route chunks after the SDK consumes and authenticates
// the mandatory opening frame. grpc-go permits one concurrent sender and one
// concurrent receiver; the wrapper serializes multiple senders.
type RouteStream struct {
	stream routeFrameStream
	open   *pluginwire.RouteStreamOpen

	sendMu     sync.Mutex
	sendClosed bool
	peerMu     sync.RWMutex
	peerClose  *pluginwire.RouteStreamClose
}

func (s *RouteStream) Context() context.Context {
	if s == nil || s.stream == nil {
		return context.Background()
	}
	return s.stream.Context()
}

func (s *RouteStream) Open() *pluginwire.RouteStreamOpen {
	if s == nil || s.open == nil {
		return nil
	}
	return proto.Clone(s.open).(*pluginwire.RouteStreamOpen)
}

func (s *RouteStream) Recv() (*protocolwire.DataChunk, error) {
	if s == nil || s.stream == nil {
		return nil, errors.New("plugin route stream is nil")
	}
	frame, err := s.stream.Recv()
	if err != nil {
		return nil, err
	}
	if chunk := frame.GetChunk(); chunk != nil {
		return cloneDataChunk(chunk), nil
	}
	if closeFrame := frame.GetClose(); closeFrame != nil {
		s.peerMu.Lock()
		s.peerClose = proto.Clone(closeFrame).(*pluginwire.RouteStreamClose)
		s.peerMu.Unlock()
		if closeFrame.GetError() != nil {
			return nil, runtimeStreamErrorFromDetail(closeFrame.GetError())
		}
		return nil, io.EOF
	}
	return nil, invalidRuntimeStream("runtime.route_frame_invalid", "Only chunk or close frames are allowed after route stream open.")
}

func (s *RouteStream) PeerClose() *pluginwire.RouteStreamClose {
	if s == nil {
		return nil
	}
	s.peerMu.RLock()
	defer s.peerMu.RUnlock()
	if s.peerClose == nil {
		return nil
	}
	return proto.Clone(s.peerClose).(*pluginwire.RouteStreamClose)
}

func (s *RouteStream) Send(chunk *protocolwire.DataChunk) error {
	if chunk == nil {
		return invalidRuntimeStream("runtime.route_chunk_required", "A route data chunk is required.")
	}
	return s.send(&pluginwire.RouteStreamFrame{Frame: &pluginwire.RouteStreamFrame_Chunk{Chunk: cloneDataChunk(chunk)}}, false)
}

func (s *RouteStream) Close(closeFrame *pluginwire.RouteStreamClose) error {
	if closeFrame == nil {
		return invalidRuntimeStream("runtime.route_close_required", "A route close frame is required.")
	}
	return s.send(&pluginwire.RouteStreamFrame{Frame: &pluginwire.RouteStreamFrame_Close{
		Close: proto.Clone(closeFrame).(*pluginwire.RouteStreamClose),
	}}, true)
}

func (s *RouteStream) send(frame *pluginwire.RouteStreamFrame, closes bool) error {
	if s == nil || s.stream == nil {
		return errors.New("plugin route stream is nil")
	}
	if err := s.Context().Err(); err != nil {
		return err
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.sendClosed {
		return invalidRuntimeStream("runtime.route_send_closed", "The route response direction is already closed.")
	}
	if err := s.stream.Send(frame); err != nil {
		return err
	}
	if closes {
		s.sendClosed = true
	}
	return nil
}

func (s *RouteStream) closeWithError(err error) error {
	if s == nil || s.stream == nil {
		return errors.New("plugin route stream is nil")
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.sendClosed {
		return nil
	}
	if contextErr := s.Context().Err(); contextErr != nil {
		return contextErr
	}
	if sendErr := s.stream.Send(&pluginwire.RouteStreamFrame{Frame: &pluginwire.RouteStreamFrame_Close{
		Close: &pluginwire.RouteStreamClose{Error: runtimeStreamErrorDetail(err)},
	}}); sendErr != nil {
		return sendErr
	}
	s.sendClosed = true
	return nil
}

// FileStream is the symmetric bounded-frame helper for TransferFile.
type FileStream struct {
	stream fileFrameStream
	open   *pluginwire.FileOpen

	sendMu     sync.Mutex
	sendClosed bool
	peerMu     sync.RWMutex
	peerClose  *pluginwire.FileClose
}

type routeFrameStream interface {
	Context() context.Context
	Send(*pluginwire.RouteStreamFrame) error
	Recv() (*pluginwire.RouteStreamFrame, error)
}

type fileFrameStream interface {
	Context() context.Context
	Send(*pluginwire.FileFrame) error
	Recv() (*pluginwire.FileFrame, error)
}

func (s *FileStream) Context() context.Context {
	if s == nil || s.stream == nil {
		return context.Background()
	}
	return s.stream.Context()
}

func (s *FileStream) Open() *pluginwire.FileOpen {
	if s == nil || s.open == nil {
		return nil
	}
	return proto.Clone(s.open).(*pluginwire.FileOpen)
}

func (s *FileStream) Recv() (*protocolwire.DataChunk, error) {
	if s == nil || s.stream == nil {
		return nil, errors.New("plugin file stream is nil")
	}
	frame, err := s.stream.Recv()
	if err != nil {
		return nil, err
	}
	if chunk := frame.GetChunk(); chunk != nil {
		return cloneDataChunk(chunk), nil
	}
	if closeFrame := frame.GetClose(); closeFrame != nil {
		s.peerMu.Lock()
		s.peerClose = proto.Clone(closeFrame).(*pluginwire.FileClose)
		s.peerMu.Unlock()
		if closeFrame.GetError() != nil {
			return nil, runtimeStreamErrorFromDetail(closeFrame.GetError())
		}
		return nil, io.EOF
	}
	return nil, invalidRuntimeStream("runtime.file_frame_invalid", "Only chunk or close frames are allowed after file stream open.")
}

func (s *FileStream) PeerClose() *pluginwire.FileClose {
	if s == nil {
		return nil
	}
	s.peerMu.RLock()
	defer s.peerMu.RUnlock()
	if s.peerClose == nil {
		return nil
	}
	return proto.Clone(s.peerClose).(*pluginwire.FileClose)
}

func (s *FileStream) Send(chunk *protocolwire.DataChunk) error {
	if chunk == nil {
		return invalidRuntimeStream("runtime.file_chunk_required", "A file data chunk is required.")
	}
	return s.send(&pluginwire.FileFrame{Frame: &pluginwire.FileFrame_Chunk{Chunk: cloneDataChunk(chunk)}}, false)
}

func (s *FileStream) Close(closeFrame *pluginwire.FileClose) error {
	if closeFrame == nil {
		return invalidRuntimeStream("runtime.file_close_required", "A file close frame is required.")
	}
	return s.send(&pluginwire.FileFrame{Frame: &pluginwire.FileFrame_Close{
		Close: proto.Clone(closeFrame).(*pluginwire.FileClose),
	}}, true)
}

func (s *FileStream) send(frame *pluginwire.FileFrame, closes bool) error {
	if s == nil || s.stream == nil {
		return errors.New("plugin file stream is nil")
	}
	if err := s.Context().Err(); err != nil {
		return err
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.sendClosed {
		return invalidRuntimeStream("runtime.file_send_closed", "The file response direction is already closed.")
	}
	if err := s.stream.Send(frame); err != nil {
		return err
	}
	if closes {
		s.sendClosed = true
	}
	return nil
}

func (s *FileStream) closeWithError(err error) error {
	if s == nil || s.stream == nil {
		return errors.New("plugin file stream is nil")
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.sendClosed {
		return nil
	}
	if contextErr := s.Context().Err(); contextErr != nil {
		return contextErr
	}
	if sendErr := s.stream.Send(&pluginwire.FileFrame{Frame: &pluginwire.FileFrame_Close{
		Close: &pluginwire.FileClose{Error: runtimeStreamErrorDetail(err)},
	}}); sendErr != nil {
		return sendErr
	}
	s.sendClosed = true
	return nil
}

func (s *Server) StreamRoute(stream grpc.BidiStreamingServer[pluginwire.RouteStreamFrame, pluginwire.RouteStreamFrame]) error {
	s.mu.RLock()
	handler := s.streams.Route
	s.mu.RUnlock()
	if handler == nil {
		return s.UnimplementedPluginRuntimeServiceServer.StreamRoute(stream)
	}
	if stream == nil {
		return errors.New("plugin route stream is nil")
	}
	first, err := stream.Recv()
	if err != nil {
		if stream.Context().Err() != nil {
			return stream.Context().Err()
		}
		return (&RouteStream{stream: stream}).closeWithError(
			invalidRuntimeStream("runtime.route_open_required", "The first route stream frame must be an open frame."),
		)
	}
	open := first.GetOpen()
	route := &RouteStream{stream: stream, open: cloneRouteOpen(open)}
	if open == nil {
		return route.closeWithError(invalidRuntimeStream("runtime.route_open_required", "The first route stream frame must be an open frame."))
	}
	if detail := validateRuntimeClientContext(open.GetContext()); detail != nil {
		return route.closeWithError(runtimeStreamErrorFromDetail(detail))
	}
	if detail := s.validateRuntimeContext(open.GetContext()); detail != nil {
		return route.closeWithError(runtimeStreamErrorFromDetail(detail))
	}
	if strings.TrimSpace(open.GetRouteId()) == "" || strings.TrimSpace(open.GetContractVersion()) == "" ||
		strings.TrimSpace(open.GetMethod()) == "" || strings.TrimSpace(open.GetPath()) == "" {
		return route.closeWithError(invalidRuntimeStream("runtime.route_open_invalid", "Route id, contract version, method, and path are required."))
	}
	err = handler(route)
	return finishRouteStream(route, err)
}

func (s *Server) TransferFile(stream grpc.BidiStreamingServer[pluginwire.FileFrame, pluginwire.FileFrame]) error {
	s.mu.RLock()
	handler := s.streams.File
	s.mu.RUnlock()
	if handler == nil {
		return s.UnimplementedPluginRuntimeServiceServer.TransferFile(stream)
	}
	if stream == nil {
		return errors.New("plugin file stream is nil")
	}
	first, err := stream.Recv()
	if err != nil {
		if stream.Context().Err() != nil {
			return stream.Context().Err()
		}
		return (&FileStream{stream: stream}).closeWithError(
			invalidRuntimeStream("runtime.file_open_required", "The first file stream frame must be an open frame."),
		)
	}
	open := first.GetOpen()
	file := &FileStream{stream: stream, open: cloneFileOpen(open)}
	if open == nil {
		return file.closeWithError(invalidRuntimeStream("runtime.file_open_required", "The first file stream frame must be an open frame."))
	}
	if detail := validateRuntimeClientContext(open.GetContext()); detail != nil {
		return file.closeWithError(runtimeStreamErrorFromDetail(detail))
	}
	if detail := s.validateRuntimeContext(open.GetContext()); detail != nil {
		return file.closeWithError(runtimeStreamErrorFromDetail(detail))
	}
	if strings.TrimSpace(open.GetOperation()) == "" || strings.TrimSpace(open.GetFileId()) == "" {
		return file.closeWithError(invalidRuntimeStream("runtime.file_open_invalid", "File operation and file id are required."))
	}
	err = handler(file)
	return finishFileStream(file, err)
}

func finishRouteStream(stream *RouteStream, err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	if stream.Context().Err() != nil {
		return stream.Context().Err()
	}
	var runtimeErr *RuntimeStreamError
	if errors.As(err, &runtimeErr) && runtimeErr.remote {
		return nil
	}
	return stream.closeWithError(err)
}

func finishFileStream(stream *FileStream, err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	if stream.Context().Err() != nil {
		return stream.Context().Err()
	}
	var runtimeErr *RuntimeStreamError
	if errors.As(err, &runtimeErr) && runtimeErr.remote {
		return nil
	}
	return stream.closeWithError(err)
}

func cloneRouteOpen(value *pluginwire.RouteStreamOpen) *pluginwire.RouteStreamOpen {
	if value == nil {
		return nil
	}
	return proto.Clone(value).(*pluginwire.RouteStreamOpen)
}

func cloneFileOpen(value *pluginwire.FileOpen) *pluginwire.FileOpen {
	if value == nil {
		return nil
	}
	return proto.Clone(value).(*pluginwire.FileOpen)
}
