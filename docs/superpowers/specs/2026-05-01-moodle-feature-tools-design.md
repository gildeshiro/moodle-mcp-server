# Moodle Feature Tools — File Submission, Forum, Messaging, Quiz/Lesson Reads

**Date:** 2026-05-01
**Status:** Approved (user pre-authorized autonomous execution)
**Working fork:** `gildeshiro/moodle-mcp-server`
**Branch:** `feat/streamable-http-transport`

## Goal

Cover three more cloud-only autonomy gaps in one round so the model in claude.ai web can do everything a student does day-to-day in their LMS:

1. **File submission for assignments** (#4): submit a PDF/doc essay or homework answer when the assignment requires file upload, not text submission.
2. **Forum + messaging** (#5): read forum announcements from professors, post replies in discussions, read direct messages.
3. **Quiz + lesson reads** (#6, reduced): list quizzes in a course, see attempt history and grades, list lessons and read individual lesson pages. Active quiz-taking (answering and submitting questions) is deferred — too verbose to fit in this round.

## Non-goals

- Active quiz answering / submission (will be its own round; the Moodle quiz API is heterogeneous across question types)
- Real-time message subscription (single-shot reads only)
- Forum attachments upload (text replies only — file replies are similar to #4 but in a different API path)
- Lesson "navigation" — picking next page based on student answers; we surface page content only

## Architecture

### File layout

```
internal/tools/
├── ... (existing)
├── assignment_file.go    NEW: HandleSubmitAssignmentFile
├── forum.go              NEW: HandleListForums, HandleListForumDiscussions,
│                              HandleGetForumDiscussion, HandlePostForumReply
├── messaging.go          NEW: HandleListMessages, HandleSendMessage
├── quiz.go               NEW: HandleListQuizzes, HandleGetQuizAttempts
└── lesson.go             NEW: HandleListLessons, HandleGetLessonPage
```

`cmd/moodle-mcp/main.go` adds 11 new `s.AddTool(...)` registrations grouped by feature, following the existing pattern.

### Tool signatures

| Tool | Inputs | Returns |
|---|---|---|
| `submit_assignment_file` | `assignment_id` (int), `filename` (string), `content_base64` (string) | submission status JSON |
| `list_forums` | `course_id` (int) | array of forums (id, name, intro, type) |
| `list_forum_discussions` | `forum_id` (int) | array of discussions (id, subject, author, lastpost) |
| `get_forum_discussion` | `discussion_id` (int) | array of posts (author, subject, message, created) |
| `post_forum_reply` | `discussion_id` (int), `post_id` (int — parent), `subject` (string), `message` (string, HTML supported) | new post id |
| `list_messages` | `unread_only` (bool, default true), `limit` (int, default 20) | array of messages (id, from, subject, time, text) |
| `send_message` | `to_user_id` (int), `message` (string) | message id |
| `list_quizzes` | `course_id` (int) | array of quizzes (id, name, timeopen, timeclose, attempts allowed) |
| `get_quiz_attempts` | `quiz_id` (int) | array of attempts (id, state, timestart, timefinish, sumgrades) |
| `list_lessons` | `course_id` (int) | array of lessons (id, name, intro, available, deadline) |
| `get_lesson_page` | `lesson_id` (int), `page_id` (int, optional — defaults to first) | page content (title, contents HTML, type, navigable next/prev) |

### Moodle API calls

| Tool | Web service function |
|---|---|
| submit_assignment_file | `core_files_upload` (to draft area) → `mod_assign_save_submission` |
| list_forums | `mod_forum_get_forums_by_courses` |
| list_forum_discussions | `mod_forum_get_forum_discussions_paginated` |
| get_forum_discussion | `mod_forum_get_discussion_posts` |
| post_forum_reply | `mod_forum_add_discussion_post` |
| list_messages | `core_message_get_messages` |
| send_message | `core_message_send_instant_messages` |
| list_quizzes | `mod_quiz_get_quizzes_by_courses` |
| get_quiz_attempts | `mod_quiz_get_user_attempts` |
| list_lessons | `mod_lesson_get_lessons_by_courses` |
| get_lesson_page | `mod_lesson_get_pages` (list) + `mod_lesson_get_page_data` (content) |

### Pattern

Each handler follows the existing pattern (compare with `internal/tools/messages.go` `HandleGetNotifications` or `internal/tools/grades.go` `HandleGetGradesOverview`):

```go
type FooInput struct { ... }

func HandleFoo(ctx context.Context, client *api.Client, input FooInput) (string, error) {
    if !client.IsAuthenticated() {
        return "", api.ErrNotAuthenticated
    }
    if input.Required == 0 {
        return "", fmt.Errorf("required is required")
    }
    params := map[string]string{ ... }
    data, err := client.Call(ctx, "moodle_function_name", params)
    if err != nil { return "", err }
    var raw struct { ... }
    if err := json.Unmarshal(data, &raw); err != nil { return "", err }
    // transform raw → display struct
    out, _ := json.MarshalIndent(display, "", "  ")
    return string(out), nil
}
```

### File submission specifics

The Moodle file-upload + assignment-save flow is two-step:

1. POST the file to `<moodle>/webservice/upload.php?token=...&filearea=draft&itemid=0` as multipart/form-data. Response is JSON with `[{itemid, filename, ...}]`. The `itemid` is what makes the file accessible to subsequent webservice calls.
2. Call `mod_assign_save_submission` with `assignmentid`, `plugindata[files_filemanager]=<draft itemid>`, `plugindata[onlinetext_editor][text]=""`, `plugindata[onlinetext_editor][format]=1`.

Step 1 is HTTP-form upload, NOT a JSON-RPC call — the existing `client.Call()` helper doesn't cover it. We add a new `client.UploadFile(ctx, content, filename)` helper in `internal/api/client.go` that handles the multipart form.

### Tool descriptions

Descriptions follow the existing tone — terse, function-oriented, mention the source (Moodle) explicitly so the model knows when to pick the tool.

## Error handling

- Standard handler pattern (auth check → param check → API call → unmarshal → format).
- For `submit_assignment_file`, validate base64 decoding before attempting upload; surface decode errors with a clear message.
- All HTML content (forum posts, lesson pages) is returned AS-IS (HTML strings). The model can render or strip as needed.

## Testing

Per the project's existing convention (see `internal/tools/messages.go`, etc., which have no unit tests), we don't add unit tests for the new handlers — they're thin wrappers over the Moodle JSON-RPC client. The reliance is on:

1. Build/vet pass in CI.
2. Manual end-to-end smoke against `pvs.cecierj.edu.br/ava` for at least one tool per feature category.

For `submit_assignment_file` we DO NOT execute a real submission against the user's account during smoke (would create real submissions in their CECIERJ record). We verify the upload endpoint accepts the request with a 200 response, then immediately fail the save (e.g., by passing a non-existent assignment ID).

## Deployment

Same as previous rounds:

1. Commit on `feat/streamable-http-transport`
2. Push to `origin feat/streamable-http-transport`
3. Force-push to `origin main` (Railway redeploy)
4. Production smoke: list_forums + list_messages + list_quizzes against course 35 / course 40.
