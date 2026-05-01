# Office Docs Extraction + Smarter Size Handling for `read_resource`

**Date:** 2026-05-01
**Status:** Approved (user pre-authorized autonomous execution)
**Working fork:** `gildeshiro/moodle-mcp-server`
**Branch:** `feat/streamable-http-transport` (not in upstream PR)

## Goal

Eliminate two remaining gaps that break the "claude.ai web on phone is the only client" workflow for student study materials:

1. **Office documents** (`.docx`, `.pptx`, `.xlsx`) come back as `application/vnd.openxmlformats-...` blobs that claude.ai web rejects, so course slides/handouts/spreadsheets cannot be read at all in cloud-only sessions.
2. **Files larger than 10 MB** hard-error with `"file too large for inline"` even when the file is a 12 MB PDF whose text extraction would yield only 200 KB. The size guard fires before extraction, blocking textbooks and lecture decks unnecessarily.

After this change, any course material whose meaningful content is text — regardless of source format or raw byte size — is reachable via `read_resource` from claude.ai web.

## Non-goals

- OCR for image-only / scanned PDFs (separate deferred item: "#2 OCR")
- PowerPoint speaker notes extraction (text content of slides only — notes can come later if useful)
- Charts/embedded objects in xlsx (cell text only)
- Legacy binary Office formats `.doc`, `.ppt`, `.xls` — these are not OOXML and require different parsers; skipping for now
- File-upload submission tool (separate item: "#4")
- OAuth 2.1 DCR (separate item: "#7")

## Context

After the previous round of fixes (`feat(read_resource): folder support + PDF text extraction`), the `read_resource` tool produces `TextContent` for PDFs and `text/*` files (works in claude.ai web), and falls back to a base64 `BlobResourceContents` for other binaries (rejected by claude.ai web).

The current sizing logic:

```go
if targetFile.FileSize > MaxInlineFileBytes /* 10 MB */ {
    return nil, fmt.Errorf("file %q is %.1f MB, exceeds %d MB inline limit ...")
}
// fetch...
// extract text if possible...
```

This rejects 12 MB PDFs even though the text extraction would have produced a perfectly servable response. We need to gate AFTER extraction, not before.

## Architecture

### File layout

```
internal/tools/
├── resources.go        — list/read/download tool handlers (existing)
└── extract.go          — NEW: text extractors for PDF, docx, pptx, xlsx
```

`extract.go` exposes one function per format and a single dispatch helper:

```go
func extractPDFText(data []byte) (string, error)
func extractDocxText(data []byte) (string, error)
func extractPptxText(data []byte) (string, error)
func extractXlsxText(data []byte) (string, error)

// extractTextByMIME returns ("", nil) when the MIME type has no extractor;
// callers treat empty string as "extraction not applicable, fall back to blob".
func extractTextByMIME(mimeType string, data []byte) (string, error)
```

`extract.go` is the new home for the existing `extractPDFText` (currently in `resources.go`); the move is part of this change to keep `resources.go` focused on tool handlers.

### Office extractors — pure stdlib approach

OOXML files are zip archives containing XML. We use `archive/zip` + `encoding/xml` from the standard library — no new third-party dependencies. The extractors don't preserve formatting or structure beyond basic separators; they recover the readable text content.

**docx** (`word/document.xml`):

- Open the byte slice as a zip archive
- Find `word/document.xml`
- Stream-decode with `xml.NewDecoder`, collecting `xml.CharData` events that occur within `<w:t>` elements
- Insert a newline after each `<w:p>` (paragraph)

**pptx** (`ppt/slides/slide*.xml`):

- Open zip
- Iterate every entry matching `ppt/slides/slide*.xml`, sorted by slide number
- For each slide, collect `<a:t>` text runs; insert a newline between text frames, two newlines between slides
- Prepend each slide block with `--- Slide N ---` so the model can reason about position

**xlsx** (`xl/sharedStrings.xml` + `xl/worksheets/sheet*.xml`):

- Open zip
- Read shared strings table from `xl/sharedStrings.xml`
- Iterate `xl/worksheets/sheet*.xml`; for each `<c>` cell, resolve its value (either a shared-string index or inline value)
- Output as `Sheet N | A1=value | B1=value` lines (compact, grep-friendly)

### Size handling rework

Replace the single `MaxInlineFileBytes = 10 MB` constant with three:

| Constant | Value | Purpose |
|---|---|---|
| `MaxRawFetchBytes` | 50 MB | OOM guard during HTTP fetch. Refuse before reading more than this from the network. |
| `MaxInlineBlobBytes` | 10 MB | When returning a blob (no text extracted), cap at this. Same as old behavior. |
| `MaxExtractedTextBytes` | 512 KB | When returning extracted text, truncate at this with a clear marker. |

