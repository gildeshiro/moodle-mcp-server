package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jawadh/moodle-mcp-server/internal/api"
)

// --- List Messages Tool ---

type ListMessagesInput struct {
	UnreadOnly bool `json:"unread_only,omitempty" jsonschema:"description=Only show unread messages (default: true)"`
	Limit      int  `json:"limit,omitempty" jsonschema:"description=Maximum number of messages to return (default: 20)"`
}

type rawListMessage struct {
	ID                int    `json:"id"`
	UserIDFrom        int    `json:"useridfrom"`
	UserIDTo          int    `json:"useridto"`
	FullNameFrom      string `json:"fullnamefrom"`
	UserFromFullName  string `json:"userfromfullname"`
	Subject           string `json:"subject"`
	Text              string `json:"text"`
	FullMessage       string `json:"fullmessage"`
	SmallMessage      string `json:"smallmessage"`
	TimeCreated       int64  `json:"timecreated"`
	TimeRead          int64  `json:"timeread"`
}

type messageListDisplay struct {
	ID          int    `json:"id"`
	FromUserID  int    `json:"from_user_id"`
	From        string `json:"from"`
	Subject     string `json:"subject,omitempty"`
	Preview     string `json:"preview"`
	Date        string `json:"date"`
	Read        bool   `json:"read"`
}

func HandleListMessages(ctx context.Context, client *api.Client, input ListMessagesInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}

	limit := input.Limit
	if limit <= 0 {
		limit = 20
	}

	userID := client.GetUserID()
	if userID == 0 {
		return "", fmt.Errorf("current user ID is unknown — please login first")
	}

	// core_message_get_messages: read parameter is a string in Moodle.
	// 0=unread, 1=read, 2=both.
	readParam := "0"
	if !input.UnreadOnly {
		readParam = "2"
	}

	params := map[string]string{
		"useridto":    fmt.Sprintf("%d", userID),
		"useridfrom":  "0",
		"type":        "conversations",
		"read":        readParam,
		"newestfirst": "1",
		"limitfrom":   "0",
		"limitnum":    fmt.Sprintf("%d", limit),
	}

	data, err := client.Call(ctx, "core_message_get_messages", params)
	if err != nil {
		return "", fmt.Errorf("listing messages: %w", err)
	}

	var resp struct {
		Messages []rawListMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("parsing messages: %w", err)
	}

	display := make([]messageListDisplay, 0, len(resp.Messages))
	for _, m := range resp.Messages {
		from := m.FullNameFrom
		if from == "" {
			from = m.UserFromFullName
		}
		preview := m.SmallMessage
		if preview == "" {
			preview = m.Text
		}
		if preview == "" {
			preview = m.FullMessage
		}
		preview = truncate(stripHTML(preview), 200)

		display = append(display, messageListDisplay{
			ID:         m.ID,
			FromUserID: m.UserIDFrom,
			From:       from,
			Subject:    m.Subject,
			Preview:    preview,
			Date:       time.Unix(m.TimeCreated, 0).Format("2006-01-02 15:04"),
			Read:       m.TimeRead > 0,
		})
	}

	out, _ := json.MarshalIndent(map[string]any{
		"unread_only":    input.UnreadOnly,
		"limit":          limit,
		"total_messages": len(display),
		"messages":       display,
	}, "", "  ")
	return string(out), nil
}

// --- Send Message Tool ---

type SendMessageInput struct {
	ToUserID int    `json:"to_user_id" jsonschema:"description=The Moodle user ID of the recipient"`
	Message  string `json:"message" jsonschema:"description=The message text to send (HTML supported)"`
}

func HandleSendMessage(ctx context.Context, client *api.Client, input SendMessageInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}
	if input.ToUserID == 0 {
		return "", fmt.Errorf("to_user_id is required")
	}
	if input.Message == "" {
		return "", fmt.Errorf("message is required")
	}

	params := map[string]string{
		"messages[0][touserid]":   fmt.Sprintf("%d", input.ToUserID),
		"messages[0][text]":       input.Message,
		"messages[0][textformat]": "1",
	}

	data, err := client.Call(ctx, "core_message_send_instant_messages", params)
	if err != nil {
		return "", fmt.Errorf("sending message: %w", err)
	}

	var raw []map[string]any
	_ = json.Unmarshal(data, &raw)

	out, _ := json.MarshalIndent(map[string]any{
		"success":     true,
		"to_user_id":  input.ToUserID,
		"results":     raw,
	}, "", "  ")
	return string(out), nil
}
