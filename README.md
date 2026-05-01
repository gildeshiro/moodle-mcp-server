# Moodle MCP Server

A [Model Context Protocol (MCP)](https://modelcontextprotocol.io) server + REST API that connects **Claude, ChatGPT, Google Gemini, and any AI model** to any Moodle LMS instance. Built in Go.

Students can interact with their Moodle account through their favorite AI — view courses, check grades, track deadlines, submit assignments, and read notifications.

**Works with:**
- ✅ **Claude** (Desktop, Code) - via MCP (easiest!)
- ✅ **ChatGPT** (Plus) - via REST API + Actions
- ✅ **Google Gemini** - via REST API + Apps Script
- ✅ **Any AI** - via REST API (HTTP endpoints)

## Features

### Authentication & profile
| Tool | Description |
|------|-------------|
| `login` | Authenticate with your Moodle site (interactive — prefers `MOODLE_TOKEN` server-side in remote HTTP mode) |
| `get_site_info` | View Moodle site and user info |
| `get_user_profile` | View your profile details |

### Courses & content
| Tool | Description |
|------|-------------|
| `list_courses` | List all enrolled courses |
| `get_course_contents` | View sections, resources, and activities of a course |
| `get_course_details` | View course metadata (description, dates, format) |
| `list_resources` | List downloadable files in a course (folders enumerated per-file with `file_index`) |
| `read_resource` | Fetch a file INLINE — text extracted from PDF/.docx/.pptx/.xlsx/text-* (universal client support); image-only PDFs rendered as PNGs for vision; raw blob fallback otherwise. Up to 50 MB raw. |
| `download_resource` | Save a file to the SERVER's filesystem (useful only in stdio mode where save_dir is the user's machine; in HTTP mode prefer `read_resource`) |

### Grades & assignments
| Tool | Description |
|------|-------------|
| `get_grades` | Grade items and scores for a specific course |
| `get_grades_overview` | Grade summary across all enrolled courses |
| `get_assignments` | All assignments for a course with due dates and status |
| `get_upcoming_assignments` | Upcoming assignments across all enrolled courses |
| `submit_assignment` | Submit text content for an online-text assignment |
| `update_assignment` | Update (overwrite) an existing online-text submission |
| `submit_assignment_file` | Submit a file (base64) for a file-upload assignment |

### Forums & messaging
| Tool | Description |
|------|-------------|
| `list_forums` | List forums in a course |
| `list_forum_discussions` | List discussions in a forum |
| `get_forum_discussion` | Read posts in a discussion |
| `post_forum_reply` | Reply to a discussion (HTML supported; optional file attachments via `attachments: [{filename, content_base64}]`) |
| `list_messages` | Inbox messages (filter by unread, with limit) |
| `send_message` | Send a direct message to another user |

### Quizzes & lessons
| Tool | Description |
|------|-------------|
| `list_quizzes` | Quizzes in a course with open/close dates and grade info |
| `get_quiz_attempts` | Your past attempts and grades for a quiz |
| `start_quiz_attempt` | Begin a new attempt on a quiz (returns `attempt_id` + layout) |
| `get_quiz_question` | Read questions on a page of an in-progress attempt; extracts opaque `answer_field_names` for the model to echo back |
| `save_quiz_answers` | Save answers for a page (without finalizing). `answers` is a map keyed by `answer_field_names` |
| `submit_quiz_attempt` | Submit the current page (`finalize=false`) or finalize the whole attempt (`finalize=true`). Supports multichoice (single/multi), truefalse, shortanswer, numerical, essay-text. Drag-drop / hot-spot / gap-select / matching are NOT covered. |
| `list_lessons` | Lessons in a course |
| `get_lesson_page` | Read a lesson page's content (auto-picks entry page) |

### Journals
| Tool | Description |
|------|-------------|
| `get_journal_entry` | Read your current journal entry text |
| `submit_journal` | Submit / update a journal entry (HTML supported) |

### Calendar & notifications
| Tool | Description |
|------|-------------|
| `get_calendar_events` | Upcoming calendar events from all enrolled courses |
| `get_upcoming_deadlines` | Consolidated deadlines (assignments, quizzes) sorted by urgency |
| `get_notifications` | Messages and notifications (unread filter, with limit) |

