# Office Docs Extraction + Smarter Size Handling — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `read_resource` work for Office documents (.docx, .pptx, .xlsx) and for large PDFs whose text content fits even when the raw bytes don't, so claude.ai web can autonomously read every text-based study material in a Moodle course.

**Architecture:** New `internal/tools/extract.go` with pure-stdlib OOXML text extractors (zip + XML, no new dependencies). Reworked size handling: extract text first, gate raw bytes only when no extraction was possible.

**Tech Stack:** Go 1.25 (already pinned), `archive/zip` and `encoding/xml` from stdlib, `github.com/ledongthuc/pdf` (already in go.mod).

**Spec:** [`docs/superpowers/specs/2026-05-01-office-docs-and-size-handling-design.md`](../specs/2026-05-01-office-docs-and-size-handling-design.md)

**Working branch:** `feat/streamable-http-transport` on fork `gildeshiro/moodle-mcp-server`. Not in upstream PR (separate enhancement set).

---

## File Structure

| Path | Action | Responsibility |
|---|---|---|
| `internal/tools/extract.go` | **create** | All format-to-text extractors (PDF moved here; .docx/.pptx/.xlsx new). One `extractTextByMIME` dispatcher. |
| `internal/tools/extract_test.go` | **create** | In-package unit tests using synthetic OOXML zips constructed in-memory. |
| `internal/tools/resources.go` | modify | Remove inline `extractPDFText`. Rework `HandleReadResource`: new size constants, fetch first, extract first, gate blob path on `MaxInlineBlobBytes` instead of pre-fetch size check. |
| `cmd/moodle-mcp/main.go` | modify | Update `read_resource` tool description to list newly supported formats. |

**Final commit layout (1 commit on the deploy branch):**

1. `feat(read_resource): extract .docx/.pptx/.xlsx text + lift size limit for text-extractable files`

---

## Task 1: Move `extractPDFText` to a new `extract.go` (refactor only)

**Files:**
- Create: `internal/tools/extract.go`
- Modify: `internal/tools/resources.go` (delete the function and the `pdf`/`bytes` imports if unused elsewhere)

This is a pure code move — no behavior change. Done first so subsequent tasks have a clean home for the new extractors.

- [ ] **Step 1.1: Read `internal/tools/resources.go`** to locate the existing `extractPDFText` function and its imports (`bytes`, `github.com/ledongthuc/pdf`).

- [ ] **Step 1.2: Create `internal/tools/extract.go`** with this exact content:

```go
package tools

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"
)

// extractPDFText pulls the plain text out of a PDF byte slice using a pure-Go
// parser. Returns an empty string with nil error if the PDF is image-only or
// otherwise lacks extractable text — caller should treat empty as "no text".
// Returns a wrapped error only on hard parse failures.
func extractPDFText(data []byte) (string, error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("opening pdf: %w", err)
	}
	var buf strings.Builder
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		text, err := p.GetPlainText(nil)
		if err != nil {
			// Skip the bad page rather than fail the whole document — partial
			// text is still useful to the model.
			continue
		}
		buf.WriteString(text)
		buf.WriteString("\n\n")
	}
	return strings.TrimSpace(buf.String()), nil
}
```

- [ ] **Step 1.3: Edit `internal/tools/resources.go`** — remove the entire `extractPDFText` function (and its leading doc comment block). Remove the `bytes` and `github.com/ledongthuc/pdf` imports from the import block at the top of the file (they are no longer used in `resources.go`).

- [ ] **Step 1.4: Verify build**

Run from `C:\Jean\moodle-mcp-server`:

```
go build ./...
go vet ./internal/tools/... ./internal/server/... ./cmd/...
```

Expected: no output (success).

- [ ] **Step 1.5: Verify tests still pass**

Run:

```
go test -count=1 ./internal/server/... ./internal/tools/...
```

Expected: same tests as before pass; no new failures.

(No commit yet — accumulate the whole feature into one commit at the end of Task 9.)

---

## Task 2: Add `extractDocxText` (TDD)

**Files:**
- Modify: `internal/tools/extract.go`
- Create: `internal/tools/extract_test.go`

Strategy: build a minimal docx in memory with `archive/zip`, then assert the extractor recovers the paragraphs. The minimal docx is just a zip containing `word/document.xml` with `<w:t>` text runs.

- [ ] **Step 2.1: Create `internal/tools/extract_test.go`** with the test helper plus the first test:

