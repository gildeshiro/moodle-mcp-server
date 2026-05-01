package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/jawadh/moodle-mcp-server/internal/api"
)

// Active-quiz-taking tools — start an attempt, read questions, save answers,
// submit. Together these cover the dependency chain:
//
//	start_quiz_attempt → get_quiz_question → save_quiz_answers → submit_quiz_attempt
//
// Scope: the COMMON question types — multichoice (single & multi), truefalse,
// shortanswer, numerical, and essay (text only). Drag-and-drop, gap-select,
// hot-spot, and matching are NOT supported because their answer payloads use
// heterogeneous DOM-style fields (e.g. choice grid co-ordinates, drop zones)
// that this generic regex-based field-name extractor will not produce. Calls
// against those question types may still return parseable data but submitted
// answers will most likely be discarded by the Moodle quiz engine.

// fieldNameRE finds Moodle quiz answer-input names inside the questionhtml
// blob. Moodle's quiz API requires that answer payloads echo these names
// verbatim: e.g. "q12345:1_answer" for multichoice, "q12345:1_answer" plus
// "q12345:1_answerformat" for essays, "q12345:1_:flagged" for the flag bit.
// We capture every name ending in "_answer" with optional suffixes — this is
// permissive on purpose so multichoice (single value), multichoice-multi
// (q…_choice0, q…_choice1, …) and shortanswer/numerical (single value) are
// all surfaced. We do NOT do full HTML parsing — the quiz HTML is server-
// generated and stable enough that a focused regex is sufficient.
var fieldNameRE = regexp.MustCompile(`name=["']([^"']+_answer[^"']*)["']`)

// extractAnswerFieldNames returns the deduplicated, source-order list of
// answer-input field names found in a single question's HTML.
func extractAnswerFieldNames(questionHTML string) []string {
	matches := fieldNameRE.FindAllStringSubmatch(questionHTML, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		name := m[1]
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// --- Start Quiz Attempt Tool ---

type StartQuizAttemptInput struct {
	QuizID int `json:"quiz_id" jsonschema:"description=The Moodle quiz ID (from list_quizzes) to begin a fresh attempt against"`
}

// rawStartAttemptResponse mirrors the Moodle mod_quiz_start_attempt response.
// Moodle returns {attempt: {...}, warnings: [...]}. Layout is a comma-
// separated string of slot numbers with 0 markers indicating page breaks.
type rawStartAttemptResponse struct {
	Attempt struct {
		ID      int    `json:"id"`
		Quiz    int    `json:"quiz"`
		Attempt int    `json:"attempt"`
		Layout  string `json:"layout"`
		State   string `json:"state"`
	} `json:"attempt"`
	Warnings []map[string]any `json:"warnings"`
}

// HandleStartQuizAttempt initiates a new quiz attempt. The returned attempt_id
// must be passed to every subsequent quiz tool call. layout encodes the slot
// order with `0` markers between pages — total_questions is the count of
// non-zero slot tokens.
func HandleStartQuizAttempt(ctx context.Context, client *api.Client, input StartQuizAttemptInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}
	if input.QuizID == 0 {
		return "", fmt.Errorf("quiz_id is required")
	}

	params := map[string]string{
		"quizid": fmt.Sprintf("%d", input.QuizID),
	}
	data, err := client.Call(ctx, "mod_quiz_start_attempt", params)
	if err != nil {
		return "", fmt.Errorf("starting quiz attempt: %w", err)
	}

	var resp rawStartAttemptResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("parsing start_attempt response: %w", err)
	}

	totalQuestions := 0
	for _, slot := range strings.Split(resp.Attempt.Layout, ",") {
		if slot != "" && slot != "0" {
			totalQuestions++
		}
	}

	out, _ := json.MarshalIndent(map[string]any{
		"attempt_id":      resp.Attempt.ID,
		"attempt_number":  resp.Attempt.Attempt,
		"quiz_id":         resp.Attempt.Quiz,
		"state":           resp.Attempt.State,
		"layout":          resp.Attempt.Layout,
		"total_questions": totalQuestions,
		"warnings":        resp.Warnings,
	}, "", "  ")
	return string(out), nil
}