## Requirements

- Claude Desktop (macOS, Windows, or Linux)
- A Moodle account at any institution

## Quick Start

**Choose your AI platform:**

| Your AI | Guide | Time |
|---------|-------|------|
| 🤖 **Claude** (Recommended) | [Windows](WINDOWS_SETUP.md) / [macOS](MAC_SETUP.md) | 2 min |
| 💬 **ChatGPT** | [ChatGPT Setup](CHATGPT_SETUP.md) | 15 min |
| 🔍 **Google Gemini** | [Gemini Setup](GEMINI_SETUP.md) / [Gemini Windows](GEMINI_WINDOWS_SETUP.md) | 20 min |
| 🌐 **Multiple AIs** | [All Models Guide](ALL_MODELS_SETUP.md) | 1 hour |

**Start here:** If you use Claude, follow the Windows/macOS guide above (2 minutes!)

**Not a coder?** All guides have step-by-step instructions with no technical knowledge needed.

---

## Installation (Easiest)

### For Windows (PowerShell)

Open PowerShell and run:

```powershell
irm https://raw.githubusercontent.com/Jawadh-Salih/moodle-mcp-server/main/install.ps1 | iex
```

This will automatically download and install the binary to `C:\Users\YourName\moodle-mcp\moodle-mcp.exe`

### For macOS / Linux (Bash)

Open Terminal and run:

```bash
curl -fsSL https://raw.githubusercontent.com/Jawadh-Salih/moodle-mcp-server/main/install.sh | bash
```

This will automatically download and install the binary to `~/.moodle-mcp/moodle-mcp`

### Manual Installation (For Developers)

If you want to build from source:

```bash
# Clone the repository
git clone https://github.com/Jawadh-Salih/moodle-mcp-server.git
cd moodle-mcp-server

# Build the binary
go mod tidy
go build -o moodle-mcp ./cmd/moodle-mcp/
```

## Remote (HTTP) mode — for claude.ai custom connectors

