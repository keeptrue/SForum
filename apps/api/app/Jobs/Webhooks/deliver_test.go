package webhookjobs

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	webhooks "github.com/keeptrue/sforum/apps/api/app/Models/Webhooks"
)

func TestDeliverWorkerSignsAndSucceeds(t *testing.T) {
	var gotSig, gotEvent string
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-SForum-Signature")
		gotEvent = r.Header.Get("X-SForum-Event")
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`ok`))
	}))
	defer server.Close()

	store := &fakeStore{
		delivery: webhooks.Delivery{
			ID: 9, EndpointID: 1, EventName: "topic.created", Status: webhooks.StatusQueued,
			Payload: []byte(`{"name":"topic.created"}`),
		},
		endpoint: webhooks.EndpointRecord{
			// 测试注入 Client 绕过 Dial SSRF；TargetURL 仍须通过字面量公网 IP 校验。
			Endpoint: webhooks.Endpoint{ID: 1, TargetURL: "https://8.8.8.8/hook", Enabled: true},
			Secret:   "whsec_test",
		},
	}
	// 用自定义 Transport 把请求转到本地 server，模拟公网可达。
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			req.URL.Scheme = "http"
			req.URL.Host = server.Listener.Addr().String()
			return server.Client().Transport.RoundTrip(req)
		}),
	}
	fixed := time.Unix(1_700_000_000, 0).UTC()
	worker := &DeliverWorker{Store: store, Client: client, AllowHTTP: false, Now: func() time.Time { return fixed }}
	if err := worker.Work(context.Background(), &river.Job[DeliverArgs]{Args: DeliverArgs{DeliveryID: 9}}); err != nil {
		t.Fatal(err)
	}
	if store.update.Status != webhooks.StatusSent {
		t.Fatalf("status=%s", store.update.Status)
	}
	if gotEvent != "topic.created" {
		t.Fatalf("event header=%q", gotEvent)
	}
	want := signPayload("whsec_test", fixed.Unix(), body)
	if gotSig != want {
		t.Fatalf("sig=%q want %q", gotSig, want)
	}
}

func TestDeliverWorkerRetriesOn5xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	store := &fakeStore{
		delivery: webhooks.Delivery{ID: 2, EndpointID: 1, Status: webhooks.StatusQueued, Payload: []byte(`{}`)},
		endpoint: webhooks.EndpointRecord{Endpoint: webhooks.Endpoint{ID: 1, TargetURL: "https://8.8.8.8/hook", Enabled: true}},
	}
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			req.URL.Scheme = "http"
			req.URL.Host = server.Listener.Addr().String()
			return server.Client().Transport.RoundTrip(req)
		}),
	}
	worker := &DeliverWorker{Store: store, Client: client}
	err := worker.Work(context.Background(), &river.Job[DeliverArgs]{Args: DeliverArgs{DeliveryID: 2}})
	if err == nil {
		t.Fatal("expected retryable error")
	}
	if store.update.Status != webhooks.StatusSending {
		t.Fatalf("status=%s", store.update.Status)
	}
}

// 预算内的最后一次尝试必须真的发出去（RV-52）。
// 旧实现先算 attempt = AttemptCount+1 再用 attempt >= max 判 dead，于是第 5 次
// 进入即判死、一次请求都没发，实际最多只发 4 次。
func TestDeliverWorkerSendsOnFinalBudgetedAttempt(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	store := &fakeStore{
		delivery: webhooks.Delivery{ID: 5, EndpointID: 1, Status: webhooks.StatusSending, AttemptCount: 4, Payload: []byte(`{}`)},
		endpoint: webhooks.EndpointRecord{Endpoint: webhooks.Endpoint{ID: 1, TargetURL: "https://8.8.8.8/hook", Enabled: true}},
	}
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			req.URL.Scheme = "http"
			req.URL.Host = server.Listener.Addr().String()
			return server.Client().Transport.RoundTrip(req)
		}),
	}
	worker := &DeliverWorker{Store: store, Client: client}
	err := worker.Work(context.Background(), &river.Job[DeliverArgs]{
		JobRow: &rivertype.JobRow{Attempt: 5, MaxAttempts: 5},
		Args:   DeliverArgs{DeliveryID: 5},
	})
	if calls != 1 {
		t.Fatalf("final budgeted attempt issued %d HTTP requests, want 1", calls)
	}
	// 最后一次尝试失败后不再有调度：必须落终态 dead，而不是停在 sending。
	if err != nil {
		t.Fatalf("final budgeted attempt must settle terminally, got %v", err)
	}
	if store.update.Status != webhooks.StatusDead {
		t.Fatalf("status after the final failed attempt = %s, want dead", store.update.Status)
	}
}

