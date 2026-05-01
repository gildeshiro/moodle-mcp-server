package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jawadh/moodle-mcp-server/internal/api"
)

// --- Resource types ---

type FileContent struct {
	Type     string `json:"type"`
	Filename string `json:"filename"`
	FileURL  string `json:"fileurl"`
	FileSize int64  `json:"filesize"`
	MimeType string `json:"mimetype"`
}

type ResourceModule struct {
	ID       int           `json:"id"`
	Name     string        `json:"name"`
	ModName  string        `json:"modname"`
	Contents []FileContent `json:"contents"`
}

type ResourceSection struct {
	Name    string           `json:"name"`
	Modules []ResourceModule `json:"modules"`
}

type ResourceDisplay struct {
	ModuleID int    `json:"module_id"`
	Name     string `json:"name"`
	Filename string `json:"filename"`
	SizeMB   string `json:"size_mb"`
	MimeType string `json:"mime_type"`
	Section  string `json:"section"`
}

// --- List Resources Tool ---

type ListResourcesInput struct {
	CourseID int    `json:"course_id" jsonschema:"description=The Moodle course ID to list downloadable files for"`
	MimeType string `json:"mime_type,omitempty" jsonschema:"description=Filter by MIME type e.g. application/pdf (optional)"`
}

func HandleListResources(ctx context.Context, client *api.Client, input ListResourcesInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}
	if input.CourseID == 0 {
		return "", fmt.Errorf("course_id is required")
	}

	sections, err := getCourseContentsRaw(ctx, client, input.CourseID)
	if err != nil {
		return "", err
	}

	var resources []ResourceDisplay
	for _, sec := range sections {
		for _, mod := range sec.Modules {
			for _, f := range mod.Contents {
				if f.Type != "file" {
					continue
				}
				if input.MimeType != "" && !strings.Contains(f.MimeType, input.MimeType) {
					continue
				}
				resources = append(resources, ResourceDisplay{
					ModuleID: mod.ID,
					Name:     mod.Name,
					Filename: f.Filename,
					SizeMB:   fmt.Sprintf("%.1f MB", float64(f.FileSize)/1024/1024),
					MimeType: f.MimeType,
					Section:  sec.Name,
				})
			}
		}
	}

	result, _ := json.MarshalIndent(map[string]interface{}{
		"course_id":      input.CourseID,
		"total_files":    len(resources),
		"resources":      resources,
	}, "", "  ")
	return string(result), nil
}

// --- Read Resource Tool (returns content inline; useful for remote/HTTP mode) ---

// MaxInlineFileBytes caps the size of files returned via HandleReadResource.
// Anything larger should be retrieved with download_resource (stdio-mode) or
// fetched outside the MCP path entirely. The base64 expansion of this limit
// is ~13.3 MB on the wire, which most MCP clients comfortably handle.
const MaxInlineFileBytes int64 = 10 * 1024 * 1024 // 10 MB

type ReadResourceInput struct {
	CourseID int `json:"course_id" jsonschema:"description=The Moodle course ID containing the resource"`
	ModuleID int `json:"module_id" jsonschema:"description=The module ID of the resource to read (get from list_resources)"`
}

// ReadResourceOutput is the structured payload returned to the tool registration
// layer; the registration converts it into an mcp.CallToolResult that embeds the
// bytes as a base64 BlobResourceContents alongside a human-readable description.
type ReadResourceOutput struct {
	Description string // text shown to the model (filename, size, mime)
	URI         string // canonical resource URI
	Filename    string
	MimeType    string
	Size        int64
	Bytes       []byte // raw file bytes; caller is responsible for base64-encoding
}

func HandleReadResource(ctx context.Context, client *api.Client, input ReadResourceInput) (*ReadResourceOutput, error) {
	if !client.IsAuthenticated() {
		return nil, api.ErrNotAuthenticated
	}
	if input.CourseID == 0 {
		return nil, fmt.Errorf("course_id is required")
	}
	if input.ModuleID == 0 {
		return nil, fmt.Errorf("module_id is required")
	}

	sections, err := getCourseContentsRaw(ctx, client, input.CourseID)
	if err != nil {
		return nil, err
	}

	var targetFile *FileContent
	var moduleName string
	for _, sec := range sections {
		for _, mod := range sec.Modules {
			if mod.ID == input.ModuleID {
				moduleName = mod.Name
				for _, f := range mod.Contents {
					if f.Type == "file" {
						fc := f
						targetFile = &fc
						break
					}
				}
			}
		}
	}
	if targetFile == nil {
		return nil, fmt.Errorf("no downloadable file found for module_id %d in course %d", input.ModuleID, input.CourseID)
	}
	if targetFile.FileSize > MaxInlineFileBytes {
		return nil, fmt.Errorf("file %q is %.1f MB, exceeds %d MB inline limit; use download_resource (local stdio mode) instead",
			targetFile.Filename, float64(targetFile.FileSize)/1024/1024, MaxInlineFileBytes/(1024*1024))
	}

	downloadURL := targetFile.FileURL
	if strings.Contains(downloadURL, "?") {
		downloadURL += "&token=" + client.GetToken()
	} else {
		downloadURL += "?token=" + client.GetToken()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpClient := &http.Client{Timeout: 120 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch failed with status %d", resp.StatusCode)
	}

	// Cap the read so a server lying about Content-Length can't OOM us.
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxInlineFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}
	if int64(len(body)) > MaxInlineFileBytes {
		return nil, fmt.Errorf("file body exceeded %d MB inline limit during streaming", MaxInlineFileBytes/(1024*1024))
	}

	desc := fmt.Sprintf("Module %q file %q (%s, %.1f MB) returned inline as base64 blob.",
		moduleName, targetFile.Filename, targetFile.MimeType, float64(len(body))/1024/1024)

	return &ReadResourceOutput{
		Description: desc,
		URI:         fmt.Sprintf("moodle://course/%d/module/%d/%s", input.CourseID, input.ModuleID, targetFile.Filename),
		Filename:    targetFile.Filename,
		MimeType:    targetFile.MimeType,
		Size:        int64(len(body)),
		Bytes:       body,
	}, nil
}

