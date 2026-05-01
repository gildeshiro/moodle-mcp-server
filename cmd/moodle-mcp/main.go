package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/jawadh/moodle-mcp-server/internal/api"
	"github.com/jawadh/moodle-mcp-server/internal/config"
	"github.com/jawadh/moodle-mcp-server/internal/server"
	"github.com/jawadh/moodle-mcp-server/internal/tools"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func main() {
	// Parse command line flags
	mode := flag.String("mode", "mcp", "Server mode: 'mcp' for Claude, 'rest' for HTTP API, 'both' for both")
	restPort := flag.Int("port", 8080, "REST API port (default: 8080)")
	flag.Parse()

	// Load config from environment
	cfg := config.LoadFromEnv()
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	// Allow port override via env var
	if envPort := os.Getenv("REST_API_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil {
			*restPort = p
		}
	}

	// Create the Moodle API client
	client := api.NewClient()

	// If credentials are provided via env vars, authenticate now
	ctx := context.Background()
	if cfg.HasAuth() {
		token := cfg.Token
		if token == "" {
			var err error
			token, err = api.GetTokenFromCredentials(ctx, cfg.MoodleURL, cfg.Username, cfg.Password)
			if err != nil {
				fmt.Fprintf(os.Stderr, "authentication failed: %v\n", err)
				os.Exit(1)
			}
		}
		client.SetSession(cfg.MoodleURL, token)

		// Get user ID from site info
		if data, err := client.Call(ctx, "core_webservice_get_site_info", nil); err == nil {
			var info struct {
				UserID int `json:"userid"`
			}
			if json.Unmarshal(data, &info) == nil {
				client.SetUserID(info.UserID)
			}
		}
	}

	// Run the appropriate server(s)
	switch *mode {
	case "mcp":
		runMCPServer(client)
	case "rest":
		runRESTServer(client, *restPort)
	case "both":
		log.Println("Running both MCP and REST servers (experimental)")
		go runRESTServer(client, *restPort)
		runMCPServer(client)
	default:
		fmt.Fprintf(os.Stderr, "Unknown mode: %s\n", *mode)
		os.Exit(1)
	}
}

func runMCPServer(client *api.Client) {
	// Create MCP server
	s := mcpserver.NewMCPServer(
		"moodle-mcp-server",
		"1.2.0",
		mcpserver.WithToolCapabilities(true),
	)

	// Register all tools
	registerTools(s, client)

	// Run the server over stdio
	log.Println("Moodle MCP Server starting...")
	if err := mcpserver.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

func runRESTServer(client *api.Client, port int) {
	restSrv := server.NewRESTServer(client, port)
	log.Printf("Starting REST API on port %d", port)
	log.Printf("OpenAPI docs at http://localhost:%d/api/docs", port)
	if err := restSrv.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "REST server error: %v\n", err)
		os.Exit(1)
	}
}

