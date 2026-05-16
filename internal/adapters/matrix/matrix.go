package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
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

type LoginRequest struct {
	UserID                   string
	Password                 string
	DeviceID                 string
	InitialDeviceDisplayName string
}

type LoginResponse struct {
	UserID      string `json:"user_id"`
	AccessToken string `json:"access_token"`
	DeviceID    string `json:"device_id"`
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
	MsgType       string `json:"msgtype"`
	Body          string `json:"body"`
	Format        string `json:"format,omitempty"`
	FormattedBody string `json:"formatted_body,omitempty"`
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
			response, err := a.Core.HandleText(ctx, core.AdapterRequest{
				Adapter:   model.AdapterMatrix,
				EventID:   event.EventID,
				Sender:    event.Sender,
				SourceKey: matrixSourceKey(roomID, event.Sender),
				Text:      event.Content.Body,
			})
			if err != nil {
				if sendErr := a.Client.SendText(ctx, roomID, "OpenWhisker error: "+err.Error()); sendErr != nil {
					return "", sendErr
				}
				continue
			}
			if shouldSendImmediateResponse(response) {
				if err := a.Client.SendText(ctx, roomID, formatAdapterResponse(response)); err != nil {
					return "", err
				}
			}
		}
	}
	if err := a.DeliverOutbox(ctx, a.RoomID); err != nil {
		return "", err
	}
	return sync.NextBatch, nil
}

func matrixSourceKey(roomID, senderID string) string {
	return model.AdapterMatrix + ":" + roomID + ":" + senderID
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
	payload, err := json.Marshal(newTextMessage(body))
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

func (c Client) LoginPassword(ctx context.Context, login LoginRequest) (LoginResponse, error) {
	var response LoginResponse
	if strings.TrimSpace(login.UserID) == "" {
		return response, fmt.Errorf("matrix login user id is required")
	}
	if login.Password == "" {
		return response, fmt.Errorf("matrix login password is required")
	}
	endpoint, err := c.endpoint("/_matrix/client/v3/login")
	if err != nil {
		return response, err
	}
	payload := map[string]any{
		"type": "m.login.password",
		"identifier": map[string]string{
			"type": "m.id.user",
			"user": strings.TrimSpace(login.UserID),
		},
		"password": login.Password,
	}
	if strings.TrimSpace(login.DeviceID) != "" {
		payload["device_id"] = strings.TrimSpace(login.DeviceID)
	}
	if strings.TrimSpace(login.InitialDeviceDisplayName) != "" {
		payload["initial_device_display_name"] = strings.TrimSpace(login.InitialDeviceDisplayName)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return response, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return response, err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := c.doJSON(req, &response); err != nil {
		return LoginResponse{}, err
	}
	if strings.TrimSpace(response.AccessToken) == "" {
		return LoginResponse{}, fmt.Errorf("matrix login response did not include access_token")
	}
	return response, nil
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

func shouldSendImmediateResponse(response core.AdapterResponse) bool {
	if response.Duplicate || strings.TrimSpace(response.Body) == "" {
		return false
	}
	return response.OutboxKind == "" || response.OutboxKind == model.OutboxKindDiff
}

func formatAdapterResponse(response core.AdapterResponse) string {
	if response.OutboxKind == "" {
		return response.Body
	}
	return fmt.Sprintf("[%s] %s", response.OutboxKind, response.Body)
}

func newTextMessage(body string) MessageContent {
	content := MessageContent{MsgType: "m.text", Body: body}
	if formatted := matrixHTML(body); formatted != "" && formatted != html.EscapeString(body) {
		content.Format = "org.matrix.custom.html"
		content.FormattedBody = formatted
	}
	return content
}

func matrixHTML(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	var out []string
	inList := false
	inCode := false
	var codeLines []string
	closeList := func() {
		if inList {
			out = append(out, "</ul>")
			inList = false
		}
	}
	closeCode := func() {
		if inCode {
			out = append(out, "<pre><code>"+html.EscapeString(strings.Join(codeLines, "\n"))+"</code></pre>")
			codeLines = nil
			inCode = false
		}
	}
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inCode {
				closeCode()
			} else {
				closeList()
				inCode = true
				codeLines = nil
			}
			continue
		}
		if inCode {
			codeLines = append(codeLines, line)
			continue
		}
		if trimmed == "" {
			closeList()
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			if !inList {
				out = append(out, "<ul>")
				inList = true
			}
			out = append(out, "<li>"+html.EscapeString(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))+"</li>")
			continue
		}
		closeList()
		switch {
		case strings.HasPrefix(trimmed, "### "):
			out = append(out, "<strong>"+html.EscapeString(strings.TrimSpace(strings.TrimPrefix(trimmed, "### ")))+"</strong>")
		case strings.HasPrefix(trimmed, "## "):
			out = append(out, "<strong>"+html.EscapeString(strings.TrimSpace(strings.TrimPrefix(trimmed, "## ")))+"</strong>")
		case strings.HasPrefix(trimmed, "# "):
			out = append(out, "<strong>"+html.EscapeString(strings.TrimSpace(strings.TrimPrefix(trimmed, "# ")))+"</strong>")
		default:
			out = append(out, html.EscapeString(trimmed))
		}
	}
	closeCode()
	closeList()
	return strings.Join(out, "<br>")
}