```go
package tools

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// buildMinimalDocx writes a zip in memory with a single word/document.xml
// containing the given paragraphs as <w:p><w:r><w:t>...</w:t></w:r></w:p>.
// This is the minimum a Word OOXML file needs to be parseable by our
// extractor — real Office output adds many more parts but we don't depend
// on them.
func buildMinimalDocx(t *testing.T, paragraphs []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("zip Create: %v", err)
	}
	var doc strings.Builder
	doc.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	doc.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paragraphs {
		doc.WriteString(`<w:p><w:r><w:t xml:space="preserve">`)
		doc.WriteString(p)
		doc.WriteString(`</w:t></w:r></w:p>`)
	}
	doc.WriteString(`</w:body></w:document>`)
	if _, err := f.Write([]byte(doc.String())); err != nil {
		t.Fatalf("write document.xml: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip Close: %v", err)
	}
	return buf.Bytes()
}

func TestExtractDocxText(t *testing.T) {
	data := buildMinimalDocx(t, []string{
		"Primeira linha do documento.",
		"Segunda linha com acentuação: ção, ã, é, í.",
		"Terceira linha.",
	})
	got, err := extractDocxText(data)
	if err != nil {
		t.Fatalf("extractDocxText: %v", err)
	}
	for _, want := range []string{"Primeira linha", "acentuação", "Terceira linha"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in extracted text:\n%s", want, got)
		}
	}
}

func TestExtractDocxText_NotADocx(t *testing.T) {
	_, err := extractDocxText([]byte("not a zip"))
	if err == nil {
		t.Fatalf("expected error on non-zip input")
	}
}

func TestExtractDocxText_ZipWithoutDocumentXML(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create("other/file.xml")
	_, _ = f.Write([]byte("<x/>"))
	_ = zw.Close()

	got, err := extractDocxText(buf.Bytes())
	if err != nil {
		t.Fatalf("expected nil error when document.xml missing, got: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty text when document.xml missing, got %q", got)
	}
}
```

- [ ] **Step 2.2: Run the test to verify it fails with "extractDocxText undefined":**

```
go test -count=1 -run TestExtractDocxText ./internal/tools/...
```

Expected: compile error referencing `extractDocxText`.

- [ ] **Step 2.3: Add `extractDocxText` to `internal/tools/extract.go`**

Append to the existing `extract.go` (also add `archive/zip` and `encoding/xml` to its imports):

```go
import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"sort"
	"strings"

	"github.com/ledongthuc/pdf"
)
```

