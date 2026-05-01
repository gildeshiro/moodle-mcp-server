package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client is the Moodle REST API client.
type Client struct {
	mu      sync.RWMutex
	baseURL string
	token   string
	userID  int
	http    *http.Client
}

// NewClient creates a new Moodle API client.
func NewClient() *Client {
	return &Client{
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// SetSession configures the client with a Moodle URL and token.
func (c *Client) SetSession(baseURL, token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.baseURL = baseURL
	c.token = token
}

// SetUserID stores the current user's Moodle ID.
func (c *Client) SetUserID(id int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.userID = id
}

// GetUserID returns the current user's Moodle ID.
func (c *Client) GetUserID() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.userID
}

// IsAuthenticated returns true if the client has a valid session.
func (c *Client) IsAuthenticated() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.baseURL != "" && c.token != ""
}

// GetBaseURL returns the configured Moodle base URL.
func (c *Client) GetBaseURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.baseURL
}

// GetToken returns the current authentication token.
func (c *Client) GetToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// Call makes a request to the Moodle REST API.
// function is the wsfunction name (e.g. "core_enrol_get_users_courses").
// params are additional query parameters.
func (c *Client) Call(ctx context.Context, function string, params map[string]string) (json.RawMessage, error) {
	c.mu.RLock()
	baseURL := c.baseURL
	token := c.token
	c.mu.RUnlock()

	if baseURL == "" || token == "" {
		return nil, ErrNotAuthenticated
	}

	endpoint := fmt.Sprintf("%s/webservice/rest/server.php", baseURL)

	qp := url.Values{
		"wstoken":             {token},
		"wsfunction":          {function},
		"moodlewsrestformat":  {"json"},
	}
	for k, v := range params {
		qp.Set(k, v)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.URL.RawQuery = qp.Encode()

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling Moodle API (%s): %w", function, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	// Check if the response is a Moodle error object
	var apiErr struct {
		ErrorCode string `json:"errorcode"`
		Message   string `json:"message"`
		Exception string `json:"exception"`
	}
	if json.Unmarshal(body, &apiErr) == nil && apiErr.ErrorCode != "" {
		return nil, &APIError{
			ErrorCode: apiErr.ErrorCode,
			Message:   apiErr.Message,
			Exception: apiErr.Exception,
		}
	}

	return json.RawMessage(body), nil
}

// UploadFile uploads a file to Moodle's draft area via the non-JSON
// /webservice/upload.php endpoint and returns the draft itemid that can
// later be referenced from JSON-RPC calls (e.g. mod_assign_save_submission).
func (c *Client) UploadFile(ctx context.Context, content []byte, filename string) (int, error) {
	c.mu.RLock()
	baseURL := c.baseURL
	token := c.token
	c.mu.RUnlock()

	if baseURL == "" || token == "" {
		return 0, ErrNotAuthenticated
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	// Form fields. Token also goes in the URL but Moodle accepts it here too.
	for key, val := range map[string]string{
		"token":    token,
		"filearea": "draft",
		"itemid":   "0",
		"filepath": "/",
		"filename": filename,
	} {
		if err := mw.WriteField(key, val); err != nil {
			return 0, fmt.Errorf("writing form field %q: %w", key, err)
		}
	}

	// File part. Moodle's upload.php expects the part name to start with "file_".
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name="file_1"; filename=%q`, filename))
	hdr.Set("Content-Type", "application/octet-stream")
	part, err := mw.CreatePart(hdr)
	if err != nil {
		return 0, fmt.Errorf("creating file part: %w", err)
	}
	if _, err := part.Write(content); err != nil {
		return 0, fmt.Errorf("writing file content: %w", err)
	}
	if err := mw.Close(); err != nil {
		return 0, fmt.Errorf("closing multipart writer: %w", err)
	}

	endpoint := strings.TrimRight(baseURL, "/") +
		"/webservice/upload.php?token=" + url.QueryEscape(token)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return 0, fmt.Errorf("creating upload request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("uploading file: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("reading upload response: %w", err)
	}

	// upload.php may return either a JSON array of file records on success
	// or a JSON object with errorcode/exception on failure.
	var apiErr struct {
		ErrorCode string `json:"errorcode"`
		Message   string `json:"message"`
		Exception string `json:"exception"`
		Error     string `json:"error"`
	}
	if json.Unmarshal(respBody, &apiErr) == nil &&
		(apiErr.ErrorCode != "" || apiErr.Exception != "" || apiErr.Error != "") {
		msg := apiErr.Message
		if msg == "" {
			msg = apiErr.Error
		}
		return 0, &APIError{
			ErrorCode: apiErr.ErrorCode,
			Message:   msg,
			Exception: apiErr.Exception,
		}
	}

	var entries []struct {
		ItemID   int    `json:"itemid"`
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(respBody, &entries); err != nil {
		return 0, fmt.Errorf("parsing upload response: %w (body: %s)", err, string(respBody))
	}
	if len(entries) == 0 {
		return 0, fmt.Errorf("upload returned no entries (body: %s)", string(respBody))
	}

	return entries[0].ItemID, nil
}
