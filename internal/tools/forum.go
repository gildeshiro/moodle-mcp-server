package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jawadh/moodle-mcp-server/internal/api"
)

// --- List Forums Tool ---

type ListForumsInput struct {
	CourseID int `json:"course_id" jsonschema:"description=The Moodle course ID to list forums for"`
}

type rawForum struct {
	ID             int    `json:"id"`
	CourseID       int    `json:"course"`
	Type           string `json:"type"`
	Name           string `json:"name"`
	Intro          string `json:"intro"`
	NumDiscussions int    `json:"numdiscussions"`
	CMID           int    `json:"cmid"`
}

type forumDisplay struct {
	ID             int    `json:"id"`
	CMID           int    `json:"cmid"`
	Name           string `json:"name"`
	Type           string `json:"type"`
	Intro          string `json:"intro,omitempty"`
	NumDiscussions int    `json:"num_discussions"`
}

func HandleListForums(ctx context.Context, client *api.Client, input ListForumsInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}
	if input.CourseID == 0 {
		return "", fmt.Errorf("course_id is required")
	}

	params := map[string]string{
		"courseids[0]": fmt.Sprintf("%d", input.CourseID),
	}
	data, err := client.Call(ctx, "mod_forum_get_forums_by_courses", params)
	if err != nil {
		return "", fmt.Errorf("calling moodle: %w", err)
	}

	var raw []rawForum
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", fmt.Errorf("parsing forums: %w", err)
	}

	display := make([]forumDisplay, 0, len(raw))
	for _, f := range raw {
		display = append(display, forumDisplay{
			ID:             f.ID,
			CMID:           f.CMID,
			Name:           f.Name,
			Type:           f.Type,
			Intro:          truncate(stripHTML(f.Intro), 300),
			NumDiscussions: f.NumDiscussions,
		})
	}

	out, _ := json.MarshalIndent(map[string]any{
		"course_id":    input.CourseID,
		"total_forums": len(display),
		"forums":       display,
	}, "", "  ")
	return string(out), nil
}

// --- List Forum Discussions Tool ---

type ListForumDiscussionsInput struct {
	ForumID int `json:"forum_id" jsonschema:"description=The Moodle forum ID (from list_forums)"`
}

type rawDiscussion struct {
	ID            int    `json:"id"`
	Discussion    int    `json:"discussion"`
	Name          string `json:"name"`
	Subject       string `json:"subject"`
	UserID        int    `json:"userid"`
	UserFullName  string `json:"userfullname"`
	Created       int64  `json:"created"`
	TimeModified  int64  `json:"timemodified"`
	NumReplies    int    `json:"numreplies"`
	NumUnread     int    `json:"numunread"`
	Message       string `json:"message"`
	Pinned        bool   `json:"pinned"`
	Locked        int    `json:"locked"`
}

type discussionsResponse struct {
	Discussions []rawDiscussion `json:"discussions"`
}

type discussionDisplay struct {
	ID            int    `json:"id"`
	DiscussionID  int    `json:"discussion_id"`
	Subject       string `json:"subject"`
	Name          string `json:"name,omitempty"`
	Author        string `json:"author"`
	Created       string `json:"created"`
	TimeModified  string `json:"time_modified"`
	NumReplies    int    `json:"num_replies"`
	NumUnread     int    `json:"num_unread,omitempty"`
	Pinned        bool   `json:"pinned,omitempty"`
	Locked        bool   `json:"locked,omitempty"`
	MessagePreview string `json:"message_preview,omitempty"`
}

func HandleListForumDiscussions(ctx context.Context, client *api.Client, input ListForumDiscussionsInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}
	if input.ForumID == 0 {
		return "", fmt.Errorf("forum_id is required")
	}

	params := map[string]string{
		"forumid":       fmt.Sprintf("%d", input.ForumID),
		"sortby":        "timemodified",
		"sortdirection": "DESC",
	}
	data, err := client.Call(ctx, "mod_forum_get_forum_discussions_paginated", params)
	if err != nil {
		return "", fmt.Errorf("calling moodle: %w", err)
	}

	var resp discussionsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("parsing discussions: %w", err)
	}

	display := make([]discussionDisplay, 0, len(resp.Discussions))
	for _, d := range resp.Discussions {
		display = append(display, discussionDisplay{
			ID:             d.ID,
			DiscussionID:   d.Discussion,
			Subject:        d.Subject,
			Name:           d.Name,
			Author:         d.UserFullName,
			Created:        time.Unix(d.Created, 0).Format("2006-01-02 15:04"),
			TimeModified:   time.Unix(d.TimeModified, 0).Format("2006-01-02 15:04"),
			NumReplies:     d.NumReplies,
			NumUnread:      d.NumUnread,
			Pinned:         d.Pinned,
			Locked:         d.Locked > 0,
			MessagePreview: truncate(stripHTML(d.Message), 200),
		})
	}

	out, _ := json.MarshalIndent(map[string]any{
		"forum_id":          input.ForumID,
		"total_discussions": len(display),
		"discussions":       display,
	}, "", "  ")
	return string(out), nil
}