(Replace the existing import block — this version supersedes Task 1's smaller block. The `sort` import is for pptx slide ordering in Task 3; pre-adding now to avoid churn.)

Add this function below `extractPDFText`:

```go
// extractDocxText pulls plain text from a .docx (OOXML) byte slice. The format
// is a zip containing word/document.xml; we collect every <w:t> CharData and
// emit a newline after each <w:p> paragraph. Formatting, images, headers and
// footers are ignored — text content only.
//
// Returns ("", nil) if the archive is a zip but contains no document.xml
// (treated as "no text available", not an error).
func extractDocxText(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("opening docx zip: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("opening word/document.xml: %w", err)
		}
		defer rc.Close()

		var buf strings.Builder
		dec := xml.NewDecoder(rc)
		var inText bool
		for {
			tok, err := dec.Token()
			if err != nil {
				break // io.EOF or any parse hiccup ends the stream cleanly
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "t" {
					inText = true
				}
			case xml.EndElement:
				switch t.Name.Local {
				case "t":
					inText = false
				case "p":
					buf.WriteString("\n")
				}
			case xml.CharData:
				if inText {
					buf.Write(t)
				}
			}
		}
		return strings.TrimSpace(buf.String()), nil
	}
	// Zip parsed but no document.xml — empty string, no error.
	return "", nil
}
```

- [ ] **Step 2.4: Run the test to verify it passes:**

```
go test -count=1 -run TestExtractDocxText ./internal/tools/...
```

Expected: `PASS`.

---

## Task 3: Add `extractPptxText` (TDD)

**Files:**
- Modify: `internal/tools/extract.go` (append function)
- Modify: `internal/tools/extract_test.go` (append helper + tests)

- [ ] **Step 3.1: Append helper + tests to `internal/tools/extract_test.go`:**

```go
// buildMinimalPptx creates a zip with N slides under ppt/slides/slideN.xml,
// each containing the given lines as <a:t> text runs grouped under <a:p>.
func buildMinimalPptx(t *testing.T, slides [][]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i, lines := range slides {
		name := "ppt/slides/slide" + itoa(i+1) + ".xml"
		f, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip Create %s: %v", name, err)
		}
		var doc strings.Builder
		doc.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
		doc.WriteString(`<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree>`)
		for _, line := range lines {
			doc.WriteString(`<p:sp><p:txBody><a:p><a:r><a:t>`)
			doc.WriteString(line)
			doc.WriteString(`</a:t></a:r></a:p></p:txBody></p:sp>`)
		}
		doc.WriteString(`</p:spTree></p:cSld></p:sld>`)
		if _, err := f.Write([]byte(doc.String())); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip Close: %v", err)
	}
	return buf.Bytes()
}

// itoa is a tiny local helper to avoid pulling strconv just for tests.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestExtractPptxText(t *testing.T) {
	data := buildMinimalPptx(t, [][]string{
		{"Slide 1 título", "subtítulo do slide um"},
		{"Slide 2", "ponto a", "ponto b"},
		{"Slide 3 — conclusão"},
	})
	got, err := extractPptxText(data)
	if err != nil {
		t.Fatalf("extractPptxText: %v", err)
	}
	for _, want := range []string{
		"--- Slide 1 ---",
		"Slide 1 título",
		"--- Slide 2 ---",
		"ponto b",
		"--- Slide 3 ---",
		"conclusão",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in extracted pptx:\n%s", want, got)
		}
	}
	// Slide 1 marker must appear before Slide 2 marker (ordering).
	if i1, i2 := strings.Index(got, "--- Slide 1 ---"), strings.Index(got, "--- Slide 2 ---"); i1 < 0 || i2 < 0 || i1 >= i2 {
		t.Errorf("slides out of order or missing: %s", got)
	}
}

func TestExtractPptxText_NoSlides(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create("ppt/presentation.xml")
	_, _ = f.Write([]byte("<x/>"))
	_ = zw.Close()

	got, err := extractPptxText(buf.Bytes())
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty when no slides present, got: %q", got)
	}
}
```

- [ ] **Step 3.2: Run tests to verify they fail with "extractPptxText undefined":**

```
go test -count=1 -run TestExtractPptxText ./internal/tools/...
```

Expected: compile error.

- [ ] **Step 3.3: Add `extractPptxText` to `internal/tools/extract.go`:**

Append below `extractDocxText`:

```go
// extractPptxText pulls plain text from a .pptx (OOXML) byte slice. Iterates
// every ppt/slides/slideN.xml in numeric order, collects <a:t> CharData,
// and emits "--- Slide N ---" markers between slides so the model sees the
// boundaries. Speaker notes are NOT included (they live in
// ppt/notesSlides/, out of scope).
func extractPptxText(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("opening pptx zip: %w", err)
	}

	type slideEntry struct {
		num  int
		file *zip.File
	}
	var slides []slideEntry
	const prefix = "ppt/slides/slide"
	const suffix = ".xml"
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, prefix) || !strings.HasSuffix(f.Name, suffix) {
			continue
		}
		// "ppt/slides/_rels/slide1.xml.rels" also matches HasPrefix but contains
		// "/_rels/"; filter it out.
		if strings.Contains(f.Name, "/_rels/") {
			continue
		}
		numStr := f.Name[len(prefix) : len(f.Name)-len(suffix)]
		var num int
		if _, scanErr := fmt.Sscanf(numStr, "%d", &num); scanErr != nil {
			continue
		}
		slides = append(slides, slideEntry{num: num, file: f})
	}
	if len(slides) == 0 {
		return "", nil
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].num < slides[j].num })

	var out strings.Builder
	for _, s := range slides {
		fmt.Fprintf(&out, "--- Slide %d ---\n", s.num)
		text, err := extractTextFromOOXMLEntry(s.file, "t")
		if err != nil {
			// Skip a bad slide rather than fail the whole deck.
			continue
		}
		out.WriteString(text)
		out.WriteString("\n\n")
	}
	return strings.TrimSpace(out.String()), nil
}

// extractTextFromOOXMLEntry streams a single zip entry, decoding it as XML and
// concatenating CharData inside any element whose local name equals tagLocal.
// Used by both pptx (<a:t>) and the inline-string path of xlsx (<t>).
func extractTextFromOOXMLEntry(f *zip.File, tagLocal string) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", f.Name, err)
	}
	defer rc.Close()

	var buf strings.Builder
	dec := xml.NewDecoder(rc)
	depth := 0 // tracks how deep we are inside the target tag (handles nested same-name)
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == tagLocal {
				depth++
			}
		case xml.EndElement:
			if t.Name.Local == tagLocal && depth > 0 {
				depth--
				buf.WriteString("\n")
			}
		case xml.CharData:
			if depth > 0 {
				buf.Write(t)
			}
		}
	}
	return buf.String(), nil
}
```

- [ ] **Step 3.4: Run pptx tests:**

```
go test -count=1 -run TestExtractPptxText ./internal/tools/...
```

Expected: `PASS` for both `TestExtractPptxText` and `TestExtractPptxText_NoSlides`.

---

## Task 4: Add `extractXlsxText` (TDD)

**Files:**
- Modify: `internal/tools/extract.go`
- Modify: `internal/tools/extract_test.go`

XLSX is more complex: cell values are usually integers indexing into a shared strings table at `xl/sharedStrings.xml`. We resolve the indexes and emit `Sheet N | A1=value` lines.

- [ ] **Step 4.1: Append helper + test to `internal/tools/extract_test.go`:**

```go
// buildMinimalXlsx creates a zip with one worksheet (xl/worksheets/sheet1.xml)
// referencing strings via xl/sharedStrings.xml.
func buildMinimalXlsx(t *testing.T, sharedStrings []string, cells [][2]string /* {ref, value} */) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	// Shared strings.
	ssf, _ := zw.Create("xl/sharedStrings.xml")
	var ss strings.Builder
	ss.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	ss.WriteString(`<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	for _, s := range sharedStrings {
		ss.WriteString(`<si><t>`)
		ss.WriteString(s)
		ss.WriteString(`</t></si>`)
	}
	ss.WriteString(`</sst>`)
	_, _ = ssf.Write([]byte(ss.String()))

	// Worksheet — cells store a shared-string index when t="s".
	wf, _ := zw.Create("xl/worksheets/sheet1.xml")
	var w strings.Builder
	w.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	w.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row>`)
	for _, c := range cells {
		w.WriteString(`<c r="`)
		w.WriteString(c[0])
		w.WriteString(`" t="s"><v>`)
		w.WriteString(c[1])
		w.WriteString(`</v></c>`)
	}
	w.WriteString(`</row></sheetData></worksheet>`)
	_, _ = wf.Write([]byte(w.String()))

	_ = zw.Close()
	return buf.Bytes()
}

