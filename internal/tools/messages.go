package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jawadh/moodle-mcp-server/internal/api"
)

// --- Get Notifications Tool ---

type Message struct {
	ID               int    `json:"id"`
	UserIDFrom       int    `json:"useridfrom"`
	UserFromFullName string `json:"userfromfullname"`
	Subject          string `json:"subject"`
	Text             string `json:"text"`
	FullMessage      string `json:"fullmessage"`
	TimeCreated      int64  `json:"timecreated"`
	TimeRead         int64  `json:"timeread"`
}

type MessageDisplay struct {
	ID        int    `json:"id"`
	From      string `json:"from"`
	Subject   string `json:"subject"`
	Preview   string `json:"preview"`
	Date      string `json:"date"`
	Read      bool   `json:"read"`
}

type MessagesResponse struct {
	Messages []Message `json:"messages"`
}

type GetNotificationsInput struct {
	Limit    int  `json:"limit,omitempty" jsonschema:"description=Maximum number of notifications to return (default: 20)"`
	UnreadOnly bool `json:"unread_only,omitempty" jsonschema:"description=Only show unread notifications (default: true)"`
}

func HandleGetNotifications(ctx context.Context, client *api.Client, input GetNotificationsInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}

	limit := input.Limit
	if limit <= 0 {
		limit = 20
	}

	userID := client.GetUserID()
	readType := "unread"
	if !input.UnreadOnly {
		readType = "both"
	}

	params := map[string]string{
		"useridto":     fmt.Sprintf("%d", userID),
		"type":         readType,
		"limitnum":     fmt.Sprintf("%d", limit),
		"newestfirst":  "1",
	}

	data, err := client.Call(ctx, "core_message_get_messages", params)
	if err != nil {
		return "", err
	}

	var resp MessagesResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("parsing messages: %w", err)
	}

	var display []MessageDisplay
	for _, m := range resp.Messages {
		preview := m.Text
		if preview == "" {
			preview = m.FullMessage
		}
		preview = truncate(stripHTML(preview), 150)

		display = append(display, MessageDisplay{
			ID:      m.ID,
			From:    m.UserFromFullName,
			Subject: m.Subject,
			Preview: preview,
			Date:    time.Unix(m.TimeCreated, 0).Format("2006-01-02 15:04"),
			Read:    m.TimeRead > 0,
		})
	}

	result, _ := json.MarshalIndent(map[string]interface{}{
		"total_messages": len(display),
		"messages":       display,
	}, "", "  ")
	return string(result), nil
}
