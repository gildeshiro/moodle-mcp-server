package tools

import (
	"context"
	"encoding/json"
	"errors"
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
	ModuleID  int    `json:"module_id"`
	FileIndex int    `json:"file_index"` // 0 for single-file modules; >0 for additional files in folder modules
	Name      string `json:"name"`
	Filename  string `json:"filename"`
	SizeMB    string `json:"size_mb"`
	MimeType  string `json:"mime_type"`
	Section   string `json:"section"`
	ModName   string `json:"modname,omitempty"` // "folder" if this row came from a folder module
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
			fileIdx := 0
			for _, f := range mod.Contents {
				if f.Type != "file" {
					continue
				}
				if input.MimeType != "" && !strings.Contains(f.MimeType, input.MimeType) {
					fileIdx++ // keep the index aligned with the original module.contents order
					continue
				}
				resources = append(resources, ResourceDisplay{
					ModuleID:  mod.ID,
					FileIndex: fileIdx,
					Name:      mod.Name,
					Filename:  f.Filename,
					SizeMB:    fmt.Sprintf("%.1f MB", float64(f.FileSize)/1024/1024),
					MimeType:  f.MimeType,
					Section:   sec.Name,
					ModName:   mod.ModName,
				})
				fileIdx++
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

// Size constants for read_resource. Three thresholds cover the three failure
// modes:
//   * MaxRawFetchBytes — refuse to even pull bytes off the network past this.
//     OOM guard.
//   * MaxInlineBlobBytes — when returning raw bytes as a base64
//     BlobResourceContents (no text extraction available), cap here so the
//     base64-encoded payload stays under ~13.3 MB on the wire.
//   * MaxExtractedTextBytes — extracted text response cap. Most clients
//     comfortably handle up to ~1 MB tool responses; we err on the side of
//     headroom and add a clear truncation marker so the model knows.
const (
	MaxRawFetchBytes      int64 = 50 * 1024 * 1024 // 50 MB
	MaxInlineBlobBytes    int64 = 10 * 1024 * 1024 // 10 MB
	MaxExtractedTextBytes int   = 512 * 1024       // 512 KB
)

type ReadResourceInput struct {
	CourseID  int `json:"course_id" jsonschema:"description=The Moodle course ID containing the resource"`
	ModuleID  int `json:"module_id" jsonschema:"description=The module ID of the resource to read (get from list_resources)"`
	FileIndex int `json:"file_index,omitempty" jsonschema:"description=Index into the module's file list (0-based; default 0). Folder modules contain multiple files — use the file_index from list_resources to pick a specific one."`
}

// ReadResourceOutput is the structured payload returned to the tool registration
// layer. The registration converts it into an mcp.CallToolResult: when
// ExtractedText is non-empty, it is the primary content (works in clients that
// don't render binary blobs, e.g. claude.ai web). The raw Bytes are also
// returned alongside as a base64 BlobResourceContents for clients that can
// render the original binary.
type ReadResourceOutput struct {
	Description   string // text shown to the model (filename, size, mime)
	URI           string // canonical resource URI
	Filename      string
	MimeType      string
	Size          int64
	Bytes         []byte // raw file bytes; caller is responsible for base64-encoding
	ExtractedText string // best-effort plain text (PDFs, text files); empty if unavailable

	// RenderedPNGs is populated for PDFs whose ExtractedText was empty
	// (typically image-only / scanned PDFs) when pdftoppm is available.
	// Each entry is a PNG byte slice for one rendered page; the registration
	// layer encodes them as mcp.ImageContent blocks so claude.ai's vision
	// model can read the visual content. Empty if no render attempted.
	RenderedPNGs [][]byte
	// RenderNote, when non-empty, is appended as a TextContent block by the
	// registration layer to explain to the model what just happened (number
	// of pages rendered, dropped pages, or why the fallback couldn't fire).
	RenderNote string
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

	// Locate the module and pick the file_index'th file inside it.
	var targetFile *FileContent
	var moduleName, modName string
	var totalFiles int
	for _, sec := range sections {
		for _, mod := range sec.Modules {
			if mod.ID != input.ModuleID {
				continue
			}
			moduleName = mod.Name
			modName = mod.ModName
			fileIdx := 0
			for _, f := range mod.Contents {
				if f.Type != "file" {
					continue
				}
				if fileIdx == input.FileIndex {
					fc := f
					targetFile = &fc
				}
				fileIdx++
			}
			totalFiles = fileIdx
		}
	}
	if targetFile == nil {
		if totalFiles == 0 {
			return nil, fmt.Errorf("no downloadable file found for module_id %d in course %d", input.ModuleID, input.CourseID)
		}
		return nil, fmt.Errorf("file_index %d out of range for module %d (it has %d file(s); valid indices 0..%d)",
			input.FileIndex, input.ModuleID, totalFiles, totalFiles-1)
	}
	if targetFile.FileSize > MaxRawFetchBytes {
		return nil, fmt.Errorf("file %q is %.1f MB, exceeds %d MB fetch limit; use download_resource (local stdio mode) instead",
			targetFile.Filename, float64(targetFile.FileSize)/1024/1024, MaxRawFetchBytes/(1024*1024))
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

	// Cap the read at MaxRawFetchBytes — defense against a server lying about
	// Content-Length.
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxRawFetchBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}
	if int64(len(body)) > MaxRawFetchBytes {
		return nil, fmt.Errorf("file body exceeded %d MB fetch limit during streaming", MaxRawFetchBytes/(1024*1024))
	}

	// Try text extraction first (PDF, Office docs, text/*). The dispatcher
	// also magic-byte-sniffs when MIME is wrong/missing.
	extracted, _ := extractTextByMIME(targetFile.MimeType, body)
	if len(extracted) > MaxExtractedTextBytes {
		extracted = extracted[:MaxExtractedTextBytes] + fmt.Sprintf(
			"\n\n[truncated: returned %d of %d chars; ask the user for a smaller section or use download_resource in stdio mode for the full file]",
			MaxExtractedTextBytes, len(extracted))
	}

	// Image-fallback: if this is a PDF and we got no text, try rendering pages
	// as PNGs so the model's vision can read them. Only fires for PDFs (other
	// extensionless / unknown binaries fall through to the blob path).
	var renderedPNGs [][]byte
	var renderNote string
	isPDF := strings.HasPrefix(targetFile.MimeType, "application/pdf") ||
		(targetFile.MimeType == "" && len(body) >= 5 && string(body[:5]) == "%PDF-")
	if extracted == "" && isPDF {
		pngs, err := renderPDFAsPNGs(body, MaxRenderPages, RenderDPI)
		switch {
		case err == nil && len(pngs) > 0:
			trimmed, dropped := trimPNGsToBudget(pngs, MaxRenderPNGBytes, MaxRenderTotalBytes)
			renderedPNGs = trimmed
			if dropped > 0 {
				renderNote = fmt.Sprintf("PDF text extraction returned empty; rendered %d of %d page(s) as PNG (%d dropped to fit response budget). The model can read the page images visually.",
					len(trimmed), len(pngs), dropped)
			} else {
				renderNote = fmt.Sprintf("PDF text extraction returned empty; rendered %d page(s) as PNG so the model can read them visually.",
					len(trimmed))
			}
		case errors.Is(err, errPdftoppmMissing):
			renderNote = "PDF appears image-only and pdftoppm is not installed in this environment; cannot fall back to visual rendering."
		case err != nil:
			renderNote = fmt.Sprintf("PDF appears image-only; visual fallback failed: %v.", err)
		}
	}

	// If no extraction AND no rendered fallback, the raw bytes will be returned
	// as a blob — gate THAT on the (smaller) MaxInlineBlobBytes.
	if extracted == "" && len(renderedPNGs) == 0 && int64(len(body)) > MaxInlineBlobBytes {
		hint := ""
		if renderNote != "" {
			hint = " " + renderNote
		}
		return nil, fmt.Errorf("binary file %q is %.1f MB and no text extractor or visual renderer succeeded; exceeds %d MB blob inline limit. Use download_resource (local stdio mode) for the full file.%s",
			targetFile.Filename, float64(len(body))/1024/1024, MaxInlineBlobBytes/(1024*1024), hint)
	}

	folderHint := ""
	if modName == "folder" {
		folderHint = fmt.Sprintf(" (file %d of %d in folder)", input.FileIndex+1, totalFiles)
	}
	desc := fmt.Sprintf("Module %q%s — file %q (%s, %.1f MB).",
		moduleName, folderHint, targetFile.Filename, targetFile.MimeType, float64(len(body))/1024/1024)

	return &ReadResourceOutput{
		Description:   desc,
		URI:           fmt.Sprintf("moodle://course/%d/module/%d/idx/%d/%s", input.CourseID, input.ModuleID, input.FileIndex, targetFile.Filename),
		Filename:      targetFile.Filename,
		MimeType:      targetFile.MimeType,
		Size:          int64(len(body)),
		Bytes:         body,
		ExtractedText: extracted,
		RenderedPNGs:  renderedPNGs,
		RenderNote:    renderNote,
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
