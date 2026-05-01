package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jawadh/moodle-mcp-server/internal/api"
)

// --- List Lessons Tool ---

type ListLessonsInput struct {
	CourseID int `json:"course_id" jsonschema:"description=The Moodle course ID to list lessons for"`
}

type rawLesson struct {
	ID        int    `json:"id"`
	Course    int    `json:"course"`
	CMID      int    `json:"coursemodule"`
	Name      string `json:"name"`
	Intro     string `json:"intro"`
	Available int64  `json:"available"`
	Deadline  int64  `json:"deadline"`
	TimeLimit int64  `json:"timelimit"`
}

type lessonDisplay struct {
	ID            int    `json:"id"`
	CMID          int    `json:"cmid"`
	Course        int    `json:"course"`
	Name          string `json:"name"`
	Intro         string `json:"intro,omitempty"`
	Available     string `json:"available,omitempty"`
	Deadline      string `json:"deadline,omitempty"`
	TimeLimitSecs int64  `json:"time_limit_secs,omitempty"`
}

func HandleListLessons(ctx context.Context, client *api.Client, input ListLessonsInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}
	if input.CourseID == 0 {
		return "", fmt.Errorf("course_id is required")
	}

	params := map[string]string{
		"courseids[0]": fmt.Sprintf("%d", input.CourseID),
	}
	data, err := client.Call(ctx, "mod_lesson_get_lessons_by_courses", params)
	if err != nil {
		return "", fmt.Errorf("calling moodle: %w", err)
	}

	var resp struct {
		Lessons []rawLesson `json:"lessons"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("parsing lessons: %w", err)
	}

	display := make([]lessonDisplay, 0, len(resp.Lessons))
	for _, l := range resp.Lessons {
		d := lessonDisplay{
			ID:            l.ID,
			CMID:          l.CMID,
			Course:        l.Course,
			Name:          l.Name,
			Intro:         truncate(stripHTML(l.Intro), 300),
			TimeLimitSecs: l.TimeLimit,
		}
		if l.Available > 0 {
			d.Available = time.Unix(l.Available, 0).Format("2006-01-02 15:04")
		}
		if l.Deadline > 0 {
			d.Deadline = time.Unix(l.Deadline, 0).Format("2006-01-02 15:04")
		}
		display = append(display, d)
	}

	out, _ := json.MarshalIndent(map[string]any{
		"course_id":     input.CourseID,
		"total_lessons": len(display),
		"lessons":       display,
	}, "", "  ")
	return string(out), nil
}

// --- Get Lesson Page Tool ---

type GetLessonPageInput struct {
	LessonID int `json:"lesson_id" jsonschema:"description=The Moodle lesson ID (from list_lessons)"`
	PageID   int `json:"page_id,omitempty" jsonschema:"description=The lesson page ID (omit or pass 0 to fetch the entry page)"`
}

type rawLessonPagesEntry struct {
	Page struct {
		ID         int    `json:"id"`
		LessonID   int    `json:"lessonid"`
		PrevPageID int    `json:"prevpageid"`
		NextPageID int    `json:"nextpageid"`
		Title      string `json:"title"`
		TypeString string `json:"typestring"`
	} `json:"page"`
}

type rawLessonPageData struct {
	Page struct {
		ID         int    `json:"id"`
		LessonID   int    `json:"lessonid"`
		PrevPageID int    `json:"prevpageid"`
		NextPageID int    `json:"nextpageid"`
		Title      string `json:"title"`
		TypeString string `json:"typestring"`
	} `json:"page"`
	PageContent string `json:"pagecontent"`
	Contents    []struct {
		Filename string `json:"filename"`
		Content  string `json:"content"`
	} `json:"contentfiles"`
}

type lessonPageDisplay struct {
	LessonID    int    `json:"lesson_id"`
	PageID      int    `json:"page_id"`
	Title       string `json:"title"`
	PageType    string `json:"page_type"`
	PrevPageID  int    `json:"prev_page_id"`
	NextPageID  int    `json:"next_page_id"`
	Contents    string `json:"contents"`
}

func HandleGetLessonPage(ctx context.Context, client *api.Client, input GetLessonPageInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}
	if input.LessonID == 0 {
		return "", fmt.Errorf("lesson_id is required")
	}

	pageID := input.PageID
	if pageID == 0 {
		// Find the entry page (prevpageid == 0).
		listParams := map[string]string{
			"lessonid": fmt.Sprintf("%d", input.LessonID),
		}
		listData, err := client.Call(ctx, "mod_lesson_get_pages", listParams)
		if err != nil {
			return "", fmt.Errorf("listing lesson pages: %w", err)
		}
		var listResp struct {
			Pages []rawLessonPagesEntry `json:"pages"`
		}
		if err := json.Unmarshal(listData, &listResp); err != nil {
			return "", fmt.Errorf("parsing lesson pages: %w", err)
		}
		for _, p := range listResp.Pages {
			if p.Page.PrevPageID == 0 {
				pageID = p.Page.ID
				break
			}
		}
		if pageID == 0 {
			return "", fmt.Errorf("no entry page found in lesson %d", input.LessonID)
		}
	}

	dataParams := map[string]string{
		"lessonid":       fmt.Sprintf("%d", input.LessonID),
		"pageid":         fmt.Sprintf("%d", pageID),
		"review":         "0",
		"returncontents": "1",
	}
	data, err := client.Call(ctx, "mod_lesson_get_page_data", dataParams)
	if err != nil {
		return "", fmt.Errorf("getting lesson page data: %w", err)
	}

	var pageData rawLessonPageData
	if err := json.Unmarshal(data, &pageData); err != nil {
		return "", fmt.Errorf("parsing lesson page: %w", err)
	}

	out, _ := json.MarshalIndent(lessonPageDisplay{
		LessonID:   input.LessonID,
		PageID:     pageData.Page.ID,
		Title:      pageData.Page.Title,
		PageType:   pageData.Page.TypeString,
		PrevPageID: pageData.Page.PrevPageID,
		NextPageID: pageData.Page.NextPageID,
		Contents:   pageData.PageContent,
	}, "", "  ")

	return string(out), nil
}
