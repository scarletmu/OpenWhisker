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
	var sentFormatted string
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
			sentFormatted = content.FormattedBody
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
			Body:   "pending delivery\n- item",
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
	if !strings.Contains(sentFormatted, "<li>") {
		t.Fatalf("formatted body = %q, want Matrix HTML list", sentFormatted)
	}
	if core.delivered != "out_pending" {
		t.Fatalf("delivered = %q, want out_pending", core.delivered)
	}
}

func TestAdapterPollOnceSendsImmediateStatusResponse(t *testing.T) {
	var sentBody string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			return jsonResponse(`{
				"next_batch": "next",
				"rooms": {
					"join": {
						"!room:example.test": {
							"timeline": {
								"events": [{
									"type": "m.room.message",
									"event_id": "$status",
									"sender": "@user:example.test",
									"content": {"msgtype": "m.text", "body": "/status"}
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
	core := &fakeCore{response: core.AdapterResponse{Status: "ok", Body: "No jobs yet."}}
	adapter := Adapter{
		Core:   core,
		Client: Client{Homeserver: "https://matrix.example.test", HTTPClient: client},
		UserID: "@bot:example.test",
		RoomID: "!room:example.test",
	}

	if _, err := adapter.PollOnce(context.Background(), "", time.Second); err != nil {
		t.Fatal(err)
	}
	if sentBody != "No jobs yet." {
		t.Fatalf("sent body = %q, want immediate status response", sentBody)
	}
}

func TestAdapterPollOnceDoesNotDuplicatePersistedOutboxResponse(t *testing.T) {
	var sendCount int
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			return jsonResponse(`{
				"next_batch": "next",
				"rooms": {
					"join": {
						"!room:example.test": {
							"timeline": {
								"events": [{
									"type": "m.room.message",
									"event_id": "$organize",
									"sender": "@user:example.test",
									"content": {"msgtype": "m.text", "body": "/organize last"}
								}]
							}
						}
					}
				}
			}`), nil
		case http.MethodPut:
			sendCount++
			return jsonResponse(`{"event_id":"$reply"}`), nil
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
		return nil, nil
	})}
	core := &fakeCore{
		response: core.AdapterResponse{
			Status:     "awaiting_approval",
			Body:       "Plan awaits approval",
			OutboxKind: model.OutboxKindApproval,
		},
		outbox: []model.OutboxMessage{{
			ID:     "out_approval",
			Kind:   model.OutboxKindApproval,
			Body:   "Plan awaits approval",
			Status: model.OutboxStatusPending,
		}},
	}
	adapter := Adapter{
		Core:   core,
		Client: Client{Homeserver: "https://matrix.example.test", HTTPClient: client},
		UserID: "@bot:example.test",
		RoomID: "!room:example.test",
	}

	if _, err := adapter.PollOnce(context.Background(), "", time.Second); err != nil {
		t.Fatal(err)
	}
	if sendCount != 1 {
		t.Fatalf("send count = %d, want only persisted outbox delivery", sendCount)
	}
}

func TestClientLoginPassword(t *testing.T) {
	var sawLogin bool
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/_matrix/client/v3/login" {
			t.Fatalf("path = %s, want login", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatalf("Authorization = %q, want empty", r.Header.Get("Authorization"))
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["type"] != "m.login.password" || payload["password"] != "secret" {
			t.Fatalf("payload = %+v, want password login", payload)
		}
		identifier, ok := payload["identifier"].(map[string]any)
		if !ok || identifier["user"] != "@bot:example.test" {
			t.Fatalf("identifier = %+v, want bot user", payload["identifier"])
		}
		if payload["device_id"] != "OWDEVICE" {
			t.Fatalf("device_id = %v, want OWDEVICE", payload["device_id"])
		}
		sawLogin = true
		return jsonResponse(`{"user_id":"@bot:example.test","access_token":"token","device_id":"OWDEVICE"}`), nil
	})}

	response, err := (Client{
		Homeserver: "https://matrix.example.test",
		HTTPClient: client,
	}).LoginPassword(context.Background(), LoginRequest{
		UserID:                   "@bot:example.test",
		Password:                 "secret",
		DeviceID:                 "OWDEVICE",
		InitialDeviceDisplayName: "OpenWhisker Matrix Adapter",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawLogin {
		t.Fatal("login endpoint was not called")
	}
	if response.AccessToken != "token" || response.UserID != "@bot:example.test" || response.DeviceID != "OWDEVICE" {
		t.Fatalf("response = %+v, want login response", response)
	}
}

func TestNewTextMessageKeepsPlainBodyAndAddsFormattedHTML(t *testing.T) {
	content := newTextMessage("Diff for plan plan_1\n- create_note Knowledge/Drafts/a.md\n```text\nhello <world>\n```")
	if content.Body == "" {
		t.Fatal("body is empty")
	}
	if content.Format != "org.matrix.custom.html" {
		t.Fatalf("format = %q, want Matrix custom HTML", content.Format)
	}
	if !strings.Contains(content.FormattedBody, "<li>create_note Knowledge/Drafts/a.md</li>") {
		t.Fatalf("formatted body = %q, want list item", content.FormattedBody)
	}
	if !strings.Contains(content.FormattedBody, "hello &lt;world&gt;") {
		t.Fatalf("formatted body = %q, want escaped code content", content.FormattedBody)
	}
}

type fakeCore struct {
	handled   core.AdapterRequest
	response  core.AdapterResponse
	outbox    []model.OutboxMessage
	delivered string
}

func (c *fakeCore) HandleText(_ context.Context, req core.AdapterRequest) (core.AdapterResponse, error) {
	c.handled = req
	if c.response.Status != "" || c.response.Body != "" {
		return c.response, nil
	}
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