// --- Get Quiz Question Tool ---

type GetQuizQuestionInput struct {
	AttemptID int `json:"attempt_id" jsonschema:"description=The attempt_id returned by start_quiz_attempt"`
	Page      int `json:"page" jsonschema:"description=The 0-based page number to fetch (Moodle quizzes can split questions across multiple pages — first page is 0)"`
}

// rawAttemptDataQuestion is the Moodle question record inside
// mod_quiz_get_attempt_data. We only consume a subset of fields here. Note
// that Moodle's response shape varies across versions: blockedbyprevious is
// historically a bool but some forks return an int; we accept either via
// json.RawMessage and ignore the value (we don't surface it).
type rawAttemptDataQuestion struct {
	Slot          int             `json:"slot"`
	Type          string          `json:"type"`
	Page          int             `json:"page"`
	HTML          string          `json:"html"`
	State         string          `json:"state"`
	Status        string          `json:"status"`
	HasAutoSave   bool            `json:"hasautosavedstep"`
	Number        int             `json:"number"`
	BlockedBy     json.RawMessage `json:"blockedbyprevious"`
	Sequencecheck int             `json:"sequencecheck"`
}

type quizQuestionDisplay struct {
	Slot             int      `json:"slot"`
	Number           int      `json:"number,omitempty"`
	Type             string   `json:"type"`
	Page             int      `json:"page"`
	HTMLQuestionText string   `json:"html_question_text"`
	AnswerFieldNames []string `json:"answer_field_names"`
	State            string   `json:"state,omitempty"`
	Status           string   `json:"status,omitempty"`
	IsCompleted      bool     `json:"is_completed"`
}

