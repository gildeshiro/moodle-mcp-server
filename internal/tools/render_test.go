package tools

import (
	"bytes"
	"testing"
)

func TestTrimPNGsToBudget_Empty(t *testing.T) {
	got, dropped := trimPNGsToBudget(nil, 100, 1000)
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
	if dropped != 0 {
		t.Errorf("expected dropped=0, got %d", dropped)
	}
}

func TestTrimPNGsToBudget_AllFit(t *testing.T) {
	in := [][]byte{
		bytes.Repeat([]byte{0x89}, 100),
		bytes.Repeat([]byte{0x89}, 200),
		bytes.Repeat([]byte{0x89}, 50),
	}
	got, dropped := trimPNGsToBudget(in, 1000, 10000)
	if len(got) != 3 {
		t.Errorf("expected 3 pngs, got %d", len(got))
	}
	if dropped != 0 {
		t.Errorf("expected dropped=0, got %d", dropped)
	}
}

func TestTrimPNGsToBudget_PerPNGCap(t *testing.T) {
	in := [][]byte{
		bytes.Repeat([]byte{0x89}, 100),
		bytes.Repeat([]byte{0x89}, 5000), // exceeds per-png
		bytes.Repeat([]byte{0x89}, 200),
	}
	got, dropped := trimPNGsToBudget(in, 1000, 10000)
	if len(got) != 2 {
		t.Errorf("expected 2 pngs (oversized one dropped), got %d", len(got))
	}
	if dropped != 1 {
		t.Errorf("expected dropped=1, got %d", dropped)
	}
}

func TestTrimPNGsToBudget_TotalCap(t *testing.T) {
	in := [][]byte{
		bytes.Repeat([]byte{0x89}, 400),
		bytes.Repeat([]byte{0x89}, 400),
		bytes.Repeat([]byte{0x89}, 400), // would push total over 1000
		bytes.Repeat([]byte{0x89}, 100), // skipped because total already too close (cumulative gate)
	}
	got, dropped := trimPNGsToBudget(in, 500, 1000)
	if len(got) != 2 {
		t.Errorf("expected 2 pngs (total cap hit), got %d (sizes: %v)", len(got), pngSizes(got))
	}
	if dropped != 2 {
		t.Errorf("expected dropped=2, got %d", dropped)
	}
}

func pngSizes(pngs [][]byte) []int {
	sizes := make([]int, len(pngs))
	for i, p := range pngs {
		sizes[i] = len(p)
	}
	return sizes
}
