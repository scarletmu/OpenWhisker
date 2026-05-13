package matrix

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/scarletmu/openwhisker/internal/core"
	"github.com/scarletmu/openwhisker/internal/model"
)

func TestAdapterPollOnceRoutesTextThroughCoreAndDeliversOutbox(t *testing.T) {
	var sentBody string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("Authorization = %q, want bearer token", r.Header.Get("Authorization"))
		}
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path != "/_matrix/client/v3/sync" {
				t.Fatalf("GET path = %s", r.URL.Path)
			}
			return jsonResponse(`{
				"next_batch": "next",
				"rooms": {
					"join": {
						"!room:example.test": {
							"timeline": {
								"events": [{
									"type": "m.room.message",
									"event_id": "$event",
									"sender": "@user:example.test",
									"content": {"msgtype": "m.text", "body": "/organize last"}
								}]
							}
						}
					}
				}
			}`), nil
		case http.MethodPut:
			var content MessageContent
			if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
				t.Fatal(err)
			}
			sentBody = content.Body
			return jsonResponse(`{"event_id":"$reply"}`), nil
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
		return nil, nil
	})}

	core := &fakeCore{
		outbox: []model.OutboxMessage{{
			ID:     "out_1",
			JobID:  "job_1",
			Kind:   model.OutboxKindApproval,
			Body:   "Plan plan_1 awaits approval",
			Status: model.OutboxStatusPending,
		}},
	}
	adapter := Adapter{
		Core:   core,
		Client: Client{Homeserver: "https://matrix.example.test", AccessToken: "token", HTTPClient: client},
		UserID: "@bot:example.test",
		RoomID: "!room:example.test",
	}

	next, err := adapter.PollOnce(context.Background(), "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if next != "next" {
		t.Fatalf("next batch = %q, want next", next)
	}
	if core.handled.Text != "/organize last" || core.handled.EventID != "$event" {
		t.Fatalf("handled request = %+v, want event text", core.handled)
	}
	if !strings.Contains(sentBody, "Plan plan_1 awaits approval") {
		t.Fatalf("sent body = %q, want outbox body", sentBody)
	}
	if core.delivered != "out_1" {
		t.Fatalf("delivered = %q, want out_1", core.delivered)
	}
}

func TestAdapterPollOnceDeliversPendingOutboxWithoutEvents(t *testing.T) {
	var sentBody string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			return jsonResponse(`{
				"next_batch": "next",
				"rooms": {
					"join": {
						"!room:example.test": {
							"timeline": {"events": []}
						}
					}
				}
			}`), nil
		case http.MethodPut:
			var content MessageContent
			if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
				t.Fatal(err)
			}
			sentBody = content.Body
			return jsonResponse(`{"event_id":"$reply"}`), nil
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
		return nil, nil
	})}
	core := &fakeCore{
		outbox: []model.OutboxMessage{{
			ID:     "out_pending",
			Kind:   model.OutboxKindResult,
			Body:   "pending delivery",
			Status: model.OutboxStatusPending,
		}},
	}
	adapter := Adapter{
		Core:   core,
		Client: Client{Homeserver: "https://matrix.example.test", HTTPClient: client},
		UserID: "@bot:example.test",
		RoomID: "!room:example.test",
	}

	next, err := adapter.PollOnce(context.Background(), "old", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if next != "next" {
		t.Fatalf("next batch = %q, want next", next)
	}
	if core.handled.Text != "" {
		t.Fatalf("handled request = %+v, want none", core.handled)
	}
	if !strings.Contains(sentBody, "pending delivery") {
		t.Fatalf("sent body = %q, want pending outbox", sentBody)
	}
	if core.delivered != "out_pending" {
		t.Fatalf("delivered = %q, want out_pending", core.delivered)
	}
}

type fakeCore struct {
	handled   core.AdapterRequest
	outbox    []model.OutboxMessage
	delivered string
}

func (c *fakeCore) HandleText(_ context.Context, req core.AdapterRequest) (core.AdapterResponse, error) {
	c.handled = req
	return core.AdapterResponse{Status: "ok"}, nil
}

func (c *fakeCore) PullOutbox(_ int) ([]model.OutboxMessage, error) {
	return c.outbox, nil
}

func (c *fakeCore) MarkOutboxDelivered(id string) error {
	c.delivered = id
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