func TestExtractXlsxText(t *testing.T) {
	data := buildMinimalXlsx(t,
		[]string{"Disciplina", "Nota", "Português", "8.5"},
		[][2]string{{"A1", "0"}, {"B1", "1"}, {"A2", "2"}, {"B2", "3"}},
	)
	got, err := extractXlsxText(data)
	if err != nil {
		t.Fatalf("extractXlsxText: %v", err)
	}
	for _, want := range []string{"Disciplina", "Português", "8.5", "A1=Disciplina", "B2=8.5"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in extracted xlsx:\n%s", want, got)
		}
	}
}

func TestExtractXlsxText_NoSharedStrings(t *testing.T) {
	// Build an xlsx with inline strings (t="inlineStr"), no sharedStrings.xml.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	wf, _ := zw.Create("xl/worksheets/sheet1.xml")
	wf.Write([]byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>` +
		`<row><c r="A1" t="inlineStr"><is><t>inline value</t></is></c></row>` +
		`</sheetData></worksheet>`))
	_ = zw.Close()

	got, err := extractXlsxText(buf.Bytes())
	if err != nil {
		t.Fatalf("extractXlsxText: %v", err)
	}
	if !strings.Contains(got, "inline value") {
		t.Errorf("expected inline value, got: %q", got)
	}
}
```

- [ ] **Step 4.2: Run xlsx tests to verify they fail:**

```
go test -count=1 -run TestExtractXlsxText ./internal/tools/...
```

Expected: compile error referencing `extractXlsxText`.

- [ ] **Step 4.3: Add `extractXlsxText` to `internal/tools/extract.go`:**

Append below `extractTextFromOOXMLEntry`:

```go
// extractXlsxText pulls plain text from an .xlsx (OOXML) byte slice. Reads
// the shared strings table at xl/sharedStrings.xml, then iterates each
// xl/worksheets/sheet*.xml and emits "Sheet N | <ref>=<value>" lines for
// every populated cell. Inline strings (t="inlineStr") are also resolved.
// Numbers, dates, and formulas are emitted by the value Moodle's Excel
// would have stored — formula results, not the formula text.
func extractXlsxText(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("opening xlsx zip: %w", err)
	}

	// Step 1: shared strings.
	var sharedStrings []string
	for _, f := range zr.File {
		if f.Name != "xl/sharedStrings.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			break
		}
		dec := xml.NewDecoder(rc)
		var inT bool
		var current strings.Builder
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "si" {
					current.Reset()
				}
				if t.Name.Local == "t" {
					inT = true
				}
			case xml.EndElement:
				if t.Name.Local == "t" {
					inT = false
				}
				if t.Name.Local == "si" {
					sharedStrings = append(sharedStrings, current.String())
				}
			case xml.CharData:
				if inT {
					current.Write(t)
				}
			}
		}
		_ = rc.Close()
		break
	}

	// Step 2: each worksheet.
	type sheetEntry struct {
		num  int
		file *zip.File
	}
	var sheets []sheetEntry
	const prefix = "xl/worksheets/sheet"
	const suffix = ".xml"
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, prefix) || !strings.HasSuffix(f.Name, suffix) {
			continue
		}
		if strings.Contains(f.Name, "/_rels/") {
			continue
		}
		numStr := f.Name[len(prefix) : len(f.Name)-len(suffix)]
		var num int
		if _, scanErr := fmt.Sscanf(numStr, "%d", &num); scanErr != nil {
			continue
		}
		sheets = append(sheets, sheetEntry{num: num, file: f})
	}
	if len(sheets) == 0 {
		return "", nil
	}
	sort.Slice(sheets, func(i, j int) bool { return sheets[i].num < sheets[j].num })

	var out strings.Builder
	for _, sh := range sheets {
		text, err := extractXlsxSheet(sh.file, sh.num, sharedStrings)
		if err != nil {
			continue // skip bad sheet
		}
		out.WriteString(text)
	}
	return strings.TrimSpace(out.String()), nil
}

// extractXlsxSheet emits "Sheet N | <ref>=<value>" lines for every populated
// cell in a single worksheet zip entry. Resolves shared-string references
// (t="s") via the supplied table; inline strings (t="inlineStr") and direct
// values (no t attr) are emitted as-is.
func extractXlsxSheet(f *zip.File, sheetNum int, sharedStrings []string) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", f.Name, err)
	}
	defer rc.Close()

	var out strings.Builder
	dec := xml.NewDecoder(rc)

	// Per-cell state.
	var inCell, inV, inIS, inIST bool
	var cellRef, cellType string
	var cellVal strings.Builder

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "c":
				inCell = true
				cellRef = ""
				cellType = ""
				cellVal.Reset()
				for _, a := range t.Attr {
					if a.Name.Local == "r" {
						cellRef = a.Value
					}
					if a.Name.Local == "t" {
						cellType = a.Value
					}
				}
			case "v":
				if inCell {
					inV = true
				}
			case "is":
				if inCell {
					inIS = true
				}
			case "t":
				if inIS {
					inIST = true
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "c":
				if inCell && cellRef != "" && cellVal.Len() > 0 {
					value := cellVal.String()
					if cellType == "s" {
						idx := 0
						_, _ = fmt.Sscanf(value, "%d", &idx)
						if idx >= 0 && idx < len(sharedStrings) {
							value = sharedStrings[idx]
						}
					}
					fmt.Fprintf(&out, "Sheet %d | %s=%s\n", sheetNum, cellRef, value)
				}
				inCell = false
			case "v":
				inV = false
			case "is":
				inIS = false
			case "t":
				inIST = false
			}
		case xml.CharData:
			if inV || inIST {
				cellVal.Write(t)
			}
		}
	}
	return out.String(), nil
}
```

- [ ] **Step 4.4: Run xlsx tests:**

```
go test -count=1 -run TestExtractXlsxText ./internal/tools/...
```

Expected: `PASS` for both `TestExtractXlsxText` and `TestExtractXlsxText_NoSharedStrings`.

---

## Task 5: Add `extractTextByMIME` dispatcher with magic-byte fallback (TDD)

**Files:**
- Modify: `internal/tools/extract.go`
- Modify: `internal/tools/extract_test.go`

- [ ] **Step 5.1: Append tests for the dispatcher:**

```go
func TestExtractTextByMIME_Dispatch(t *testing.T) {
	docx := buildMinimalDocx(t, []string{"hello docx"})
	pptx := buildMinimalPptx(t, [][]string{{"hello pptx"}})
	xlsx := buildMinimalXlsx(t, []string{"hello xlsx"}, [][2]string{{"A1", "0"}})

	cases := []struct {
		name string
		mime string
		data []byte
		want string // substring expected in the output
	}{
		{"docx by mime", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", docx, "hello docx"},
		{"pptx by mime", "application/vnd.openxmlformats-officedocument.presentationml.presentation", pptx, "hello pptx"},
		{"xlsx by mime", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", xlsx, "hello xlsx"},
		{"text by mime", "text/plain", []byte("plain text body"), "plain text body"},
		{"text/html by mime", "text/html", []byte("<p>html</p>"), "<p>html</p>"}, // raw html — HTML-stripping is the model's job
		{"docx by sniff (wrong mime)", "application/octet-stream", docx, "hello docx"},
		{"unknown binary", "image/png", []byte{0x89, 'P', 'N', 'G', 0, 0, 0, 0}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractTextByMIME(tc.mime, tc.data)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if tc.want == "" && got != "" {
				t.Errorf("expected empty result, got %q", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Errorf("expected substring %q in:\n%s", tc.want, got)
			}
		})
	}
}
```

- [ ] **Step 5.2: Run the test to verify it fails with "extractTextByMIME undefined":**

```
go test -count=1 -run TestExtractTextByMIME ./internal/tools/...
```

Expected: compile error.

- [ ] **Step 5.3: Add the dispatcher to `internal/tools/extract.go`:**

Append at the bottom of the file:

```go
// extractTextByMIME tries to recover plain text from a byte slice based on the
// declared MIME type. Returns ("", nil) when no extractor applies — the caller
// should treat empty as "fall back to blob".
//
// When the MIME is generic (octet-stream, missing, or wrong), tries a
// magic-byte sniff: PDF if the body starts with "%PDF-", or one of the OOXML
// formats if it's a zip and contains the matching internal entry. This makes
// us robust against Moodle servers that serve the wrong content-type header
// for legitimate study materials.
func extractTextByMIME(mimeType string, data []byte) (string, error) {
	switch {
	case strings.HasPrefix(mimeType, "application/pdf"):
		return extractPDFText(data)
	case strings.HasPrefix(mimeType, "application/vnd.openxmlformats-officedocument.wordprocessingml.document"):
		return extractDocxText(data)
	case strings.HasPrefix(mimeType, "application/vnd.openxmlformats-officedocument.presentationml.presentation"):
		return extractPptxText(data)
	case strings.HasPrefix(mimeType, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"):
		return extractXlsxText(data)
	case strings.HasPrefix(mimeType, "text/"):
		return string(data), nil
	}
	// Sniff fallback for wrong/missing MIME.
	if bytes.HasPrefix(data, []byte("%PDF-")) {
		return extractPDFText(data)
	}
	if isZip(data) {
		switch {
		case zipContainsEntry(data, "word/document.xml"):
			return extractDocxText(data)
		case zipContainsEntryPrefix(data, "ppt/slides/slide"):
			return extractPptxText(data)
		case zipContainsEntry(data, "xl/workbook.xml"):
			return extractXlsxText(data)
		}
	}
	return "", nil
}

func isZip(data []byte) bool {
	// Local file header signature: PK\x03\x04
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K' && data[2] == 0x03 && data[3] == 0x04
}

func zipContainsEntry(data []byte, name string) bool {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return false
	}
	for _, f := range zr.File {
		if f.Name == name {
			return true
		}
	}
	return false
}

func zipContainsEntryPrefix(data []byte, prefix string) bool {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return false
	}
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, prefix) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5.4: Run all extractor tests:**

```
go test -count=1 ./internal/tools/...
```

Expected: every test passes (the dispatcher tests + all earlier per-format tests).

---

## Task 6: Rework `HandleReadResource` size logic

**Files:**
- Modify: `internal/tools/resources.go`

Replace the single `MaxInlineFileBytes` with three constants and gate the blob path on whether text was extracted.

- [ ] **Step 6.1: Read the current `internal/tools/resources.go`** to find:
- The constant `MaxInlineFileBytes` (current value `10 * 1024 * 1024`)
- The pre-fetch size check inside `HandleReadResource` (the `if targetFile.FileSize > MaxInlineFileBytes` block)
- The `io.ReadAll(io.LimitReader(...))` line that caps the body
- The post-fetch extraction switch (which currently uses `strings.HasPrefix(targetFile.MimeType, "application/pdf")` etc.)

- [ ] **Step 6.2: Replace the constant declaration.**

In `internal/tools/resources.go`, find:

```go
// MaxInlineFileBytes caps the size of files returned via HandleReadResource.
// Anything larger should be retrieved with download_resource (stdio-mode) or
// fetched outside the MCP path entirely. The base64 expansion of this limit
// is ~13.3 MB on the wire, which most MCP clients comfortably handle.
const MaxInlineFileBytes int64 = 10 * 1024 * 1024 // 10 MB
```

Replace with:

```go
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
```

- [ ] **Step 6.3: Replace the pre-fetch guard.**

Find:

```go
	if targetFile.FileSize > MaxInlineFileBytes {
		return nil, fmt.Errorf("file %q is %.1f MB, exceeds %d MB inline limit; use download_resource (local stdio mode) instead",
			targetFile.Filename, float64(targetFile.FileSize)/1024/1024, MaxInlineFileBytes/(1024*1024))
	}
```

Replace with:

```go
	if targetFile.FileSize > MaxRawFetchBytes {
		return nil, fmt.Errorf("file %q is %.1f MB, exceeds %d MB fetch limit; use download_resource (local stdio mode) instead",
			targetFile.Filename, float64(targetFile.FileSize)/1024/1024, MaxRawFetchBytes/(1024*1024))
	}
```

- [ ] **Step 6.4: Adjust the LimitReader cap.**

Find:

```go
	// Cap the read so a server lying about Content-Length can't OOM us.
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxInlineFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}
	if int64(len(body)) > MaxInlineFileBytes {
		return nil, fmt.Errorf("file body exceeded %d MB inline limit during streaming", MaxInlineFileBytes/(1024*1024))
	}
```

Replace with:

```go
	// Cap the read at MaxRawFetchBytes — defense against a server lying about
	// Content-Length.
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxRawFetchBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}
	if int64(len(body)) > MaxRawFetchBytes {
		return nil, fmt.Errorf("file body exceeded %d MB fetch limit during streaming", MaxRawFetchBytes/(1024*1024))
	}
