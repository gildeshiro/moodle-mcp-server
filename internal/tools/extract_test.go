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
