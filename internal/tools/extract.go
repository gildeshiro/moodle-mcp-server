package tools

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"sort"
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