func registerTools(s *mcpserver.MCPServer, client *api.Client) {

	// ── Login ──────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("login",
			mcp.WithDescription("Log in to your Moodle account. This is the first step — provide your Moodle site URL, username, and password."),
			mcp.WithString("moodle_url", mcp.Required(), mcp.Description("Your Moodle site URL (e.g. https://online.uom.lk)")),
			mcp.WithString("username", mcp.Required(), mcp.Description("Your Moodle username or email")),
			mcp.WithString("password", mcp.Required(), mcp.Description("Your Moodle password")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			url := mcp.ParseString(req, "moodle_url", "")
			user := mcp.ParseString(req, "username", "")
			pass := mcp.ParseString(req, "password", "")
			result, err := tools.HandleLogin(ctx, client, tools.LoginInput{
				MoodleURL: url, Username: user, Password: pass,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Get Site Info ──────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_site_info",
			mcp.WithDescription("Get information about the connected Moodle site and the logged-in user."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			result, err := tools.HandleGetSiteInfo(ctx, client, tools.GetSiteInfoInput{})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Get User Profile ──────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_user_profile",
			mcp.WithDescription("Get the detailed profile of the currently logged-in user."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			result, err := tools.HandleGetUserProfile(ctx, client, tools.GetUserProfileInput{})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── List Courses ──────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("list_courses",
			mcp.WithDescription("List all courses the student is enrolled in."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			result, err := tools.HandleListCourses(ctx, client, tools.ListCoursesInput{})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Get Course Contents ───────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_course_contents",
			mcp.WithDescription("Get sections, resources, and activities of a specific course."),
			mcp.WithNumber("course_id", mcp.Required(), mcp.Description("The Moodle course ID (get it from list_courses)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := intArg(req, "course_id")
			result, err := tools.HandleGetCourseContents(ctx, client, tools.GetCourseContentsInput{CourseID: id})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Get Course Details ────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_course_details",
			mcp.WithDescription("Get detailed metadata about a specific course (description, dates, format)."),
			mcp.WithNumber("course_id", mcp.Required(), mcp.Description("The Moodle course ID")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := intArg(req, "course_id")
			result, err := tools.HandleGetCourseDetails(ctx, client, tools.GetCourseDetailsInput{CourseID: id})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Get Grades ────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_grades",
			mcp.WithDescription("Get all grade items and scores for a specific course."),
			mcp.WithNumber("course_id", mcp.Required(), mcp.Description("The Moodle course ID to get grades for")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := intArg(req, "course_id")
			result, err := tools.HandleGetGrades(ctx, client, tools.GetGradesInput{CourseID: id})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Get Grades Overview ───────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_grades_overview",
			mcp.WithDescription("Get a summary of grades across all enrolled courses."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			result, err := tools.HandleGetGradesOverview(ctx, client, tools.GetGradesOverviewInput{})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Get Assignments ───────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_assignments",
			mcp.WithDescription("Get all assignments for a specific course with due dates and status."),
			mcp.WithNumber("course_id", mcp.Required(), mcp.Description("The Moodle course ID")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := intArg(req, "course_id")
			result, err := tools.HandleGetAssignments(ctx, client, tools.GetAssignmentsInput{CourseID: id})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Get Upcoming Assignments ──────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_upcoming_assignments",
			mcp.WithDescription("Get all upcoming assignments across all enrolled courses, sorted by due date."),
			mcp.WithNumber("days_ahead", mcp.Description("Number of days to look ahead (default: 30)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			days := intArg(req, "days_ahead")
			result, err := tools.HandleGetUpcomingAssignments(ctx, client, tools.GetUpcomingAssignmentsInput{DaysAhead: days})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Submit Assignment ─────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("submit_assignment",
			mcp.WithDescription("Submit text content for an online text assignment."),
			mcp.WithNumber("assignment_id", mcp.Required(), mcp.Description("The assignment ID to submit to")),
			mcp.WithString("text", mcp.Required(), mcp.Description("The text content to submit")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := intArg(req, "assignment_id")
			text := mcp.ParseString(req, "text", "")
			result, err := tools.HandleSubmitAssignment(ctx, client, tools.SubmitAssignmentInput{
				AssignmentID: id, Text: text,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Update Assignment ─────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("update_assignment",
			mcp.WithDescription("Update (overwrite) an existing online text assignment submission."),
			mcp.WithNumber("assignment_id", mcp.Required(), mcp.Description("The assignment ID to update")),
			mcp.WithString("text", mcp.Required(), mcp.Description("The new text content to replace the existing submission")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := intArg(req, "assignment_id")
			text := mcp.ParseString(req, "text", "")
			result, err := tools.HandleUpdateAssignment(ctx, client, tools.UpdateAssignmentInput{
				AssignmentID: id, Text: text,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── List Resources ────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("list_resources",
			mcp.WithDescription("List all downloadable files (PDFs, slides, videos) in a course with their module IDs and sizes."),
			mcp.WithNumber("course_id", mcp.Required(), mcp.Description("The Moodle course ID")),
			mcp.WithString("mime_type", mcp.Description("Filter by MIME type e.g. application/pdf (optional)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := intArg(req, "course_id")
			mimeType := mcp.ParseString(req, "mime_type", "")
			result, err := tools.HandleListResources(ctx, client, tools.ListResourcesInput{
				CourseID: id, MimeType: mimeType,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Download Resource ─────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("download_resource",
			mcp.WithDescription("Download a file (PDF, slides, etc.) from Moodle and save it locally. Use list_resources to find module IDs."),
			mcp.WithNumber("course_id", mcp.Required(), mcp.Description("The Moodle course ID")),
			mcp.WithNumber("module_id", mcp.Required(), mcp.Description("The module ID of the resource (from list_resources)")),
			mcp.WithString("save_dir", mcp.Description("Directory to save the file (default: ~/Downloads)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			courseID := intArg(req, "course_id")
			moduleID := intArg(req, "module_id")
			saveDir := mcp.ParseString(req, "save_dir", "")
			result, err := tools.HandleDownloadResource(ctx, client, tools.DownloadResourceInput{
				CourseID: courseID, ModuleID: moduleID, SaveDir: saveDir,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Get Journal Entry ─────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_journal_entry",
			mcp.WithDescription("Get the current text of a journal entry (modname=journal activities). Use course contents to find the journal ID."),
			mcp.WithNumber("journal_id", mcp.Required(), mcp.Description("The journal module ID from course contents (modname=journal)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := intArg(req, "journal_id")
			result, err := tools.HandleGetJournalEntry(ctx, client, tools.GetJournalEntryInput{JournalID: id})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Submit Journal Entry ──────────────────────────────────────
	s.AddTool(
		mcp.NewTool("submit_journal",
			mcp.WithDescription("Submit or update a journal entry (modname=journal activities such as Technical Article Review, Research Paper Review). Use course contents to find the journal ID."),
			mcp.WithNumber("journal_id", mcp.Required(), mcp.Description("The journal module ID from course contents (modname=journal)")),
			mcp.WithString("text", mcp.Required(), mcp.Description("The text content to submit (HTML supported)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := intArg(req, "journal_id")
			text := mcp.ParseString(req, "text", "")
			result, err := tools.HandleSubmitJournal(ctx, client, tools.SubmitJournalInput{
				JournalID: id, Text: text,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Get Calendar Events ───────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_calendar_events",
			mcp.WithDescription("Get upcoming calendar events from all enrolled courses."),
			mcp.WithNumber("days_ahead", mcp.Description("Number of days to look ahead (default: 30)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			days := intArg(req, "days_ahead")
			result, err := tools.HandleGetCalendarEvents(ctx, client, tools.GetCalendarEventsInput{DaysAhead: days})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Get Upcoming Deadlines ────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_upcoming_deadlines",
			mcp.WithDescription("Get a consolidated view of all upcoming deadlines (assignments, quizzes, etc.), sorted by urgency."),
			mcp.WithNumber("days_ahead", mcp.Description("Number of days to look ahead (default: 14)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			days := intArg(req, "days_ahead")
			result, err := tools.HandleGetUpcomingDeadlines(ctx, client, tools.GetUpcomingDeadlinesInput{DaysAhead: days})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Get Notifications ─────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_notifications",
			mcp.WithDescription("Get recent messages and notifications from Moodle."),
			mcp.WithNumber("limit", mcp.Description("Maximum number of notifications (default: 20)")),
			mcp.WithBoolean("unread_only", mcp.Description("Only show unread notifications (default: true)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			limit := intArg(req, "limit")
			unreadOnly := true
			if v, ok := req.GetArguments()["unread_only"]; ok {
				if b, ok := v.(bool); ok {
					unreadOnly = b
				}
			}
			result, err := tools.HandleGetNotifications(ctx, client, tools.GetNotificationsInput{
				Limit: limit, UnreadOnly: unreadOnly,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)
}

// intArg extracts an integer from request arguments (JSON numbers are float64).
func intArg(req mcp.CallToolRequest, key string) int {
	if v, ok := req.GetArguments()[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return int(i)
			}
		}
	}
	return 0
}