// HandleGetQuizQuestion fetches a single page of an in-progress attempt and
// returns each question with the field names the model needs to echo back
// in save_quiz_answers. Heterogeneous question types whose answer fields are
// not named "*_answer*" will yield an empty answer_field_names — caller can
// inspect html_question_text directly in that case.
func HandleGetQuizQuestion(ctx context.Context, client *api.Client, input GetQuizQuestionInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}
	if input.AttemptID == 0 {
		return "", fmt.Errorf("attempt_id is required")
	}

	params := map[string]string{
		"attemptid": fmt.Sprintf("%d", input.AttemptID),
		"page":      fmt.Sprintf("%d", input.Page),
	}
	data, err := client.Call(ctx, "mod_quiz_get_attempt_data", params)
	if err != nil {
		return "", fmt.Errorf("fetching attempt data: %w", err)
	}

	var resp struct {
		Questions []rawAttemptDataQuestion `json:"questions"`
		Attempt   struct {
			ID    int    `json:"id"`
			State string `json:"state"`
		} `json:"attempt"`
		Warnings []map[string]any `json:"warnings"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("parsing attempt data: %w", err)
	}

	display := make([]quizQuestionDisplay, 0, len(resp.Questions))
	for _, q := range resp.Questions {
		// "complete" / "gradedright" / "gradedwrong" all imply the question
		// has been filled in; "todo" / "invalid" mean the user has not yet
		// committed an answer.
		isComplete := q.State == "complete" || strings.HasPrefix(q.State, "graded")
		display = append(display, quizQuestionDisplay{
			Slot:             q.Slot,
			Number:           q.Number,
			Type:             q.Type,
			Page:             q.Page,
			HTMLQuestionText: q.HTML,
			AnswerFieldNames: extractAnswerFieldNames(q.HTML),
			State:            q.State,
			Status:           q.Status,
			IsCompleted:      isComplete,
		})
	}

	out, _ := json.MarshalIndent(map[string]any{
		"attempt_id":     resp.Attempt.ID,
		"attempt_state":  resp.Attempt.State,
		"page":           input.Page,
		"total_returned": len(display),
		"questions":      display,
		"warnings":       resp.Warnings,
	}, "", "  ")
	return string(out), nil
}

// --- Save Quiz Answers Tool ---

type SaveQuizAnswersInput struct {
	AttemptID int               `json:"attempt_id" jsonschema:"description=The attempt_id returned by start_quiz_attempt"`
	Page      int               `json:"page" jsonschema:"description=The 0-based page number whose answers are being saved (must match the page returned by get_quiz_question)"`
	Answers   map[string]string `json:"answers" jsonschema:"description=Map of answer_field_name → value. Keys are the opaque tokens returned by get_quiz_question (e.g. q1234:1_answer); values are the answer payloads (multichoice → choice index as string; truefalse → '1' or '0'; shortanswer/numerical/essay → user text)"`
}

// HandleSaveQuizAnswers commits answers for the given page WITHOUT submitting
// the whole attempt. Moodle's `data` parameter is an array of {name, value}
// pairs — we translate the answers map into that shape.
func HandleSaveQuizAnswers(ctx context.Context, client *api.Client, input SaveQuizAnswersInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}
	if input.AttemptID == 0 {
		return "", fmt.Errorf("attempt_id is required")
	}
	if len(input.Answers) == 0 {
		return "", fmt.Errorf("answers must contain at least one entry")
	}

	params := map[string]string{
		"attemptid": fmt.Sprintf("%d", input.AttemptID),
	}
	i := 0
	for name, value := range input.Answers {
		params[fmt.Sprintf("data[%d][name]", i)] = name
		params[fmt.Sprintf("data[%d][value]", i)] = value
		i++
	}

	data, err := client.Call(ctx, "mod_quiz_save_attempt", params)
	if err != nil {
		return "", fmt.Errorf("saving attempt answers: %w", err)
	}

	var resp struct {
		Status   bool             `json:"status"`
		Warnings []map[string]any `json:"warnings"`
	}
	_ = json.Unmarshal(data, &resp)

	out, _ := json.MarshalIndent(map[string]any{
		"success":         resp.Status,
		"attempt_id":      input.AttemptID,
		"page":            input.Page,
		"answers_saved":   len(input.Answers),
		"warnings":        resp.Warnings,
	}, "", "  ")
	return string(out), nil
}

// --- Submit Quiz Attempt Tool ---

type SubmitQuizAttemptInput struct {
	AttemptID int  `json:"attempt_id" jsonschema:"description=The attempt_id returned by start_quiz_attempt"`
	Finalize  bool `json:"finalize" jsonschema:"description=When true, finishes the attempt (counts toward grade); when false, just commits the current state — useful for moving between pages without submitting"`
}

// HandleSubmitQuizAttempt routes to mod_quiz_process_attempt with
// finishattempt set per the Finalize flag. When Finalize=true, the attempt
// becomes "finished" and is graded; when Finalize=false, the call merely
// processes whatever has been saved so far without ending the attempt.
func HandleSubmitQuizAttempt(ctx context.Context, client *api.Client, input SubmitQuizAttemptInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}
	if input.AttemptID == 0 {
		return "", fmt.Errorf("attempt_id is required")
	}

	finishFlag := "0"
	if input.Finalize {
		finishFlag = "1"
	}
	params := map[string]string{
		"attemptid":     fmt.Sprintf("%d", input.AttemptID),
		"finishattempt": finishFlag,
		"timeup":        "0",
	}
	data, err := client.Call(ctx, "mod_quiz_process_attempt", params)
	if err != nil {
		return "", fmt.Errorf("processing attempt: %w", err)
	}

	// Moodle returns {state: "finished"|"inprogress", warnings: [...]}.
	// SumGrades is exposed on a follow-up review call; not all servers
	// return it directly here, so we surface what we receive.
	var resp struct {
		State     string           `json:"state"`
		SumGrades float64          `json:"sumgrades"`
		Warnings  []map[string]any `json:"warnings"`
	}
	_ = json.Unmarshal(data, &resp)

	out, _ := json.MarshalIndent(map[string]any{
		"success":    true,
		"attempt_id": input.AttemptID,
		"finalize":   input.Finalize,
		"state":      resp.State,
		"sumgrades":  resp.SumGrades,
		"warnings":   resp.Warnings,
	}, "", "  ")
	return string(out), nil
}
