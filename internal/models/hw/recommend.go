package hw

import (
	"fmt"
	"strings"

	"github.com/xlsft/companion/internal/models"
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

// SpeechLanguage is the language to recommend a model for: the configured
// speech language, or the interface language while that is "auto" (a
// Russian UI almost always means Russian speech).
func SpeechLanguage(voice, ui string) string {
	if voice != "" && voice != "auto" {
		return voice
	}
	if ui != "" {
		return ui
	}
	return "auto"
}

// budgetMB is the RAM a model may take: a share of what is free now when
// known, else of the total; 0 means unknown.
func budgetMB(p Profile) int {
	switch {
	case p.Memory.AvailableBytes > 0:
		return int(p.Memory.AvailableBytes >> 20 * 8 / 10)
	case p.Memory.TotalBytes > 0:
		return int(p.Memory.TotalBytes >> 20 * 6 / 10)
	}
	return 0
}

// Recommend picks the model for lang ("auto" or an ISO code) and this
// machine. Primary (sherpa-onnx) models come first: Parakeet for European
// languages, SenseVoice for Chinese, Japanese, Korean and Cantonese.
// whisper.cpp is recommended only for a language no primary model covers,
// or when none fits in memory.
func Recommend(p Profile, catalog models.Catalog, lang string) (ModelEntry, string) {
	if m, why, ok := recommendPrimary(p, catalog, lang); ok {
		return m, why
	}
	m, why := recommendWhisper(p, catalog, lang)
	return m, "fallback for " + lang + ": " + why
}

// recommendPrimary ranks the listed sherpa models that recognise lang.
// Offline models beat streaming ones (they see the whole utterance, and
// partials come from the rolling window anyway), and among those the
// larger one that fits wins (for streaming ones the monolingual one);
// ties keep catalogue order, which puts
// Parakeet v3 before v2 (same 0.6B encoder, so v2 is not faster). For
// "auto" the model with the most languages wins. Without AVX2/NEON or a
// GPU the smallest fitting model wins instead: a 0.6B encoder is too slow
// there.
func recommendPrimary(p Profile, catalog models.Catalog, lang string) (ModelEntry, string, bool) {
	budget := budgetMB(p)
	slow := !p.CPU.FastSIMD && !p.GPU.Usable() && p.CPU.Cores > 0
	auto := lang == "" || lang == "auto"
	var best ModelEntry
	found := false
	better := func(m ModelEntry) bool {
		switch {
		case !found:
			return true
		case slow:
			return m.RAMRequiredMB < best.RAMRequiredMB
		case auto && len(m.Languages) != len(best.Languages):
			return len(m.Languages) > len(best.Languages)
		case m.Streaming() != best.Streaming():
			return !m.Streaming()
		case m.Streaming() && len(m.Languages) != len(best.Languages):
			// A small bilingual transducer splits its capacity; the
			// monolingual one is the better pick for its language.
			return len(m.Languages) < len(best.Languages)
		}
		return m.RAMRequiredMB > best.RAMRequiredMB
	}
	for _, m := range catalog {
		if m.Family != models.FamilySherpa || m.Fallback || m.Hidden {
			continue
		}
		if !auto && !m.Supports(lang) {
			continue
		}
		if budget > 0 && m.RAMRequiredMB > budget {
			continue
		}
		if better(m) {
			best, found = m, true
		}
	}
	if !found {
		return ModelEntry{}, "", false
	}
	why := fmt.Sprintf("%s covers %s within ~%d MB RAM budget", best.ID, lang, budget)
	if slow {
		why += "; no AVX2/NEON: smallest model"
	}
	return best, why, true
}

// recommendWhisper picks the largest whisper model that fits the machine.
// lang is "auto" or an ISO code; anything but English needs a
// multilingual model.
func recommendWhisper(p Profile, catalog models.Catalog, lang string) (ModelEntry, string) {
	ceiling, why := ceilingFor(p)
	budgetMB := budgetMB(p)

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
