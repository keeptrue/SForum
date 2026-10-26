package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// streamRoute 执行 SSE（events）与 stream（stream）模式的真实分帧响应。
// WebSocket 由平台公共测试覆盖，本参考矩阵在文档/测试中声明覆盖来源。
func (s *commerceServer) streamRoute(stream *pluginv2.RouteStream) error {
	if stream == nil || stream.Open() == nil {
		return fmt.Errorf("missing route stream open")
	}
	open := stream.Open()
	routeID := open.GetRouteId()
	switch routeID {
	case routeEventsID:
		return s.streamSSE(stream)
	case routeStreamID:
		return s.streamBinary(stream)
	default:
		return stream.Close(&pluginwire.RouteStreamClose{
			StatusCode: http.StatusNotFound,
			Error: &protocolwire.ErrorDetail{
				Code: protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND, Reason: "commerce.stream_route_unknown",
			},
		})
	}
}

func (s *commerceServer) streamSSE(stream *pluginv2.RouteStream) error {
	// 消费请求侧直到 EOF（SSE GET 通常无 body）。
	for {
		_, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	events := []string{
		"event: commerce\ndata: {\"type\":\"order.opened\",\"orderId\":\"ord-1\"}\n\n",
		"event: commerce\ndata: {\"type\":\"order.settled\",\"orderId\":\"ord-1\"}\n\n",
	}
	for i, event := range events {
		if err := stream.Send(&protocolwire.DataChunk{
			Sequence: uint64(i + 1), Data: []byte(event), Final: i == len(events)-1,
		}); err != nil {
			return err
		}
	}
	return stream.Close(&pluginwire.RouteStreamClose{StatusCode: http.StatusOK})
}

func (s *commerceServer) streamBinary(stream *pluginv2.RouteStream) error {
	var received strings.Builder
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if chunk != nil {
			received.Write(chunk.GetData())
		}
	}
	payload := []byte("commerce-stream-ack:" + received.String())
	if err := stream.Send(&protocolwire.DataChunk{
		Sequence: 1, Data: payload, Final: true,
	}); err != nil {
		return err
	}
	return stream.Close(&pluginwire.RouteStreamClose{StatusCode: http.StatusOK})
}
