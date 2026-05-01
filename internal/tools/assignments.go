package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jawadh/moodle-mcp-server/internal/api"
)

// --- Get Assignments Tool ---

type Assignment struct {
	ID          int    `json:"id"`
	CMID        int    `json:"cmid"`
	Name        string `json:"name"`
	DueDate     int64  `json:"duedate"`
	CutoffDate  int64  `json:"cutoffdate"`
	AllowFrom   int64  `json:"allowsubmissionsfromdate"`
	Grade       int    `json:"grade"`
	Intro       string `json:"intro"`
	NoSubmit    int    `json:"nosubmissions"`
}

type AssignmentDisplay struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	DueDate    string `json:"due_date"`
	CutoffDate string `json:"cutoff_date,omitempty"`
	MaxGrade   int    `json:"max_grade"`
	Overdue    bool   `json:"overdue"`
	DaysLeft   int    `json:"days_left,omitempty"`
	Intro      string `json:"description,omitempty"`
}

type AssignmentCourse struct {
	ID          int          `json:"id"`
	FullName    string       `json:"fullname"`
	Assignments []Assignment `json:"assignments"`
}

type AssignmentResponse struct {
	Courses []AssignmentCourse `json:"courses"`
}

type GetAssignmentsInput struct {
	CourseID int `json:"course_id" jsonschema:"description=The Moodle course ID to get assignments for"`
}

func HandleGetAssignments(ctx context.Context, client *api.Client, input GetAssignmentsInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}

	if input.CourseID == 0 {
		return "", fmt.Errorf("course_id is required")
	}

	params := map[string]string{
		"courseids[0]": fmt.Sprintf("%d", input.CourseID),
	}

	data, err := client.Call(ctx, "mod_assign_get_assignments", params)
	if err != nil {
		return "", err
	}

	var resp AssignmentResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("parsing assignments: %w", err)
	}

	now := time.Now()
	var display []AssignmentDisplay
	for _, course := range resp.Courses {
		for _, a := range course.Assignments {
			d := AssignmentDisplay{
				ID:       a.ID,
				Name:     a.Name,
				MaxGrade: a.Grade,
				Intro:    truncate(a.Intro, 200),
			}
			if a.DueDate > 0 {
				due := time.Unix(a.DueDate, 0)
				d.DueDate = due.Format("2006-01-02 15:04")
				d.Overdue = now.After(due)
				if !d.Overdue {
					d.DaysLeft = int(time.Until(due).Hours() / 24)
				}
			}
			if a.CutoffDate > 0 {
				d.CutoffDate = time.Unix(a.CutoffDate, 0).Format("2006-01-02 15:04")
			}
			display = append(display, d)
		}
	}

	result, _ := json.MarshalIndent(map[string]interface{}{
		"course_id":         input.CourseID,
		"total_assignments": len(display),
		"assignments":       display,
	}, "", "  ")
	return string(result), nil
}

// --- Get Upcoming Assignments Tool ---

type UpcomingAssignment struct {
	CourseID   int    `json:"course_id"`
	CourseName string `json:"course_name"`
	AssignmentDisplay
}

type GetUpcomingAssignmentsInput struct {
	DaysAhead int `json:"days_ahead,omitempty" jsonschema:"description=Number of days to look ahead (default: 30)"`
}

func HandleGetUpcomingAssignments(ctx context.Context, client *api.Client, input GetUpcomingAssignmentsInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}

	daysAhead := input.DaysAhead
	if daysAhead <= 0 {
		daysAhead = 30
	}

	courseIDs, err := getEnrolledCourseIDs(ctx, client)
	if err != nil {
		return "", err
	}

	now := time.Now()
	cutoff := now.Add(time.Duration(daysAhead) * 24 * time.Hour)
	var upcoming []UpcomingAssignment

	for _, cid := range courseIDs {
		params := map[string]string{
			"courseids[0]": fmt.Sprintf("%d", cid),
		}

		data, err := client.Call(ctx, "mod_assign_get_assignments", params)
		if err != nil {
			continue
		}

		var resp AssignmentResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			continue
		}

		for _, course := range resp.Courses {
			for _, a := range course.Assignments {
				if a.DueDate == 0 {
					continue
				}
				due := time.Unix(a.DueDate, 0)
				if due.After(now) && due.Before(cutoff) {
					upcoming = append(upcoming, UpcomingAssignment{
						CourseID:   cid,
						CourseName: course.FullName,
						AssignmentDisplay: AssignmentDisplay{
							ID:       a.ID,
							Name:     a.Name,
							DueDate:  due.Format("2006-01-02 15:04"),
							MaxGrade: a.Grade,
							DaysLeft: int(time.Until(due).Hours() / 24),
						},
					})
				}
			}
		}
	}

	result, _ := json.MarshalIndent(map[string]interface{}{
		"days_ahead":          daysAhead,
		"upcoming_count":      len(upcoming),
		"upcoming_assignments": upcoming,
	}, "", "  ")
	return string(result), nil
}

