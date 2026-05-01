# PDF Page Rendering Fallback for Scanned PDFs

**Date:** 2026-05-01
**Status:** Approved (user pre-authorized autonomous execution)
**Working fork:** `gildeshiro/moodle-mcp-server`
**Branch:** `feat/streamable-http-transport`

## Goal

When `read_resource` fetches a PDF whose text extraction returns empty (image-only / scanned content), render the first N pages as PNG images and return them as `mcp.ImageContent` blocks. claude.ai's vision model reads scanned material visually — eliminating one of the last "this material won't open in claude.ai" failure modes for student study.

## Non-goals

- OCR (Tesseract or otherwise). Claude's vision is better than OCR for diagrams, handwriting, and mixed text+image content; redundant pipeline.
- Rendering EVERY PDF as images alongside text. Only fires when text extraction yields empty.
- Pure-Go rendering. The available libs (`unidoc`, `pdfium-go`) are either commercial-licensed or CGO-dependent. We use the mature `pdftoppm` external tool from poppler-utils.

## Why pdftoppm

- Battle-tested (poppler is the renderer behind Linux PDF viewers)
- Available as Alpine package (`poppler-utils`) — adds ~30 MB to runtime image
- No CGO, no Go dep
- Stable CLI: `pdftoppm -png -r <dpi> -f 1 -l N input.pdf prefix` writes `prefix-1.png`, `prefix-2.png`, ...

## Architecture

### New file: `internal/tools/render.go`

```go
func renderPDFAsPNGs(data []byte, maxPages, dpi int) ([][]byte, error)
```

Steps:
1. Look up `pdftoppm` in PATH. If missing, return a sentinel error `errPdftoppmMissing` so the caller can degrade gracefully.
2. Write `data` to a temp file (`os.CreateTemp`).
3. Exec `pdftoppm -png -r <dpi> -f 1 -l <maxPages> <tempfile> <prefix>` with a 60s timeout.
4. Read every `<prefix>-*.png` from the temp directory in numeric order.
5. Clean up temp files (defer).
6. Return slice of PNG byte slices.

Caps:
- `maxPages = 10` default — claude.ai handles up to ~20 images per message; 10 leaves headroom for description/text blocks.
- `dpi = 150` default — readable for most scanned text; balances size.
- Per-PNG size limit: 2 MB. If a rendered page exceeds, retry that page at `dpi/2`. If still over, drop and emit a warning text block.
- Total bytes limit: 15 MB across all returned PNGs. Drop trailing pages to fit.

### Modifications to `internal/tools/resources.go`

`ReadResourceOutput` gains:

```go
RenderedPNGs [][]byte // populated when ExtractedText was empty AND we successfully rendered the PDF
RenderNote   string   // human-readable note about the render (page count, fallback reason); empty if no render attempted
```

`HandleReadResource` after extraction:

```go
if extracted == "" && strings.HasPrefix(targetFile.MimeType, "application/pdf") {
    pngs, err := renderPDFAsPNGs(body, MaxRenderPages, RenderDPI)
    if err == nil && len(pngs) > 0 {
        // Trim to fit total size budget
        out.RenderedPNGs = trimPNGsToBudget(pngs, MaxRenderTotalBytes)
        out.RenderNote = fmt.Sprintf("PDF text extraction returned empty; rendered %d page(s) as PNG so the model can read them visually.", len(out.RenderedPNGs))
    } else if errors.Is(err, errPdftoppmMissing) {
        out.RenderNote = "PDF appears image-only and pdftoppm is not installed; cannot fall back to visual rendering."
    }
}
```

The blob-size guard (`MaxInlineBlobBytes`) only fires when both extraction AND rendering produced nothing useful.

### Constants

In `resources.go`:

```go
const (
    MaxRenderPages       = 10
    RenderDPI            = 150
    MaxRenderPNGBytes    = 2 * 1024 * 1024  // per page
    MaxRenderTotalBytes  = 15 * 1024 * 1024 // total across all pages
)
```

### Modifications to `cmd/moodle-mcp/main.go`

In the `read_resource` registration, after constructing the description and (text OR blob) content blocks, append `mcp.ImageContent` blocks for each rendered PNG:

```go
for _, png := range out.RenderedPNGs {
    content = append(content, mcp.ImageContent{
        Type:     mcp.ContentTypeImage,
        Data:     base64.StdEncoding.EncodeToString(png),
        MIMEType: "image/png",
    })
}
if out.RenderNote != "" {
    content = append(content, mcp.TextContent{Type: mcp.ContentTypeText, Text: out.RenderNote})
}
```

When `RenderedPNGs` is populated, the function does NOT include the binary blob (the rendered images replace the failed-binary fallback).

### Dockerfile change

Replace:

```dockerfile
RUN apk --no-cache add ca-certificates
```

with:

```dockerfile
RUN apk --no-cache add ca-certificates poppler-utils
```

Adds ~30 MB to runtime image. Available on the Railway free tier without trouble.

### Tool description update

`read_resource` description gains: "Image-only / scanned PDFs whose text extraction is empty are rendered to PNG (up to 10 pages, 150 DPI) so the model can read them visually."

## Error handling

| Case | Behavior |
|---|---|
| `pdftoppm` not in PATH | Emit `RenderNote` explaining; fall through to blob path |
| Temp file/exec failure | Log to stderr; same as missing tool — degrade gracefully |
| Render produces 0 pages | No render note added; fall through to blob |
| Rendered PNGs exceed budget | Drop trailing pages; note final count in `RenderNote` |
| pdftoppm timeout (60s) | Treat as render failure, fall through |

## Testing

The renderer relies on an external binary so we can't unit-test it without staging a real PDF + having pdftoppm available. Approach:

1. **Unit-test the trim logic** (`trimPNGsToBudget`) with synthetic byte slices.
2. **Manual smoke** against a real CECIERJ PDF: render 2 pages of any text PDF (the textbook), verify the PNGs are well-formed (start with `\x89PNG`).
3. **Forced-empty smoke**: temporarily flip the PDF extraction to return empty even for a text PDF, verify the render fallback fires and emits `ImageContent` blocks.

For step 3 we use a flag-only test driver — not a permanent code path.

## Deployment

Same as previous fixes:

1. Commit on `feat/streamable-http-transport`
2. Push to fork
3. Force-push to `main` → Railway redeploys (also rebuilds the Docker image with poppler-utils)
4. Manual smoke against production using a known scanned PDF, OR force-test a text PDF by passing tiny `MaxRenderPages` to confirm the wiring.