// --- Get Forum Discussion Tool ---

type GetForumDiscussionInput struct {
	DiscussionID int `json:"discussion_id" jsonschema:"description=The Moodle forum discussion ID (from list_forum_discussions)"`
}

type rawForumPost struct {
	ID           int    `json:"id"`
	PostID       int    `json:"postid"`
	ParentID     int    `json:"parent"`
	ParentPostID int    `json:"parentid"`
	Subject      string `json:"subject"`
	Message      string `json:"message"`
	UserID       int    `json:"userid"`
	UserFullName string `json:"userfullname"`
	Author       struct {
		FullName string `json:"fullname"`
	} `json:"author"`
	Created      int64 `json:"created"`
	TimeCreated  int64 `json:"timecreated"`
	Modified     int64 `json:"modified"`
}

type forumPostDisplay struct {
	ID           int    `json:"id"`
	ParentID     int    `json:"parent_id"`
	Subject      string `json:"subject"`
	Author       string `json:"author"`
	Created      string `json:"created"`
	Message      string `json:"message"`
}

// pickPostFields tolerates two response shapes — older Moodle returns
// posts with id/parent/userfullname/created; newer Moodle wraps them
// inside {posts:[...]} with author.fullname / postid / parentid / timecreated.
func pickPostFields(p rawForumPost) forumPostDisplay {
	id := p.ID
	if id == 0 {
		id = p.PostID
	}
	parent := p.ParentID
	if parent == 0 {
		parent = p.ParentPostID
	}
	author := p.UserFullName
	if author == "" {
		author = p.Author.FullName
	}
	created := p.Created
	if created == 0 {
		created = p.TimeCreated
	}

	return forumPostDisplay{
		ID:       id,
		ParentID: parent,
		Subject:  p.Subject,
		Author:   author,
		Created:  time.Unix(created, 0).Format("2006-01-02 15:04"),
		Message:  p.Message,
	}
}

func HandleGetForumDiscussion(ctx context.Context, client *api.Client, input GetForumDiscussionInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}
	if input.DiscussionID == 0 {
		return "", fmt.Errorf("discussion_id is required")
	}

	params := map[string]string{
		"discussionid": fmt.Sprintf("%d", input.DiscussionID),
	}
	data, err := client.Call(ctx, "mod_forum_get_discussion_posts", params)
	if err != nil {
		return "", fmt.Errorf("calling moodle: %w", err)
	}

	// Two response shapes — newer Moodle wraps under "posts", older returns
	// the array (or wraps under "posts" too). Prefer "posts", fall back to
	// trying a top-level array.
	var wrapped struct {
		Posts []rawForumPost `json:"posts"`
	}
	var raw []rawForumPost
	if err := json.Unmarshal(data, &wrapped); err == nil && wrapped.Posts != nil {
		raw = wrapped.Posts
	} else {
		if err := json.Unmarshal(data, &raw); err != nil {
			return "", fmt.Errorf("parsing posts: %w", err)
		}
	}

	display := make([]forumPostDisplay, 0, len(raw))
	for _, p := range raw {
		display = append(display, pickPostFields(p))
	}

	out, _ := json.MarshalIndent(map[string]any{
		"discussion_id": input.DiscussionID,
		"total_posts":   len(display),
		"posts":         display,
	}, "", "  ")
	return string(out), nil
}

// --- Post Forum Reply Tool ---

type PostForumReplyInput struct {
	PostID  int    `json:"post_id" jsonschema:"description=The Moodle PARENT post ID to reply to (not the discussion ID — the post you are replying under)"`
	Subject string `json:"subject" jsonschema:"description=The subject line of the reply"`
	Message string `json:"message" jsonschema:"description=The message body (HTML supported)"`
}

func HandlePostForumReply(ctx context.Context, client *api.Client, input PostForumReplyInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}
	if input.PostID == 0 {
		return "", fmt.Errorf("post_id is required (parent post id, from get_forum_discussion)")
	}
	if input.Subject == "" {
		return "", fmt.Errorf("subject is required")
	}
	if input.Message == "" {
		return "", fmt.Errorf("message is required")
	}

	params := map[string]string{
		"postid":        fmt.Sprintf("%d", input.PostID),
		"subject":       input.Subject,
		"message":       input.Message,
		"messageformat": "1",
	}
	data, err := client.Call(ctx, "mod_forum_add_discussion_post", params)
	if err != nil {
		return "", fmt.Errorf("posting reply: %w", err)
	}

	var resp struct {
		PostID   int              `json:"postid"`
		Warnings []map[string]any `json:"warnings"`
	}
	_ = json.Unmarshal(data, &resp)

	out, _ := json.MarshalIndent(map[string]any{
		"success":         true,
		"new_post_id":     resp.PostID,
		"parent_post_id":  input.PostID,
		"subject":         input.Subject,
		"warnings":        resp.Warnings,
	}, "", "  ")
	return string(out), nil
}