// --- Submit Assignment Tool ---

type SubmitAssignmentInput struct {
	AssignmentID int    `json:"assignment_id" jsonschema:"description=The assignment ID to submit to"`
	Text         string `json:"text" jsonschema:"description=The text content to submit (for online text submissions)"`
}

func HandleSubmitAssignment(ctx context.Context, client *api.Client, input SubmitAssignmentInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}

	if input.AssignmentID == 0 {
		return "", fmt.Errorf("assignment_id is required")
	}
	if input.Text == "" {
		return "", fmt.Errorf("text content is required for submission")
	}

	params := map[string]string{
		"assignmentid":                           fmt.Sprintf("%d", input.AssignmentID),
		"plugindata[onlinetext_editor][text]":     input.Text,
		"plugindata[onlinetext_editor][format]":   "1", // HTML format
		"plugindata[onlinetext_editor][itemid]":   "0",
	}

	_, err := client.Call(ctx, "mod_assign_save_submission", params)
	if err != nil {
		return "", fmt.Errorf("submitting assignment: %w", err)
	}

	return fmt.Sprintf("Assignment %d submitted successfully at %s",
		input.AssignmentID, time.Now().Format("2006-01-02 15:04:05")), nil
}

// --- Update Assignment Tool ---

type UpdateAssignmentInput struct {
	AssignmentID int    `json:"assignment_id" jsonschema:"description=The assignment ID to update"`
	Text         string `json:"text" jsonschema:"description=The new text content to replace the existing submission"`
}

func HandleUpdateAssignment(ctx context.Context, client *api.Client, input UpdateAssignmentInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}

	if input.AssignmentID == 0 {
		return "", fmt.Errorf("assignment_id is required")
	}
	if input.Text == "" {
		return "", fmt.Errorf("text content is required")
	}

	params := map[string]string{
		"assignmentid":                         fmt.Sprintf("%d", input.AssignmentID),
		"plugindata[onlinetext_editor][text]":   input.Text,
		"plugindata[onlinetext_editor][format]": "1",
		"plugindata[onlinetext_editor][itemid]": "0",
	}

	_, err := client.Call(ctx, "mod_assign_save_submission", params)
	if err != nil {
		return "", fmt.Errorf("updating assignment: %w", err)
	}

	return fmt.Sprintf("Assignment %d updated successfully at %s",
		input.AssignmentID, time.Now().Format("2006-01-02 15:04:05")), nil
}

// --- Get Journal Entry Tool ---

type GetJournalEntryInput struct {
	JournalID int `json:"journal_id" jsonschema:"description=The Moodle journal module ID (get from course contents, modname=journal)"`
}

func HandleGetJournalEntry(ctx context.Context, client *api.Client, input GetJournalEntryInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}
	if input.JournalID == 0 {
		return "", fmt.Errorf("journal_id is required")
	}

	params := map[string]string{
		"journalid": fmt.Sprintf("%d", input.JournalID),
	}

	data, err := client.Call(ctx, "mod_journal_get_entry", params)
	if err != nil {
		return "", fmt.Errorf("getting journal entry: %w", err)
	}

	return string(data), nil
}

// --- Submit Journal Entry Tool ---

type SubmitJournalInput struct {
	JournalID int    `json:"journal_id" jsonschema:"description=The Moodle journal module ID (get from course contents, modname=journal)"`
	Text      string `json:"text" jsonschema:"description=The text content to submit or update in the journal"`
}

func HandleSubmitJournal(ctx context.Context, client *api.Client, input SubmitJournalInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}
	if input.JournalID == 0 {
		return "", fmt.Errorf("journal_id is required")
	}
	if input.Text == "" {
		return "", fmt.Errorf("text is required")
	}

	params := map[string]string{
		"journalid": fmt.Sprintf("%d", input.JournalID),
		"text":      input.Text,
		"format":    "1", // HTML format
	}

	_, err := client.Call(ctx, "mod_journal_set_text", params)
	if err != nil {
		return "", fmt.Errorf("submitting journal entry: %w", err)
	}

	return fmt.Sprintf("Journal %d entry saved successfully at %s",
		input.JournalID, time.Now().Format("2006-01-02 15:04:05")), nil
}
