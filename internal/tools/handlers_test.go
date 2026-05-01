package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jawadh/moodle-mcp-server/internal/api"
)

// newTestClient creates an API client pointed at a mock HTTP server.
func newTestClient(t *testing.T, handler http.Handler) (*api.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c := api.NewClient()
	c.SetSession(srv.URL, "testtoken")
	c.SetUserID(1)
	return c, srv
}

func TestHandleListCourses_UnauthenticatedError(t *testing.T) {
	c := api.NewClient()
	_, err := HandleListCourses(context.Background(), c, ListCoursesInput{})
	if err == nil {
		t.Fatal("expected error when not authenticated")
	}
}

func TestHandleListCourses_Success(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "shortname": "CS101", "fullname": "Intro to CS", "startdate": 1700000000, "enddate": 0},
		})
	})
	c, srv := newTestClient(t, handler)
	defer srv.Close()

	result, err := HandleListCourses(context.Background(), c, ListCoursesInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if out["total_courses"].(float64) != 1 {
		t.Errorf("total_courses = %v, want 1", out["total_courses"])
	}
}

func TestHandleGetAssignments_MissingCourseID(t *testing.T) {
	c, srv := newTestClient(t, http.NotFoundHandler())
	defer srv.Close()

	_, err := HandleGetAssignments(context.Background(), c, GetAssignmentsInput{CourseID: 0})
	if err == nil {
		t.Fatal("expected error for missing course_id")
	}
}

func TestHandleGetGrades_MissingCourseID(t *testing.T) {
	c, srv := newTestClient(t, http.NotFoundHandler())
	defer srv.Close()

	_, err := HandleGetGrades(context.Background(), c, GetGradesInput{CourseID: 0})
	if err == nil {
		t.Fatal("expected error for missing course_id")
	}
}

func TestHandleGetCourseContents_MissingCourseID(t *testing.T) {
	c, srv := newTestClient(t, http.NotFoundHandler())
	defer srv.Close()

	_, err := HandleGetCourseContents(context.Background(), c, GetCourseContentsInput{CourseID: 0})
	if err == nil {
		t.Fatal("expected error for missing course_id")
	}
}

func TestHandleGetCourseDetails_MissingCourseID(t *testing.T) {
	c, srv := newTestClient(t, http.NotFoundHandler())
	defer srv.Close()

	_, err := HandleGetCourseDetails(context.Background(), c, GetCourseDetailsInput{CourseID: 0})
	if err == nil {
		t.Fatal("expected error for missing course_id")
	}
}

func TestHandleSubmitAssignment_MissingFields(t *testing.T) {
	c, srv := newTestClient(t, http.NotFoundHandler())
	defer srv.Close()

	tests := []struct {
		name  string
		input SubmitAssignmentInput
	}{
		{"missing assignment_id", SubmitAssignmentInput{Text: "hello"}},
		{"missing text", SubmitAssignmentInput{AssignmentID: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := HandleSubmitAssignment(context.Background(), c, tt.input)
			if err == nil {
				t.Fatal("expected error for missing required field")
			}
		})
	}
}

func TestHandleLogin_MissingFields(t *testing.T) {
	c := api.NewClient()
	tests := []struct {
		name  string
		input LoginInput
	}{
		{"missing all", LoginInput{}},
		{"missing username", LoginInput{MoodleURL: "https://m.example.com", Password: "p"}},
		{"missing password", LoginInput{MoodleURL: "https://m.example.com", Username: "u"}},
		{"missing url", LoginInput{Username: "u", Password: "p"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := HandleLogin(context.Background(), c, tt.input)
			if err == nil {
				t.Fatal("expected error for missing required field")
			}
		})
	}
}

func TestHandleGetNotifications_DefaultsApplied(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify default limit and read type are sent
		q := r.URL.Query()
		if q.Get("limitnum") == "0" {
			http.Error(w, "bad limit", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"messages": []any{}})
	})
	c, srv := newTestClient(t, handler)
	defer srv.Close()

	// Zero limit should default to 20
	result, err := HandleGetNotifications(context.Background(), c, GetNotificationsInput{Limit: 0, UnreadOnly: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
}

// BenchmarkHandleListCourses measures end-to-end handler performance with a mock server.
func BenchmarkHandleListCourses(b *testing.B) {
	courses := make([]map[string]any, 10)
	for i := range courses {
		courses[i] = map[string]any{
			"id": i + 1, "shortname": "CS", "fullname": "Course", "startdate": 1700000000, "enddate": 0,
		}
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(courses)
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	c := api.NewClient()
	c.SetSession(srv.URL, "tok")
	c.SetUserID(1)
	ctx := context.Background()

	b.ResetTimer()
	for b.Loop() {
		HandleListCourses(ctx, c, ListCoursesInput{})
	}
}
