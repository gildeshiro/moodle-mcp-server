package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/jawadh/moodle-mcp-server/internal/api"
	"github.com/jawadh/moodle-mcp-server/internal/config"
	"github.com/jawadh/moodle-mcp-server/internal/oauth"
	"github.com/jawadh/moodle-mcp-server/internal/server"
	"github.com/jawadh/moodle-mcp-server/internal/tools"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func main() {
	// Parse command line flags
	mode := flag.String("mode", "mcp", "Server mode: 'mcp' (stdio), 'rest' (custom HTTP API), 'http' (MCP Streamable HTTP), 'both' (mcp+rest)")
	restPort := flag.Int("port", 8080, "REST API port (default: 8080)")
	authToken := flag.String("auth-token", "", "Bearer token for http mode (env: MCP_AUTH_TOKEN)")
	corsOrigins := flag.String("cors-origins", "", "Comma-separated CORS origins for http mode (env: MCP_CORS_ORIGINS)")
	httpPath := flag.String("http-path", "/mcp", "MCP endpoint path for http mode (env: MCP_HTTP_PATH)")
	useOAuth := flag.Bool("oauth", false, "Enable OAuth 2.1 + DCR for http mode (env: MCP_USE_OAUTH)")
	oauthIssuer := flag.String("oauth-issuer", "", "Public base URL of the deployment, required when -oauth (env: MCP_OAUTH_ISSUER)")
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

	if *authToken == "" {
		*authToken = os.Getenv("MCP_AUTH_TOKEN")
	}
	if *corsOrigins == "" {
		*corsOrigins = os.Getenv("MCP_CORS_ORIGINS")
	}
	if v := os.Getenv("MCP_HTTP_PATH"); v != "" {
		*httpPath = v
	}
	if os.Getenv("MCP_USE_OAUTH") == "1" {
		*useOAuth = true
	}
	if v := os.Getenv("MCP_OAUTH_ISSUER"); v != "" {
		*oauthIssuer = v
	}
	// Honor PORT env from cloud platforms (Railway, Render, Fly, Cloud Run)
	if envPort := os.Getenv("PORT"); envPort != "" {
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
	case "http":
		runStreamableHTTPServer(client, *restPort, *authToken, *corsOrigins, *httpPath, *useOAuth, *oauthIssuer)
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

func runStreamableHTTPServer(client *api.Client, port int, authToken, corsOrigins, path string, useOAuth bool, issuer string) {
	s := mcpserver.NewMCPServer(
		"moodle-mcp-server",
		"1.2.0",
		mcpserver.WithToolCapabilities(true),
	)
	registerTools(s, client)

	opts := server.StreamableOpts{
		Port:        port,
		CORSOrigins: corsOrigins,
		Path:        path,
		Version:     "1.2.0",
	}
	switch {
	case useOAuth:
		// OAuth 2.1 + DCR mode: the provider becomes the bearer issuer; the
		// boot guard in RunStreamable will reject if AuthToken/AllowNoAuth is
		// also set, so we deliberately leave them empty here.
		if issuer == "" {
			fmt.Fprintln(os.Stderr, "MCP_OAUTH_ISSUER is required when MCP_USE_OAUTH=1 (the public base URL of this deployment, e.g. https://moodle-mcp.example.com)")
			os.Exit(1)
		}
		opts.OAuthProvider = oauth.NewProvider(issuer)
	default:
		// Existing modes: bearer-static or URL-as-secret. Mutually exclusive
		// with OAuth, enforced by the boot guard in RunStreamable.
		opts.AuthToken = authToken
		opts.AllowNoAuth = os.Getenv("MCP_DISABLE_AUTH") == "1"
	}
	if err := server.RunStreamable(context.Background(), s, opts); err != nil {
		fmt.Fprintf(os.Stderr, "streamable server error: %v\n", err)
		os.Exit(1)
	}
}

func registerTools(s *mcpserver.MCPServer, client *api.Client) {

	// ── Login ──────────────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("login",
			mcp.WithDescription("Log in to your Moodle account. This is the first step — provide your Moodle site URL, username, and password. (In remote HTTP mode, prefer pre-configuring MOODLE_TOKEN server-side so credentials don't transit the network.)"),
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

	// ── Read Resource (inline; preferred for remote/HTTP mode) ────
	s.AddTool(
		mcp.NewTool("read_resource",
			mcp.WithDescription("Fetch a file from Moodle and return its content INLINE so the model can read it directly. Plain text is extracted server-side for: PDFs, .docx, .pptx, .xlsx, and any text/* MIME (works in clients that don't render binary blobs, e.g. claude.ai web). Image-only / scanned PDFs whose text extraction is empty are rendered to PNG (up to 10 pages, 150 DPI) and returned as ImageContent so the model's vision can read them. Other binary types fall back to a base64 BlobResourceContents (max 10 MB raw). Files up to 50 MB raw are accepted as long as their extracted text or rendered pages fit the response. Folder modules contain multiple files — use list_resources to discover (module_id, file_index) pairs. Preferred over download_resource for any client that cannot read the server's filesystem."),
			mcp.WithNumber("course_id", mcp.Required(), mcp.Description("The Moodle course ID")),
			mcp.WithNumber("module_id", mcp.Required(), mcp.Description("The module ID of the resource (from list_resources)")),
			mcp.WithNumber("file_index", mcp.Description("0-based index into the module's file list. Default 0. For folder modules, list_resources reports the index for each contained file.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			out, err := tools.HandleReadResource(ctx, client, tools.ReadResourceInput{
				CourseID:  intArg(req, "course_id"),
				ModuleID:  intArg(req, "module_id"),
				FileIndex: intArg(req, "file_index"),
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			// First content block: human-readable description.
			content := []mcp.Content{
				mcp.TextContent{Type: mcp.ContentTypeText, Text: out.Description},
			}
			// Three mutually-exclusive payload paths, picked in priority order:
			//   1) Extracted text (PDFs, Office docs, text/*) — universal client support.
			//   2) Rendered PDF pages as ImageContent — for scanned/image-only PDFs;
			//      claude.ai's vision can read them. Only fires when ExtractedText
			//      was empty AND pdftoppm rendered at least one page.
			//   3) Raw bytes as a base64 BlobResourceContents — last resort for
			//      binaries that have no text extractor and aren't a renderable PDF
			//      (e.g. small images, audio). Blob-rejecting clients see the
			//      description but not the content.
			switch {
			case out.ExtractedText != "":
				content = append(content, mcp.TextContent{
					Type: mcp.ContentTypeText,
					Text: "Content:\n\n" + out.ExtractedText,
				})
			case len(out.RenderedPNGs) > 0:
				for _, png := range out.RenderedPNGs {
					content = append(content, mcp.ImageContent{
						Type:     mcp.ContentTypeImage,
						Data:     base64.StdEncoding.EncodeToString(png),
						MIMEType: "image/png",
					})
				}
			default:
				content = append(content, mcp.EmbeddedResource{
					Type: mcp.ContentTypeResource,
					Resource: mcp.BlobResourceContents{
						URI:      out.URI,
						MIMEType: out.MimeType,
						Blob:     base64.StdEncoding.EncodeToString(out.Bytes),
					},
				})
			}
			// Optional render note (always last — explains the visual fallback).
			if out.RenderNote != "" {
				content = append(content, mcp.TextContent{
					Type: mcp.ContentTypeText,
					Text: out.RenderNote,
				})
			}
			return &mcp.CallToolResult{Content: content}, nil
		},
	)

	// ── Download Resource ─────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("download_resource",
			mcp.WithDescription("Download a file (PDF, slides, etc.) from Moodle and save it on the SERVER's filesystem. Useful in local stdio mode (Claude Desktop) where save_dir is the user's machine. In remote/HTTP mode prefer `read_resource` instead — this tool's output is not visible to the model. Use list_resources to find module IDs."),
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

	// ── Submit Assignment File ───────────────────────────────────
	s.AddTool(
		mcp.NewTool("submit_assignment_file",
			mcp.WithDescription("Submit a file (PDF, .docx, image, etc.) to a Moodle assignment that requires file upload. Two-step flow: uploads to Moodle's draft area then finalizes the submission. Pass file content as standard base64 in content_base64; data: URI prefix is also accepted."),
			mcp.WithNumber("assignment_id", mcp.Required(), mcp.Description("The Moodle assignment ID to submit to")),
			mcp.WithString("filename", mcp.Required(), mcp.Description("The filename to attach (e.g. essay.pdf)")),
			mcp.WithString("content_base64", mcp.Required(), mcp.Description("File content encoded as base64")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := intArg(req, "assignment_id")
			filename := mcp.ParseString(req, "filename", "")
			content := mcp.ParseString(req, "content_base64", "")
			result, err := tools.HandleSubmitAssignmentFile(ctx, client, tools.SubmitAssignmentFileInput{
				AssignmentID: id, Filename: filename, ContentBase64: content,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── List Forums ──────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("list_forums",
			mcp.WithDescription("List all Moodle forums (announcements, discussion forums) in a course."),
			mcp.WithNumber("course_id", mcp.Required(), mcp.Description("The Moodle course ID")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := intArg(req, "course_id")
			result, err := tools.HandleListForums(ctx, client, tools.ListForumsInput{CourseID: id})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── List Forum Discussions ───────────────────────────────────
	s.AddTool(
		mcp.NewTool("list_forum_discussions",
			mcp.WithDescription("List discussions in a Moodle forum, sorted by most recent activity."),
			mcp.WithNumber("forum_id", mcp.Required(), mcp.Description("The Moodle forum ID (from list_forums)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := intArg(req, "forum_id")
			result, err := tools.HandleListForumDiscussions(ctx, client, tools.ListForumDiscussionsInput{ForumID: id})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Get Forum Discussion ─────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_forum_discussion",
			mcp.WithDescription("Get the full thread of posts in a Moodle forum discussion (root post + all replies)."),
			mcp.WithNumber("discussion_id", mcp.Required(), mcp.Description("The Moodle discussion ID (from list_forum_discussions)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := intArg(req, "discussion_id")
			result, err := tools.HandleGetForumDiscussion(ctx, client, tools.GetForumDiscussionInput{DiscussionID: id})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Post Forum Reply ─────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("post_forum_reply",
			mcp.WithDescription("Post a reply to a Moodle forum post. post_id is the PARENT post id (use get_forum_discussion to find it). HTML formatting is supported in the message body."),
			mcp.WithNumber("post_id", mcp.Required(), mcp.Description("The PARENT post ID to reply under (from get_forum_discussion)")),
			mcp.WithString("subject", mcp.Required(), mcp.Description("Subject line of the reply")),
			mcp.WithString("message", mcp.Required(), mcp.Description("The message body (HTML supported)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			postID := intArg(req, "post_id")
			subject := mcp.ParseString(req, "subject", "")
			message := mcp.ParseString(req, "message", "")
			result, err := tools.HandlePostForumReply(ctx, client, tools.PostForumReplyInput{
				PostID: postID, Subject: subject, Message: message,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── List Messages ────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("list_messages",
			mcp.WithDescription("List Moodle direct messages received by the logged-in user (separate from notifications). Defaults to unread_only=true."),
			mcp.WithBoolean("unread_only", mcp.Description("Only show unread messages (default: true)")),
			mcp.WithNumber("limit", mcp.Description("Maximum number of messages (default: 20)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			limit := intArg(req, "limit")
			unreadOnly := true
			if v, ok := req.GetArguments()["unread_only"]; ok {
				if b, ok := v.(bool); ok {
					unreadOnly = b
				}
			}
			result, err := tools.HandleListMessages(ctx, client, tools.ListMessagesInput{
				UnreadOnly: unreadOnly, Limit: limit,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Send Message ─────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("send_message",
			mcp.WithDescription("Send a Moodle direct message to another user. Requires the recipient's Moodle user ID."),
			mcp.WithNumber("to_user_id", mcp.Required(), mcp.Description("The recipient's Moodle user ID")),
			mcp.WithString("message", mcp.Required(), mcp.Description("The message text to send (HTML supported)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			toID := intArg(req, "to_user_id")
			message := mcp.ParseString(req, "message", "")
			result, err := tools.HandleSendMessage(ctx, client, tools.SendMessageInput{
				ToUserID: toID, Message: message,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── List Quizzes ─────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("list_quizzes",
			mcp.WithDescription("List Moodle quizzes in a course with open/close dates and attempt limits."),
			mcp.WithNumber("course_id", mcp.Required(), mcp.Description("The Moodle course ID")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := intArg(req, "course_id")
			result, err := tools.HandleListQuizzes(ctx, client, tools.ListQuizzesInput{CourseID: id})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Get Quiz Attempts ────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_quiz_attempts",
			mcp.WithDescription("Get the logged-in user's attempt history for a Moodle quiz (state, start/finish times, sum of grades)."),
			mcp.WithNumber("quiz_id", mcp.Required(), mcp.Description("The Moodle quiz ID (from list_quizzes)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := intArg(req, "quiz_id")
			result, err := tools.HandleGetQuizAttempts(ctx, client, tools.GetQuizAttemptsInput{QuizID: id})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── List Lessons ─────────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("list_lessons",
			mcp.WithDescription("List Moodle lessons in a course with availability and deadline dates."),
			mcp.WithNumber("course_id", mcp.Required(), mcp.Description("The Moodle course ID")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := intArg(req, "course_id")
			result, err := tools.HandleListLessons(ctx, client, tools.ListLessonsInput{CourseID: id})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// ── Get Lesson Page ──────────────────────────────────────────
	s.AddTool(
		mcp.NewTool("get_lesson_page",
			mcp.WithDescription("Get a Moodle lesson page's HTML contents and navigation links. Pass page_id=0 (or omit) to fetch the entry page."),
			mcp.WithNumber("lesson_id", mcp.Required(), mcp.Description("The Moodle lesson ID (from list_lessons)")),
			mcp.WithNumber("page_id", mcp.Description("The lesson page ID (omit or pass 0 to fetch the first page)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			lessonID := intArg(req, "lesson_id")
			pageID := intArg(req, "page_id")
			result, err := tools.HandleGetLessonPage(ctx, client, tools.GetLessonPageInput{
				LessonID: lessonID, PageID: pageID,
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
