package db

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.kenn.io/agentsview/internal/parser"
)

// UsageDaysOptions selects a bounded calendar window for an archive export.
type UsageDaysOptions struct{ From, To, Timezone, Source string }

type UsageDay struct {
	Date                string   `json:"date"`
	Machine             string   `json:"machine"`
	Source              string   `json:"source"`
	Quality             string   `json:"quality"`
	InputTokens         int64    `json:"input_tokens"`
	OutputTokens        int64    `json:"output_tokens"`
	CacheReadTokens     int64    `json:"cache_read_tokens"`
	CacheWriteTokens    int64    `json:"cache_write_tokens"`
	TotalTokens         int64    `json:"total_tokens"`
	Credits             *float64 `json:"credits,omitempty"`
	CreditAuthority     string   `json:"credit_authority,omitempty"`
	EstimatePolicy      string   `json:"estimate_policy,omitempty"`
	LegacyArchiveHigher bool     `json:"legacy_archive_higher,omitempty"`
}

type UsageDaysExport struct {
	Schema     string          `json:"schema"`
	DatabaseID string          `json:"database_id"`
	Timezone   string          `json:"timezone"`
	From       string          `json:"from"`
	To         string          `json:"to"`
	Days       []UsageDay      `json:"days"`
	Blocked    []UsageDayBlock `json:"blocked,omitempty"`
}

// ExportUsageDays is the local archive's public integration boundary. It uses
// the same normalized facts, native credit ledger, and dedup as daily usage;
// callers never need SQLite access or a second provider parser.
func (d *DB) ExportUsageDays(ctx context.Context, options UsageDaysOptions) (UsageDaysExport, error) {
	if options.Timezone == "" {
		return UsageDaysExport{}, fmt.Errorf("timezone is required")
	}
	loc, err := time.LoadLocation(options.Timezone)
	if err != nil {
		return UsageDaysExport{}, err
	}
	from, err := time.ParseInLocation("2006-01-02", options.From, loc)
	if err != nil {
		return UsageDaysExport{}, fmt.Errorf("invalid from date")
	}
	to, err := time.ParseInLocation("2006-01-02", options.To, loc)
	if err != nil || to.Before(from) || to.After(from.AddDate(1, 0, 0)) {
		return UsageDaysExport{}, fmt.Errorf("date window must be ordered and no longer than one year")
	}
	filter := UsageFilter{From: options.From, To: options.To, Timezone: options.Timezone}
	snapshot, facts, _, err := d.queryUsageRollups(ctx, filter, usageQueryKindToken, true)
	if err != nil {
		return UsageDaysExport{}, err
	}
	blocks := usageDayBlocks(snapshot, options, loc, from, to)
	days := map[creditDayKey]*UsageDay{}
	get := func(key creditDayKey) *UsageDay {
		if days[key] == nil {
			days[key] = &UsageDay{Date: key.Day, Machine: key.Machine, Source: key.Source, Quality: "measured"}
		}
		return days[key]
	}
	for _, group := range facts.Groups {
		source := publicUsageSource(group.Agent)
		if options.Source != "" && options.Source != source {
			continue
		}
		day := get(creditDayKey{group.Date, group.Machine, source})
		day.InputTokens += group.InputTokens
		day.OutputTokens += group.OutputTokens
		day.CacheReadTokens += group.CacheReadTokens
		day.CacheWriteTokens += group.CacheCreationTokens
		if group.Model == parser.KiroCreditEstimateModel {
			day.Quality, day.EstimatePolicy = "estimated", parser.KiroCreditEstimateModel
		}
	}
	for _, credit := range facts.Credits {
		if options.Source != "" && options.Source != credit.Source {
			continue
		}
		day := get(creditDayKey{credit.Date, credit.Machine, credit.Source})
		day.Credits = new(credit.Credits)
		day.Quality, day.EstimatePolicy = "estimated", parser.KiroCreditEstimateModel
		day.CreditAuthority, day.LegacyArchiveHigher = credit.Authority, credit.LegacyArchiveHigher
	}
	result := UsageDaysExport{Schema: "agentsview.usage-days/v1", DatabaseID: snapshot.DatabaseID,
		Timezone: options.Timezone, From: options.From, To: options.To, Days: []UsageDay{}, Blocked: blocks}
	blocked := map[creditDayKey]bool{}
	for _, block := range blocks {
		blocked[creditDayKey{block.Date, block.Machine, block.Source}] = true
	}
	for key, day := range days {
		if blocked[key] {
			continue
		}
		day.TotalTokens = day.InputTokens + day.OutputTokens + day.CacheReadTokens + day.CacheWriteTokens
		result.Days = append(result.Days, *day)
	}
	sort.Slice(result.Days, func(i, j int) bool {
		a, b := result.Days[i], result.Days[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		if a.Machine != b.Machine {
			return a.Machine < b.Machine
		}
		return a.Source < b.Source
	})
	return result, nil
}