// --- Download Resource Tool ---

type DownloadResourceInput struct {
	CourseID int    `json:"course_id" jsonschema:"description=The Moodle course ID containing the resource"`
	ModuleID int    `json:"module_id" jsonschema:"description=The module ID of the resource to download (get from list_resources)"`
	SaveDir  string `json:"save_dir,omitempty" jsonschema:"description=Directory to save the file (default: ~/Downloads)"`
}

func HandleDownloadResource(ctx context.Context, client *api.Client, input DownloadResourceInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}
	if input.CourseID == 0 {
		return "", fmt.Errorf("course_id is required")
	}
	if input.ModuleID == 0 {
		return "", fmt.Errorf("module_id is required")
	}

	// Resolve save directory
	saveDir := input.SaveDir
	if saveDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			saveDir = "."
		} else {
			saveDir = filepath.Join(home, "Downloads")
		}
	}
	if err := os.MkdirAll(saveDir, 0755); err != nil {
		return "", fmt.Errorf("creating save directory: %w", err)
	}

	// Find the file in course contents
	sections, err := getCourseContentsRaw(ctx, client, input.CourseID)
	if err != nil {
		return "", err
	}

	var targetFile *FileContent
	var moduleName string
	for _, sec := range sections {
		for _, mod := range sec.Modules {
			if mod.ID == input.ModuleID {
				moduleName = mod.Name
				for _, f := range mod.Contents {
					if f.Type == "file" {
						fc := f
						targetFile = &fc
						break
					}
				}
			}
		}
	}

	if targetFile == nil {
		return "", fmt.Errorf("no downloadable file found for module_id %d in course %d", input.ModuleID, input.CourseID)
	}

	// Build authenticated download URL
	downloadURL := targetFile.FileURL
	if strings.Contains(downloadURL, "?") {
		downloadURL += "&token=" + client.GetToken()
	} else {
		downloadURL += "?token=" + client.GetToken()
	}

	// Download the file
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating download request: %w", err)
	}

	httpClient := &http.Client{Timeout: 120 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	savePath := filepath.Join(saveDir, targetFile.Filename)
	out, err := os.Create(savePath)
	if err != nil {
		return "", fmt.Errorf("creating file: %w", err)
	}
	defer out.Close()

	written, err := io.Copy(out, resp.Body)
	if err != nil {
		return "", fmt.Errorf("saving file: %w", err)
	}

	result, _ := json.MarshalIndent(map[string]interface{}{
		"success":     true,
		"module_name": moduleName,
		"filename":    targetFile.Filename,
		"saved_to":    savePath,
		"size_mb":     fmt.Sprintf("%.1f MB", float64(written)/1024/1024),
		"mime_type":   targetFile.MimeType,
	}, "", "  ")
	return string(result), nil
}

// --- Internal helpers ---

func getCourseContentsRaw(ctx context.Context, client *api.Client, courseID int) ([]ResourceSection, error) {
	params := map[string]string{
		"courseid": fmt.Sprintf("%d", courseID),
	}
	data, err := client.Call(ctx, "core_course_get_contents", params)
	if err != nil {
		return nil, fmt.Errorf("getting course contents: %w", err)
	}

	var raw []struct {
		Name    string `json:"name"`
		Modules []struct {
			ID       int    `json:"id"`
			Name     string `json:"name"`
			ModName  string `json:"modname"`
			Contents []struct {
				Type     string `json:"type"`
				Filename string `json:"filename"`
				FileURL  string `json:"fileurl"`
				FileSize int64  `json:"filesize"`
				MimeType string `json:"mimetype"`
			} `json:"contents"`
		} `json:"modules"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing course contents: %w", err)
	}

	var sections []ResourceSection
	for _, s := range raw {
		sec := ResourceSection{Name: s.Name}
		for _, m := range s.Modules {
			mod := ResourceModule{ID: m.ID, Name: m.Name, ModName: m.ModName}
			for _, c := range m.Contents {
				mod.Contents = append(mod.Contents, FileContent{
					Type:     c.Type,
					Filename: c.Filename,
					FileURL:  c.FileURL,
					FileSize: c.FileSize,
					MimeType: c.MimeType,
				})
			}
			sec.Modules = append(sec.Modules, mod)
		}
		sections = append(sections, sec)
	}
	return sections, nil
}