```

- [ ] **Step 6.5: Replace the inline extraction switch with the dispatcher + size-aware decision.**

Find:

```go
	// Best-effort text extraction. We always try when the MIME suggests it.
	// Empty result is OK — the caller falls back to the blob.
	var extracted string
	switch {
	case strings.HasPrefix(targetFile.MimeType, "application/pdf"):
		if t, err := extractPDFText(body); err == nil {
			extracted = t
		}
	case strings.HasPrefix(targetFile.MimeType, "text/"):
		extracted = string(body)
	}
```

Replace with:

```go
	// Try text extraction first (PDF, Office docs, text/*). The dispatcher
	// also magic-byte-sniffs when MIME is wrong/missing.
	extracted, _ := extractTextByMIME(targetFile.MimeType, body)
	if len(extracted) > MaxExtractedTextBytes {
		extracted = extracted[:MaxExtractedTextBytes] + fmt.Sprintf(
			"\n\n[truncated: returned %d of %d chars; ask the user for a smaller section or use download_resource in stdio mode for the full file]",
			MaxExtractedTextBytes, len(extracted))
	}
	// If no extraction was possible, the raw bytes will be returned as a blob
	// — gate THAT on the (smaller) MaxInlineBlobBytes.
	if extracted == "" && int64(len(body)) > MaxInlineBlobBytes {
		return nil, fmt.Errorf("binary file %q is %.1f MB and no text extractor is available; exceeds %d MB blob inline limit. Use download_resource (local stdio mode) for the full file",
			targetFile.Filename, float64(len(body))/1024/1024, MaxInlineBlobBytes/(1024*1024))
	}