The server can also run as a remote MCP endpoint over Streamable HTTP, suitable for [claude.ai custom connectors](https://support.claude.com/en/articles/11503834) and any other client that speaks the MCP Streamable HTTP transport.

### Quick start (local)

```bash
# Generate a strong shared secret
export MCP_AUTH_TOKEN=$(openssl rand -hex 32)

# Configure your Moodle session (one of the two options below)
export MOODLE_URL=https://your.moodle.example
export MOODLE_TOKEN=<your-moodle-mobile-token>
# or use username/password (server fetches a token at boot):
# export MOODLE_USERNAME=you; export MOODLE_PASSWORD=...

./moodle-mcp -mode http -port 8080
```

Then in **claude.ai → Settings → Connectors → Add custom connector**:

- **URL:** `https://<your-deployment>/mcp`
- **Custom header:** `Authorization: Bearer <your MCP_AUTH_TOKEN>`

### Available knobs

| Flag | Env var | Default | Purpose |
|---|---|---|---|
| `-auth-token` | `MCP_AUTH_TOKEN` | — (required) | Shared bearer secret |
| `-port` | `PORT`, `REST_API_PORT` | `8080` | TCP port to listen on. Note: env vars override the flag (cloud platforms like Railway/Render/Fly inject `PORT`). |
| `-cors-origins` | `MCP_CORS_ORIGINS` | (empty) | Comma-separated origins (e.g. `https://claude.ai,https://claude.com`) |
| `-http-path` | `MCP_HTTP_PATH` | `/mcp` | Endpoint path |

The server refuses to boot in `http` mode without an auth token (security guardrail). The `/healthz` endpoint is unauthenticated for cloud load balancers. See [DEPLOYMENT_GUIDE.md](DEPLOYMENT_GUIDE.md#claudeai-custom-connector) for hosted deployment recipes.

## Usage

### Option 1: Interactive Login (Recommended)

Just start the server with no configuration. When you chat with Claude, use the `login` tool to authenticate:

> "Log in to my Moodle at https://online.uom.lk with username student@uom.lk"

Claude will ask for your password and authenticate you.

### Option 2: Environment Variables

Set credentials as environment variables for automatic login:

```bash
export MOODLE_URL=https://online.uom.lk
export MOODLE_USERNAME=your-username
export MOODLE_PASSWORD=your-password
```

Or if you have a Moodle API token:

```bash
export MOODLE_URL=https://online.uom.lk
export MOODLE_TOKEN=your-api-token
```

## Claude Desktop Configuration

### If you used the auto-installer:

The installer will show you the exact path. Just copy it!

### Manual Configuration

Find your Claude Desktop config file:

**macOS:** `~/Library/Application Support/Claude/claude_desktop_config.json`
**Windows:** `%APPDATA%\Claude\claude_desktop_config.json`
**Linux:** `~/.config/Claude/claude_desktop_config.json`

#### Option A: Interactive login (Recommended - no credentials in config)

```json
{
  "mcpServers": {
    "moodle": {
      "command": "/path/to/moodle-mcp"
    }
  }
}
```

Then in Claude, use the `login` tool to authenticate interactively.

#### Option B: With credentials stored

```json
{
  "mcpServers": {
    "moodle": {
      "command": "/path/to/moodle-mcp",
      "env": {
        "MOODLE_URL": "https://online.uom.lk",
        "MOODLE_USERNAME": "your-username",
        "MOODLE_PASSWORD": "your-password"
      }
    }
  }
}
```

**Windows example paths:**
- Auto-installer: `C:\Users\YourName\moodle-mcp\moodle-mcp.exe`
- Manual build: `C:\Users\YourName\Go\bin\moodle-mcp.exe`

**macOS example paths:**
- Auto-installer: `/Users/yourname/.moodle-mcp/moodle-mcp`
- Manual build: `/Users/yourname/moodle-mcp-server/moodle-mcp`

## Supports Multiple AI Platforms

### Via MCP (Claude Only)
- ✅ Claude Desktop (macOS, Windows, Linux)
- ✅ Claude Code (VSCode, terminal)

### Via REST API (ChatGPT, Gemini, Any AI)
- ✅ ChatGPT (with Custom GPT Actions)
- ✅ Google Gemini (with Apps Script)
- ✅ Any AI with HTTP client access
- ✅ Custom scripts and integrations

See [All Models Setup](ALL_MODELS_SETUP.md) for detailed instructions for each platform.

## Running the REST API Server

For ChatGPT, Gemini, or other AI models, run the REST API mode:

```bash
# Start REST API server
go run ./cmd/moodle-mcp/ -mode rest -port 8080

# Or if you built the binary:
./moodle-mcp -mode rest -port 8080

# View API docs
curl http://localhost:8080/api/docs
```

The server listens on `http://localhost:8080` and exposes REST endpoints:
- `POST /api/login` - Authenticate
- `GET /api/courses` - List courses
- `GET /api/grades?course_id=123` - Get grades
- `GET /api/assignments/upcoming` - Upcoming assignments
- And more!

For production (ChatGPT/Gemini), deploy to cloud:
- [Google Cloud Run](DEPLOYMENT_GUIDE.md) (Recommended for Gemini)
- [Heroku](DEPLOYMENT_GUIDE.md) (Simplest)
- [DigitalOcean](DEPLOYMENT_GUIDE.md) (Most control)

---

## Example Conversations

Once connected, you can ask Claude things like:

- "Show me my enrolled courses"
- "What are my grades in CS101?"
- "What assignments are due this week?"
- "Show me the contents of my Data Structures course"
- "Do I have any unread notifications?"
- "What deadlines are coming up in the next 7 days?"

## How It Works

This server uses Moodle's [Web Services REST API](https://docs.moodle.org/dev/Web_service_API_functions) with the `moodle_mobile_app` service token. This service is enabled by default on most Moodle installations, so no admin setup is needed.

## Troubleshooting

**"Invalid login" error:** Double-check your username and password. Some institutions use email as username, others use a separate ID.

**"Web service not available" error:** Your Moodle admin may have disabled the mobile web service. Ask them to enable it under *Site administration > Plugins > Web services > Mobile*.

**Grades not showing:** The grade report API requires the `gradereport/user:view` capability, which is standard for students but may be restricted on some sites.

## License

MIT