New `HandleReadResource` flow:

```
1. Resolve targetFile from course contents (existing logic).
2. If targetFile.FileSize > MaxRawFetchBytes → error "file too large to fetch
   (X MB > Y MB cap)". (Hard cap, no override.)
3. Fetch bytes via HTTP, capped by io.LimitReader at MaxRawFetchBytes+1.
4. Try extractTextByMIME(mime, body).
5. If extracted text non-empty:
     - If len(text) > MaxExtractedTextBytes:
         text = text[:MaxExtractedTextBytes] + "\n\n[truncated: showing first N chars
         of M total]"
     - Return ExtractedText set; Bytes also set (caller decides).
6. If no extracted text:
     - If len(body) > MaxInlineBlobBytes → error "binary file too large for inline
       (X MB > 10 MB) and no text extractor available; use download_resource in
       stdio mode".
     - Return Bytes only; ExtractedText empty.
```

The caller in `cmd/moodle-mcp/main.go` already prefers `ExtractedText` when present and only falls back to a blob when it is empty. No change there beyond a description tweak listing the supported formats.

### MIME type recognition

Moodle's `core_course_get_contents` returns `mimetype` per file. Mapping:

| MIME | Extractor |
|---|---|
| `application/pdf` | `extractPDFText` |
| `application/vnd.openxmlformats-officedocument.wordprocessingml.document` | `extractDocxText` |
| `application/vnd.openxmlformats-officedocument.presentationml.presentation` | `extractPptxText` |
| `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet` | `extractXlsxText` |
| `text/*` | direct `string(body)` |
| anything else | none (caller falls back to blob, subject to `MaxInlineBlobBytes`) |

Robustness: when Moodle's MIME is missing or wrong (it happens), `extractTextByMIME` also tries to **sniff by zip-archive structure**: if `bytes` starts with `PK\x03\x04` AND a known internal entry exists (`word/document.xml`, `ppt/slides/`, `xl/workbook.xml`), pick the matching extractor. Pure-PDF magic-byte sniff (`%PDF-`) similarly catches mis-typed PDFs.

### Truncation marker

When text exceeds `MaxExtractedTextBytes`, the response appends a clear marker the model can reason about:

```
[truncated: returned 524288 of 1247392 chars; ask the user for a smaller section
or use download_resource in stdio mode for the full file]
```

This is preferable to silent truncation: the model can decide to ask the user for guidance rather than hallucinate the missing tail.

## Error handling

- **Fetch errors** (network, 4xx/5xx from Moodle): unchanged — surface as tool errors
- **Parse failures in extractor**: log to stderr, return `("", nil)` — caller treats as "no extraction available" and falls back to blob (which will then fail size check if too large; an honest "this binary doesn't fit" beats a corrupted text response)
- **Zip with no recognizable office entry**: same as parse failure — return empty
- **Empty extracted text** (image-only PDF, slide deck with no text): treated as "no extraction"; falls back to blob path

## Testing

Existing tests don't cover extractors. We add a small in-package test using a synthesized minimal docx (a zip with one `word/document.xml`) and assert the extractor recovers the expected text. Same for pptx and xlsx. The PDF extractor already gets end-to-end coverage via the streamable_test.go path.

For confidence in real-world cases, manual end-to-end verification against the user's `pvs.cecierj.edu.br/ava` instance: pick a course with slides/docs (likely PL-ON or RED-ON), call `list_resources`, then `read_resource` against each format, confirm text comes back.

## Documentation

- Update `read_resource`'s tool description to list the supported formats explicitly (so the model picks it confidently): "PDFs, Office documents (.docx, .pptx, .xlsx), and text files are extracted to plain text..."
- No README/DEPLOYMENT_GUIDE changes — the user-visible behavior is "more files now work."

## Deployment

Same flow as previous fixes:

1. Commit on `feat/streamable-http-transport`
2. Push to `origin feat/streamable-http-transport`
3. Force-push to `origin main` (triggers Railway auto-redeploy from default branch)
4. Wait for redeploy, validate against a real CECIERJ docx/pptx

This branch is intentionally separate from the upstream PR (`pr/streamable-http-transport`); these enhancements can become a follow-up upstream PR after the first one merges.

## Open follow-ups (not in this PR)

- **OCR for scanned PDFs** (#2): blocked on choosing a Go-friendly OCR path. `gen2brain/go-fitz` (libmupdf) requires CGO. External `pdftoppm` + `tesseract` is bigger infra. Defer.
- **Per-page PDF rendering as ImageContent**: would let claude.ai see scanned material visually. Same dependency considerations.
- **File-upload assignment submission** (#4): new tool, separate scope.
- **OAuth 2.1 DCR** (#7): bigger architectural change.