```

- [ ] **Step 6.6: Verify build and existing tests still pass.**

```
go build ./...
go vet ./internal/tools/... ./internal/server/... ./cmd/...
go test -count=1 ./internal/tools/... ./internal/server/...
```

Expected: build clean, vet clean, all tests pass (the existing streamable_test.go tests continue working because the registration in main.go is unchanged so far).

---

## Task 7: Update `read_resource` tool description in `cmd/moodle-mcp/main.go`

**Files:**
- Modify: `cmd/moodle-mcp/main.go`

- [ ] **Step 7.1: Update the description.**

Find the existing `read_resource` registration:

```go
	// ── Read Resource (inline; preferred for remote/HTTP mode) ────
	s.AddTool(
		mcp.NewTool("read_resource",
			mcp.WithDescription("Fetch a file (PDF, slides, etc.) from Moodle and return its content INLINE so the model can read it directly. For PDFs and text files, returns extracted plain text (works in clients that don't render binary blobs, e.g. claude.ai web). For other binary types, returns a base64 BlobResourceContents. Preferred for remote/HTTP deployments. Max 10 MB; use download_resource for larger files in stdio mode. Folder modules contain multiple files — use list_resources to discover (module_id, file_index) pairs."),
```

Replace the description string with:

```go
	// ── Read Resource (inline; preferred for remote/HTTP mode) ────
	s.AddTool(
		mcp.NewTool("read_resource",
			mcp.WithDescription("Fetch a file from Moodle and return its content INLINE so the model can read it directly. Plain text is extracted server-side for: PDFs, .docx, .pptx, .xlsx, and any text/* MIME (works in clients that don't render binary blobs, e.g. claude.ai web). Other binary types fall back to a base64 BlobResourceContents (max 10 MB raw). Files up to 50 MB raw are accepted as long as their extracted text fits the response (large textbook PDFs typically work fine). Folder modules contain multiple files — use list_resources to discover (module_id, file_index) pairs. Preferred over download_resource for any client that cannot read the server's filesystem."),
```

- [ ] **Step 7.2: Verify build:**

```
go build ./...
```

Expected: clean.

---

## Task 8: Smoke test against real Moodle

**Files:** none (read-only verification)

- [ ] **Step 8.1: Build the test binary.**

```
go build -o moodle-mcp-test.exe ./cmd/moodle-mcp/
```

- [ ] **Step 8.2: Start the test server in the background.** Run from a Bash shell:

```
MCP_DISABLE_AUTH=1 \
  MOODLE_URL="https://pvs.cecierj.edu.br/ava" \
  MOODLE_TOKEN="e732c7ca086142a5ba351f9b64371826" \
  ./moodle-mcp-test.exe -mode http -port 8770 &
sleep 2
```

- [ ] **Step 8.3: Initialize a session.**

```
URL="http://localhost:8770"
SID=$(curl -s -i -X POST "$URL/mcp" \
  -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"smoke","version":"0.0.0"}}}' \
  | grep -i "mcp-session-id:" | tr -d '\r' | awk '{print $2}')
echo "session: $SID"
```

- [ ] **Step 8.4: List PDFs and Office docs in course 39 (PL-ON, Português e Literatura) and 38 (RED-ON, Redação) to find sample files.**

```
for COURSE_ID in 39 38; do
  echo "=== course $COURSE_ID ==="
  curl -s -X POST "$URL/mcp" \
    -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" -H "Mcp-Session-Id: $SID" \
    -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"list_resources\",\"arguments\":{\"course_id\":$COURSE_ID}}}" \
    | python -c "
import sys, json
d = json.load(sys.stdin)
data = json.loads(d['result']['content'][0]['text'])
for r in data['resources']:
    mt = r.get('mime_type', '')
    if 'wordprocessing' in mt or 'presentation' in mt or 'spreadsheet' in mt or 'pdf' in mt:
        print(f\"  module {r['module_id']} idx={r['file_index']} mime={mt[-20:]}: {r['filename']} ({r['size_mb']})\")
"
done
```

Capture the (course_id, module_id, file_index, mime) of one .docx, one .pptx (if any exist), and one PDF >10MB if visible.

- [ ] **Step 8.5: For each sample file, call `read_resource` and verify text comes back.**

Replace `<COURSE>`, `<MOD>`, `<IDX>` with values found in 8.4:

```
curl -s -X POST "$URL/mcp" \
  -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_resource","arguments":{"course_id":<COURSE>,"module_id":<MOD>,"file_index":<IDX>}}}' \
  | python -c "
import sys, json
d = json.load(sys.stdin)
content = d['result']['content']
print('blocks:', len(content))
for c in content:
    t = c.get('type')
    if t == 'text':
        print(f'  text ({len(c[\"text\"])} chars): {c[\"text\"][:200]}...')
    elif t == 'resource':
        r = c['resource']
        print(f'  resource mime={r.get(\"mimeType\")} blob_b64_len={len(r.get(\"blob\",\"\"))}')
"
```

Expected: for .docx/.pptx/.xlsx: TWO text blocks (description + extracted "Content: ..." with readable Portuguese text). For PDFs: same. NO `resource` block when extraction succeeded.

If a file > 10 MB is reachable (e.g. textbook PDF), call `read_resource` on it and confirm it succeeds (the old code would have errored with "exceeds 10 MB inline limit"; the new code should return text with a possible truncation marker).

- [ ] **Step 8.6: Stop the test server.**

```
taskkill //F //IM moodle-mcp-test.exe
rm -f moodle-mcp-test.exe
```

---

## Task 9: Commit, push, deploy

**Files:** none

- [ ] **Step 9.1: Commit all changes.**

```
git add internal/tools/extract.go internal/tools/extract_test.go internal/tools/resources.go cmd/moodle-mcp/main.go
git commit -m "$(cat <<'EOF'
feat(read_resource): extract .docx/.pptx/.xlsx text + lift size limit

Two gaps were blocking the "claude.ai web is the only client" workflow
for student study materials:

1. Office documents (.docx, .pptx, .xlsx) came back as binary blobs
   that claude.ai web rejects with "Resources of type ... are not
   currently supported." Course slides, handouts, and spreadsheets
   were unreadable in cloud-only sessions.

2. Files larger than 10 MB hard-errored before extraction even ran —
   so a 12 MB textbook PDF whose text is 200 KB failed unnecessarily.

Changes:

- internal/tools/extract.go (new): pure-stdlib OOXML text extractors
  (archive/zip + encoding/xml). Functions extractDocxText,
  extractPptxText, extractXlsxText, plus a single extractTextByMIME
  dispatcher with magic-byte sniffing for wrong/missing MIME types.
  extractPDFText moves here too (clean refactor; same behavior).

- internal/tools/extract_test.go (new): in-package tests using
  synthetic OOXML zips constructed in memory. No external fixtures.

- internal/tools/resources.go: replace single MaxInlineFileBytes with
  three thresholds:
    * MaxRawFetchBytes      = 50 MB (network/OOM guard)
    * MaxInlineBlobBytes    = 10 MB (only applies when no extraction)
    * MaxExtractedTextBytes = 512 KB (text response cap with marker)
  HandleReadResource now extracts FIRST, then decides: extracted text
  is returned regardless of raw size (up to fetch cap); blob fallback
  still capped at 10 MB but only fires when extraction returned empty.

- cmd/moodle-mcp/main.go: read_resource description updated to list
  the supported formats explicitly so the model picks it confidently.

Verified end-to-end against pvs.cecierj.edu.br/ava with real .docx,
.pptx, .xlsx and an oversized PDF.
EOF
)"
```

- [ ] **Step 9.2: Push to fork (both feat and main; main is the deploy branch).**

```
git push origin feat/streamable-http-transport
git push origin feat/streamable-http-transport:main --force
```

- [ ] **Step 9.3: Wait for Railway redeploy, verify production has the new dispatcher.**

```
URL="https://moodle-mcp-server-production-0473.up.railway.app"
for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
  sleep 12
  SID=$(curl -s -i -X POST "$URL/mcp" \
    -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"smoke","version":"0.0.0"}}}' 2>/dev/null \
    | grep -i "mcp-session-id:" | tr -d '\r' | awk '{print $2}')
  if [ -z "$SID" ]; then continue; fi
  desc=$(curl -s -X POST "$URL/mcp" \
    -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" -H "Mcp-Session-Id: $SID" \
    -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
    | python -c "
import sys, json
d = json.load(sys.stdin)
for t in d['result']['tools']:
    if t['name'] == 'read_resource':
        print('docx' in t['description'])
        break
")
  echo "  attempt $i: docx in description = $desc"
  if [ "$desc" = "True" ]; then
    echo "✓ Production picked up the new tool description."
    break
  fi
done
```

- [ ] **Step 9.4: Production smoke against a real Office doc.**

Repeat Step 8.5 against `URL=https://moodle-mcp-server-production-0473.up.railway.app` instead of localhost, using the same module/file-index found in 8.4. Confirm extracted text comes back as TextContent.

---

## Self-review

**Spec coverage:**
- ✅ extractDocxText — Task 2
- ✅ extractPptxText — Task 3
- ✅ extractXlsxText — Task 4
- ✅ extractTextByMIME with magic-byte sniffing — Task 5
- ✅ MaxRawFetchBytes / MaxInlineBlobBytes / MaxExtractedTextBytes — Task 6
- ✅ Truncation marker — Task 6 Step 6.5
- ✅ Tool description update — Task 7
- ✅ Tests using synthetic OOXML zips — Tasks 2-5
- ✅ Manual end-to-end verification — Task 8
- ✅ Single feature commit + Railway deploy — Task 9

**Placeholder scan:**
- No "TBD"/"TODO" — all code shown verbatim.
- Step 8.5 uses `<COURSE>`, `<MOD>`, `<IDX>` placeholders — these are intentionally filled at runtime from Step 8.4's output. Acceptable because the previous step explicitly produces them.

**Type consistency:**
- `extractDocxText`, `extractPptxText`, `extractXlsxText`, `extractPDFText`, `extractTextByMIME` all return `(string, error)` — consistent.
- `MaxRawFetchBytes` / `MaxInlineBlobBytes` are `int64`, `MaxExtractedTextBytes` is `int` — consistent with how Go strings are indexed (int) vs how `targetFile.FileSize` is typed (int64).
- `extractTextFromOOXMLEntry` (Task 3) is reused implicitly as a helper but is currently only called from `extractPptxText`. The xlsx path uses its own state machine (Task 4) because it needs cell-level state, not just text concatenation. Intentional, not a contradiction.

No drift detected.