// 预算用尽后（理论上不会再被调度）仍保持防御：不发出第 6 次请求。
func TestDeliverWorkerDoesNotExceedAttemptBudget(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	store := &fakeStore{
		delivery: webhooks.Delivery{ID: 6, EndpointID: 1, Status: webhooks.StatusSending, AttemptCount: 5, Payload: []byte(`{}`)},
		endpoint: webhooks.EndpointRecord{Endpoint: webhooks.Endpoint{ID: 1, TargetURL: "https://8.8.8.8/hook", Enabled: true}},
	}
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			req.URL.Scheme = "http"
			req.URL.Host = server.Listener.Addr().String()
			return server.Client().Transport.RoundTrip(req)
		}),
	}
	worker := &DeliverWorker{Store: store, Client: client}
	if err := worker.Work(context.Background(), &river.Job[DeliverArgs]{Args: DeliverArgs{DeliveryID: 6}}); err != nil {
		t.Fatalf("over-budget attempt: %v", err)
	}
	if calls != 0 {
		t.Fatalf("over-budget attempt issued %d HTTP requests, want 0", calls)
	}
	if store.update.Status != webhooks.StatusDead {
		t.Fatalf("status = %s, want dead", store.update.Status)
	}
}

func TestDeliverWorkerBlocksPrivateTarget(t *testing.T) {
	store := &fakeStore{
		delivery: webhooks.Delivery{ID: 3, EndpointID: 1, Status: webhooks.StatusQueued, Payload: []byte(`{}`)},
		endpoint: webhooks.EndpointRecord{
			Endpoint: webhooks.Endpoint{ID: 1, TargetURL: "http://127.0.0.1/steal", Enabled: true},
			Secret:   "whsec",
		},
	}
	worker := &DeliverWorker{Store: store, AllowHTTP: true}
	if err := worker.Work(context.Background(), &river.Job[DeliverArgs]{Args: DeliverArgs{DeliveryID: 3}}); err != nil {
		t.Fatalf("ssrf should fail permanently without river retry: %v", err)
	}
	if store.update.Status != webhooks.StatusFailed || store.update.Reason != "ssrf_blocked" {
		t.Fatalf("update=%#v", store.update)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSignPayloadFormat(t *testing.T) {
	ts := int64(100)
	body := []byte(`{"a":1}`)
	sig := signPayload("secret", ts, body)
	if !strings.HasPrefix(sig, "t=100,v1=") {
		t.Fatalf("sig=%s", sig)
	}
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = fmt.Fprintf(mac, "%d.", ts)
	_, _ = mac.Write(body)
	want := "t=" + strconv.FormatInt(ts, 10) + ",v1=" + hex.EncodeToString(mac.Sum(nil))
	if sig != want {
		t.Fatalf("got %s want %s", sig, want)
	}
}

type fakeStore struct {
	delivery webhooks.Delivery
	endpoint webhooks.EndpointRecord
	update   webhooks.DeliveryUpdate
}

func (s *fakeStore) GetDelivery(context.Context, int64) (webhooks.Delivery, error) {
	return s.delivery, nil
}
func (s *fakeStore) UpdateDelivery(_ context.Context, update webhooks.DeliveryUpdate) error {
	s.update = update
	s.delivery.Status = update.Status
	s.delivery.AttemptCount = update.AttemptCount
	return nil
}
func (s *fakeStore) GetEndpoint(context.Context, int64) (webhooks.EndpointRecord, error) {
	return s.endpoint, nil
}
