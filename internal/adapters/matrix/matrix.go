package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/core"
	"github.com/scarletmu/openwhisker/internal/model"
)

type Core interface {
	HandleText(context.Context, core.AdapterRequest) (core.AdapterResponse, error)
	PullOutbox(limit int) ([]model.OutboxMessage, error)
	MarkOutboxDelivered(id string) error
}

type Adapter struct {
	Core   Core
	Client Client
	UserID string
	RoomID string
}

type Client struct {
	Homeserver  string
	AccessToken string
	HTTPClient  *http.Client
}

type SyncResponse struct {
	NextBatch string `json:"next_batch"`
	Rooms     struct {
		Join map[string]JoinedRoom `json:"join"`
	} `json:"rooms"`
}

type JoinedRoom struct {
	Timeline struct {
		Events []Event `json:"events"`
	} `json:"timeline"`
}

type Event struct {
	Type    string         `json:"type"`
	EventID string         `json:"event_id"`
	Sender  string         `json:"sender"`
	Content MessageContent `json:"content"`
}

type MessageContent struct {
	MsgType string `json:"msgtype"`
	Body    string `json:"body"`
}

func (a Adapter) PollOnce(ctx context.Context, since string, timeout time.Duration) (string, error) {
	if a.Core == nil {
		return "", fmt.Errorf("matrix adapter core is required")
	}
	if a.RoomID == "" {
		return "", fmt.Errorf("matrix adapter room id is required")
	}
	sync, err := a.Client.Sync(ctx, since, timeout)
	if err != nil {
		return "", err
	}
	for roomID, room := range sync.Rooms.Join {
		if roomID != a.RoomID {
			continue
		}
		for _, event := range room.Timeline.Events {
			if !isTextMessage(event) || event.Sender == a.UserID {
				continue
			}
			_, err := a.Core.HandleText(ctx, core.AdapterRequest{
				Adapter: model.AdapterMatrix,
				EventID: event.EventID,
				Sender:  event.Sender,
				Text:    event.Content.Body,
			})
			if err != nil {
				if sendErr := a.Client.SendText(ctx, roomID, "OpenWhisker error: "+err.Error()); sendErr != nil {
					return "", sendErr
				}
				continue
			}
		}
	}
	if err := a.DeliverOutbox(ctx, a.RoomID); err != nil {
		return "", err
	}
	return sync.NextBatch, nil
}

func (a Adapter) DeliverOutbox(ctx context.Context, roomID string) error {
	messages, err := a.Core.PullOutbox(20)
	if err != nil {
		return err
	}
	for _, msg := range messages {
		if err := a.Client.SendText(ctx, roomID, formatOutboxMessage(msg)); err != nil {
			return err
		}
		if err := a.Core.MarkOutboxDelivered(msg.ID); err != nil {
			return err
		}
	}
	return nil
}

func (c Client) Sync(ctx context.Context, since string, timeout time.Duration) (SyncResponse, error) {
	var response SyncResponse
	endpoint, err := c.endpoint("/_matrix/client/v3/sync")
	if err != nil {
		return response, err
	}
	query := endpoint.Query()
	if since != "" {
		query.Set("since", since)
	}
	if timeout > 0 {
		query.Set("timeout", fmt.Sprintf("%d", timeout.Milliseconds()))
	}
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return response, err
	}
	c.authorize(req)
	if err := c.doJSON(req, &response); err != nil {
		return SyncResponse{}, err
	}
	return response, nil
}

func (c Client) SendText(ctx context.Context, roomID, body string) error {
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/send/m.room.message/%d",
		url.PathEscape(roomID), time.Now().UnixNano())
	endpoint, err := c.endpoint(path)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(MessageContent{MsgType: "m.text", Body: body})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)
	return c.doJSON(req, nil)
}

func (c Client) endpoint(path string) (*url.URL, error) {
	if strings.TrimSpace(c.Homeserver) == "" {
		return nil, fmt.Errorf("matrix homeserver is required")
	}
	base, err := url.Parse(strings.TrimRight(c.Homeserver, "/"))
	if err != nil {
		return nil, err
	}
	rel, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	return base.ResolveReference(rel), nil
}

func (c Client) authorize(req *http.Request) {
	if c.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	}
}

func (c Client) doJSON(req *http.Request, out any) error {
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("matrix %s %s failed: %s: %s", req.Method, req.URL.Path, resp.Status, strings.TrimSpace(string(body)))
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return err
	}
	return nil
}

func isTextMessage(event Event) bool {
	return event.Type == "m.room.message" && event.Content.MsgType == "m.text" && strings.TrimSpace(event.Content.Body) != ""
}

func formatOutboxMessage(msg model.OutboxMessage) string {
	if msg.Kind == "" {
		return msg.Body
	}
	return fmt.Sprintf("[%s] %s", msg.Kind, msg.Body)
}
