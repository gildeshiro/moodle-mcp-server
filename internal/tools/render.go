package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// errPdftoppmMissing is returned by renderPDFAsPNGs when the pdftoppm binary is
// not available in PATH. Callers can detect this with errors.Is to provide a
// graceful fallback message.
var errPdftoppmMissing = errors.New("pdftoppm not in PATH (install poppler-utils)")

// Defaults for PDF page rendering. Exposed as package vars (rather than
// constants) so tests can override them at the package level.
var (
	MaxRenderPages      = 10
	RenderDPI           = 150
	MaxRenderPNGBytes   = 2 * 1024 * 1024  // 2 MB per page
	MaxRenderTotalBytes = 15 * 1024 * 1024 // 15 MB total across all returned pages
	RenderTimeout       = 60 * time.Second
)

// renderPDFAsPNGs renders the first maxPages of the supplied PDF byte slice as
// PNG images using the external `pdftoppm` binary (poppler-utils). Returns a
// slice of PNG byte slices in page order. Returns errPdftoppmMissing when the
// binary is not in PATH so callers can degrade gracefully.
//
// This function shells out to a subprocess; expect tens-to-hundreds of ms per
// page depending on PDF complexity. Caller should treat the operation as
// "expensive" and avoid running it concurrently for the same PDF.
func renderPDFAsPNGs(data []byte, maxPages, dpi int) ([][]byte, error) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		return nil, errPdftoppmMissing
	}
	if maxPages <= 0 {
		maxPages = MaxRenderPages
	}
	if dpi <= 0 {
		dpi = RenderDPI
	}

	tmpDir, err := os.MkdirTemp("", "moodle-mcp-render-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	srcPath := filepath.Join(tmpDir, "input.pdf")
	if err := os.WriteFile(srcPath, data, 0600); err != nil {
		return nil, fmt.Errorf("writing pdf to temp: %w", err)
	}
	prefix := filepath.Join(tmpDir, "page")

	ctx, cancel := context.WithTimeout(context.Background(), RenderTimeout)
	defer cancel()

	// pdftoppm -png -r <dpi> -f 1 -l <maxPages> input.pdf prefix
	// Writes prefix-1.png, prefix-2.png, ... in tmpDir.
	cmd := exec.CommandContext(ctx, "pdftoppm",
		"-png",
		"-r", fmt.Sprintf("%d", dpi),
		"-f", "1",
		"-l", fmt.Sprintf("%d", maxPages),
		srcPath, prefix,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("pdftoppm failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}

	// Read every prefix-*.png back, in numeric order. pdftoppm pads the page
	// number when there are 10+ pages (page-01.png ... page-10.png), so we
	// sort by extracting the integer suffix.
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("reading temp dir: %w", err)
	}

	type pageEntry struct {
		num  int
		name string
	}
	var pages []pageEntry
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "page-") || !strings.HasSuffix(name, ".png") {
			continue
		}
		numStr := strings.TrimSuffix(strings.TrimPrefix(name, "page-"), ".png")
		var num int
		if _, scanErr := fmt.Sscanf(numStr, "%d", &num); scanErr != nil {
			continue
		}
		pages = append(pages, pageEntry{num: num, name: name})
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].num < pages[j].num })

	var pngs [][]byte
	for _, p := range pages {
		b, err := os.ReadFile(filepath.Join(tmpDir, p.name))
		if err != nil {
			continue
		}
		pngs = append(pngs, b)
	}
	return pngs, nil
}

// trimPNGsToBudget keeps pages in order while honoring two budgets:
//   - perPNGCap drops oversized individual pages but does NOT stop iteration —
//     a single weird page shouldn't block the rest of the document.
//   - totalCap is a running budget; once a page would push the total over,
//     iteration STOPS and every remaining page is dropped. Sequential page
//     order matters more than reaching every page, so we never re-introduce
//     gaps by skipping a page just because a later one is smaller.
//
// Returns the surviving (possibly shorter) slice plus the count of dropped
// inputs (oversized + trailing combined) so callers can mention it to the
// model.
func trimPNGsToBudget(pngs [][]byte, perPNGCap, totalCap int) ([][]byte, int) {
	var out [][]byte
	var total int
	dropped := 0
	for i, p := range pngs {
		if len(p) > perPNGCap {
			dropped++
			continue
		}
		if total+len(p) > totalCap {
			// Can't fit this page; everything after is also dropped.
			dropped += len(pngs) - i
			break
		}
		out = append(out, p)
		total += len(p)
	}
	return out, dropped
}
