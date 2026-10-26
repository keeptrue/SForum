package pluginv2

import (
	"context"
	"testing"
	"time"

	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTransportConcurrencyGateRejectsBeforeDeadline(t *testing.T) {
	semaphore := make(chan struct{}, 1)
	semaphore <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	called := false
	_, err := unaryLimitInterceptor(time.Second, semaphore)(ctx, &protocolwire.HealthRequest{}, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
		called = true
		return nil, nil
	})
	if called || status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("called = %t, error = %v", called, err)
	}
}

func TestTransportDefaultsAndUpperBound(t *testing.T) {
	got := normalizeServeOptions(ServeOptions{MaxMessageBytes: DefaultMaxMessageBytes + 1})
	if got.MaxMessageBytes != DefaultMaxMessageBytes || got.MaxConcurrent != DefaultConcurrentCalls || got.DefaultTimeout != DefaultRequestTimeout {
		t.Fatalf("normalized options = %#v", got)
	}
}
