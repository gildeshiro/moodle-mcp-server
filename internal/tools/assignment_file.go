package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jawadh/moodle-mcp-server/internal/api"
)

// --- Submit Assignment File Tool ---

type SubmitAssignmentFileInput struct {
	AssignmentID  int    `json:"assignment_id" jsonschema:"description=The Moodle assignment ID to submit to"`
	Filename      string `json:"filename" jsonschema:"description=The filename to attach (e.g. essay.pdf)"`
	ContentBase64 string `json:"content_base64" jsonschema:"description=The file content encoded as standard base64 (data URI prefix is also accepted)"`
}

func HandleSubmitAssignmentFile(ctx context.Context, client *api.Client, input SubmitAssignmentFileInput) (string, error) {
	if !client.IsAuthenticated() {
		return "", api.ErrNotAuthenticated
	}
	if input.AssignmentID == 0 {
		return "", fmt.Errorf("assignment_id is required")
	}
	if input.Filename == "" {
		return "", fmt.Errorf("filename is required")
	}
	if input.ContentBase64 == "" {
		return "", fmt.Errorf("content_base64 is required")
	}

	// Tolerate a data: URI prefix ("data:<mime>;base64,<payload>").
	payload := input.ContentBase64
	if idx := strings.Index(payload, ";base64,"); idx >= 0 {
		payload = payload[idx+len(";base64,"):]
	}
	// Strip whitespace/newlines that some clients insert.
	payload = strings.NewReplacer("\n", "", "\r", "", " ", "", "\t", "").Replace(payload)

	content, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", fmt.Errorf("decoding base64 content: %w", err)
	}
	if len(content) == 0 {
		return "", fmt.Errorf("decoded file content is empty")
	}

	// Step 1: upload file to draft area (itemid=0 → Moodle allocates a fresh draft).
	itemID, err := client.UploadFile(ctx, content, input.Filename, 0)
	if err != nil {
		return "", fmt.Errorf("uploading file to Moodle draft area: %w", err)
	}

	// Step 2: save the submission referencing the draft itemid.
	params := map[string]string{
		"assignmentid":                          fmt.Sprintf("%d", input.AssignmentID),
		"plugindata[files_filemanager]":         fmt.Sprintf("%d", itemID),
		"plugindata[onlinetext_editor][text]":   "",
		"plugindata[onlinetext_editor][format]": "1",
	}

	saveResp, err := client.Call(ctx, "mod_assign_save_submission", params)
	if err != nil {
		return "", fmt.Errorf("saving submission: %w", err)
	}

	// Moodle returns either an empty body or a list of warnings on success.
	var warnings []map[string]any
	_ = json.Unmarshal(saveResp, &warnings)

	out, _ := json.MarshalIndent(map[string]any{
		"success":       true,
		"draft_itemid":  itemID,
		"assignment_id": input.AssignmentID,
		"filename":      input.Filename,
		"size_bytes":    len(content),
		"warnings":      warnings,
	}, "", "  ")
	return string(out), nil
}
