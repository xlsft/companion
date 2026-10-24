package hw

import (
	"fmt"
	"strings"

	"github.com/xlsft/openclick/internal/models"
)

const gib = 1 << 30

// ModelEntry is a catalog row; kept as an alias so callers read naturally.
type ModelEntry = models.Manifest

// Size classes in ascending order. large-* is deliberately not reachable from
// the automatic ceiling: on CPU it misses the latency budget (FR-STT), and on
// GPU the user can still pick it by hand.
var classes = []string{"tiny", "base", "small", "medium", "large"}

func classRank(m ModelEntry) int {
	name := strings.TrimPrefix(m.ID, "whisper-")
	for i, c := range classes {
		if strings.HasPrefix(name, c) {
			return i
		}
	}
	return -1
}

func isQuantized(m ModelEntry) bool {
	return strings.HasPrefix(m.Quantization, "q")
}

// Recommend picks the largest model that fits the machine. lang is "auto" or
// an ISO code; "auto" and anything but English need a multilingual model.
func Recommend(p Profile, catalog models.Catalog, lang string) (ModelEntry, string) {
	ceiling, why := ceilingFor(p)

	// Budget RAM against what is free now when known, else a share of total.
	budgetMB := 0
	switch {
	case p.Memory.AvailableBytes > 0:
		budgetMB = int(p.Memory.AvailableBytes >> 20 * 8 / 10)
	case p.Memory.TotalBytes > 0:
		budgetMB = int(p.Memory.TotalBytes >> 20 * 6 / 10)
	}

	englishOnly := lang == "en"
	var best ModelEntry
	bestRank, bestScore := -1, -1
	for _, m := range catalog {
		if m.Family != "whisper" {
			continue
		}
		r := classRank(m)
		if r < 0 || r > ceiling {
			continue
		}
		if !englishOnly && !m.Multilingual() {
			continue
		}
		if budgetMB > 0 && m.RAMRequiredMB > budgetMB {
			continue
		}
		// Within a class: English-only variant for lang=en, full precision
		// when a GPU does the work, quantized when the CPU does.
		score := 0
		if englishOnly && !m.Multilingual() {
			score += 2
		}
		if p.GPU.Usable() != isQuantized(m) {
			score++
		}
		if r > bestRank || r == bestRank && score > bestScore {
			best, bestRank, bestScore = m, r, score
		}
	}
	if bestRank < 0 {
		return fallback(catalog, englishOnly)
	}
	return best, fmt.Sprintf("%s; %s fits within ~%d MB RAM budget", why, best.ID, budgetMB)
}

// ceilingFor maps hardware to the largest allowed class index.
func ceilingFor(p Profile) (int, string) {
	ramGB := float64(p.Memory.TotalBytes) / gib
	gpu := p.GPU.Usable()
	desc := fmt.Sprintf("%.0f GB RAM, %s", ramGB, gpuDesc(p.GPU))
	switch {
	case p.Memory.TotalBytes > 0 && ramGB < 2.5:
		return 0, desc + ", under 2.5 GB: tiny only"
	case p.Memory.TotalBytes > 0 && ramGB < 4:
		return 1, desc + ", under 4 GB: tiny/base only"
	case !p.CPU.FastSIMD && !gpu:
		return 1, desc + ", no AVX2/NEON: tiny/base only"
	case gpu && (p.Memory.TotalBytes == 0 || ramGB >= 8), ramGB >= 16:
		return 3, desc + ", medium allowed"
	default:
		return 2, desc + ", small allowed"
	}
}

func gpuDesc(g GPUInfo) string {
	switch {
	case g.Usable():
		return "GPU " + strings.Join(g.Backends, "+")
	case g.Known:
		return "no GPU"
	default:
		return "GPU unknown"
	}
}

// fallback returns the smallest model when nothing fit the constraints, so
// the caller always has something to offer.
func fallback(catalog models.Catalog, englishOnly bool) (ModelEntry, string) {
	var best ModelEntry
	found := false
	for _, m := range catalog {
		if m.Family != "whisper" || (!englishOnly && !m.Multilingual()) {
			continue
		}
		if !found || m.Size < best.Size {
			best, found = m, true
		}
	}
	if !found {
		return ModelEntry{}, "no compatible model in catalog"
	}
	return best, "nothing fit the hardware limits; smallest compatible model"
}
