package db

import (
	"sort"
	"time"

	"go.kenn.io/agentsview/internal/activity"
	pricingpkg "go.kenn.io/agentsview/internal/pricing"
)

func SortedCopy(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func TranscriptRevisionValue(value *string) string {
	if value == nil || *value == "" {
		return "0"
	}
	return *value
}

// MirroredSessionMachine replaces empty and "local" archive-only sentinels with the push machine.
func MirroredSessionMachine(sess Session, fallbackMachine string) string {
	if sess.Machine != "" && sess.Machine != "local" {
		return sess.Machine
	}
	return fallbackMachine
}

func ValueOrNever(v string) string {
	if v == "" {
		return "never"
	}
	return v
}

// ActivityReportInstantBoundsUTC keeps the zone suffix because PostgreSQL and DuckDB compare parsed instants.
func ActivityReportInstantBoundsUTC(q activity.Query) (string, string) {
	return q.RangeStart.UTC().Format(time.RFC3339),
		q.RangeEnd.UTC().Format(time.RFC3339)
}

func FallbackMirrorPricingRows(updatedAt string) []ModelPricing {
	src := pricingpkg.FallbackPricing()
	out := make([]ModelPricing, len(src))
	for i, p := range src {
		bands := make([]PricingBand, len(p.Bands))
		for j, band := range p.Bands {
			bands[j] = PricingBand{
				AboveInputTokens:       band.AboveInputTokens,
				InputPerMTok:           band.InputPerMTok,
				OutputPerMTok:          band.OutputPerMTok,
				CacheCreationPerMTok:   band.CacheCreationPerMTok,
				CacheCreation1hPerMTok: band.CacheCreation1hPerMTok,
				CacheReadPerMTok:       band.CacheReadPerMTok,
				UpdatedAt:              updatedAt,
			}
		}
		out[i] = ModelPricing{
			ModelPattern:           p.ModelPattern,
			InputPerMTok:           p.InputPerMTok,
			OutputPerMTok:          p.OutputPerMTok,
			CacheCreationPerMTok:   p.CacheCreationPerMTok,
			CacheCreation1hPerMTok: p.CacheCreation1hPerMTok,
			CacheReadPerMTok:       p.CacheReadPerMTok,
			UpdatedAt:              updatedAt,
			Bands:                  bands,
		}
	}
	return out
}

func FilterWorktreeMappingsForScope(
	mappings []WorktreeProjectMapping,
	projects, excludeProjects []string,
) []WorktreeProjectMapping {
	out := make([]WorktreeProjectMapping, 0, len(mappings))
	for _, mapping := range mappings {
		if mapping.Project == "" ||
			!ProjectMatchesPushScope(
				mapping.Project, projects, excludeProjects,
			) {
			continue
		}
		if mapping.OriginalProject == "" ||
			!ProjectMatchesPushScope(
				mapping.OriginalProject, projects, excludeProjects,
			) {
			mapping.OriginalProject = ""
		}
		out = append(out, mapping)
	}
	return out
}
