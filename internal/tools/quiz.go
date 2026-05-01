package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jawadh/moodle-mcp-server/internal/api"
)

// --- List Quizzes Tool ---

type ListQuizzesInput struct {
	CourseID int `json:"course_id" jsonschema:"description=The Moodle course ID to list quizzes for"`
}

type rawQuiz struct {
	ID         int     `json:"id"`
	Course     int     `json:"course"`
	CMID       int     `json:"coursemodule"`
	Name       string  `json:"name"`
	Intro      string  `json:"intro"`
	TimeOpen   int64   `json:"timeopen"`
	TimeClose  int64   `json:"timeclose"`
	TimeLimit  int64   `json:"timelimit"`
	Attempts   int     `json:"attempts"`
	SumGrades  float64 `json:"sumgrades"`
	Grade      float64 `json:"grade"`
	GradeMethod int    `json:"grademethod"`
}

type quizDisplay struct {
	ID             int     `json:"id"`
	CMID           int     `json:"cmid"`
	Course         int     `json:"course"`
	Name           string  `json:"name"`
	Intro          string  `json:"intro,omitempty"`
	TimeOpen       string  `json:"time_open,omitempty"`
	TimeClose      string  `json:"time_close,omitempty"`
	TimeLimitSecs  int64   `json:"time_limit_secs,omitempty"`
	AttemptsAllowed int    `json:"attempts_allowed"`
	SumGrades      float64 `json:"sum_grades"`
	MaxGrade       float64 `json:"max_grade"`
}

func HandleListQuizzes(ctx context.Context, client *api.Client, input ListQuizzesInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}
	if input.CourseID == 0 {
		return "", fmt.Errorf("course_id is required")
	}

	params := map[string]string{
		"courseids[0]": fmt.Sprintf("%d", input.CourseID),
	}
	data, err := client.Call(ctx, "mod_quiz_get_quizzes_by_courses", params)
	if err != nil {
		return "", fmt.Errorf("calling moodle: %w", err)
	}

	var resp struct {
		Quizzes []rawQuiz `json:"quizzes"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("parsing quizzes: %w", err)
	}

	display := make([]quizDisplay, 0, len(resp.Quizzes))
	for _, q := range resp.Quizzes {
		d := quizDisplay{
			ID:              q.ID,
			CMID:            q.CMID,
			Course:          q.Course,
			Name:            q.Name,
			Intro:           truncate(stripHTML(q.Intro), 300),
			TimeLimitSecs:   q.TimeLimit,
			AttemptsAllowed: q.Attempts,
			SumGrades:       q.SumGrades,
			MaxGrade:        q.Grade,
		}
		if q.TimeOpen > 0 {
			d.TimeOpen = time.Unix(q.TimeOpen, 0).Format("2006-01-02 15:04")
		}
		if q.TimeClose > 0 {
			d.TimeClose = time.Unix(q.TimeClose, 0).Format("2006-01-02 15:04")
		}
		display = append(display, d)
	}

	out, _ := json.MarshalIndent(map[string]any{
		"course_id":     input.CourseID,
		"total_quizzes": len(display),
		"quizzes":       display,
	}, "", "  ")
	return string(out), nil
}

// --- Get Quiz Attempts Tool ---

type GetQuizAttemptsInput struct {
	QuizID int `json:"quiz_id" jsonschema:"description=The Moodle quiz ID to get attempt history for"`
}

type rawQuizAttempt struct {
	ID         int     `json:"id"`
	Quiz       int     `json:"quiz"`
	UserID     int     `json:"userid"`
	Attempt    int     `json:"attempt"`
	State      string  `json:"state"`
	TimeStart  int64   `json:"timestart"`
	TimeFinish int64   `json:"timefinish"`
	TimeModified int64 `json:"timemodified"`
	SumGrades  float64 `json:"sumgrades"`
	Preview    int     `json:"preview"`
}

type quizAttemptDisplay struct {
	ID         int     `json:"id"`
	Attempt    int     `json:"attempt"`
	State      string  `json:"state"`
	TimeStart  string  `json:"time_start,omitempty"`
	TimeFinish string  `json:"time_finish,omitempty"`
	SumGrades  float64 `json:"sum_grades"`
}

func HandleGetQuizAttempts(ctx context.Context, client *api.Client, input GetQuizAttemptsInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}
	if input.QuizID == 0 {
		return "", fmt.Errorf("quiz_id is required")
	}

	userID := client.GetUserID()
	params := map[string]string{
		"quizid": fmt.Sprintf("%d", input.QuizID),
		"status": "all",
		"userid": fmt.Sprintf("%d", userID),
	}
	data, err := client.Call(ctx, "mod_quiz_get_user_attempts", params)
	if err != nil {
		return "", fmt.Errorf("calling moodle: %w", err)
	}

	var resp struct {
		Attempts []rawQuizAttempt `json:"attempts"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("parsing attempts: %w", err)
	}

	display := make([]quizAttemptDisplay, 0, len(resp.Attempts))
	for _, a := range resp.Attempts {
		d := quizAttemptDisplay{
			ID:        a.ID,
			Attempt:   a.Attempt,
			State:     a.State,
			SumGrades: a.SumGrades,
		}
		if a.TimeStart > 0 {
			d.TimeStart = time.Unix(a.TimeStart, 0).Format("2006-01-02 15:04")
		}
		if a.TimeFinish > 0 {
			d.TimeFinish = time.Unix(a.TimeFinish, 0).Format("2006-01-02 15:04")
		}
		display = append(display, d)
	}

	out, _ := json.MarshalIndent(map[string]any{
		"quiz_id":        input.QuizID,
		"total_attempts": len(display),
		"attempts":       display,
	}, "", "  ")
	return string(out), nil
}
