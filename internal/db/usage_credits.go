package db

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"math"
	"sort"
	"time"

	"go.kenn.io/agentsview/internal/parser"
)

type archivedCreditRow struct {
	Session                usageQuerySession
	Day, Source, Authority string
	Credits                float64
}

// Load only normalized archive measurements for the candidate sessions. No
// provider files, transcript bodies, rounded currency, or text estimates are
// consulted. This read shares the candidate metadata's SQLite snapshot.
func loadArchivedCreditRows(ctx context.Context, tx *sql.Tx, sessions []usageQuerySession, filter UsageFilter) ([]archivedCreditRow, error) {
	var out []archivedCreditRow
	for _, session := range sessions {
		if session.Agent != "kiro" && session.Agent != "kiro-crew" {
			continue
		}
		err := func() error {
			rows, err := tx.QueryContext(ctx, `SELECT timestamp, token_usage FROM messages
			 WHERE session_id = ? AND source_subtype = 'metering_credit' ORDER BY ordinal`, session.ID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var stamp, payload string
				if err := rows.Scan(&stamp, &payload); err != nil {
					return err
				}
				var meter struct {
					Measurement string  `json:"measurement"`
					Source      string  `json:"source"`
					Credits     float64 `json:"credits"`
				}
				if err := json.Unmarshal([]byte(payload), &meter); err != nil {
					return fmt.Errorf("invalid archived credit measurement: %w", err)
				}
				if meter.Measurement != "credit" || (meter.Source != "kiro-cli" && meter.Source != "kiro-crew") || math.IsNaN(meter.Credits) || math.IsInf(meter.Credits, 0) || meter.Credits < 0 {
					return fmt.Errorf("invalid archived credit measurement")
				}
				timestamp, err := time.Parse(time.RFC3339Nano, stamp)
				if err != nil {
					return fmt.Errorf("invalid archived credit timestamp")
				}
				day := timestamp.In(filter.location()).Format("2006-01-02")
				if (filter.From != "" && day < filter.From) || (filter.To != "" && day > filter.To) {
					continue
				}
				authority := "cli-meter"
				if meter.Source == "kiro-crew" {
					authority = "legacy-cli-archive"
					if session.ProviderAgent == "kiro-crew" {
						authority = "crew-ledger"
					}
				}
				out = append(out, archivedCreditRow{Session: session, Day: day, Source: meter.Source, Authority: authority, Credits: meter.Credits})
			}
			return rows.Err()
		}()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

type creditDayKey struct{ Day, Machine, Source string }

// CreditDay reports native measurements separately from estimated tokens.
// During migration, daily native and legacy Crew snapshots overlap. Pick the
// larger complete daily observation for this machine instead of adding them.
// Older days with only a legacy archive remain available.
type CreditDay struct {
	Date                string  `json:"date"`
	Machine             string  `json:"machine"`
	Source              string  `json:"source"`
	Credits             float64 `json:"credits"`
	Authority           string  `json:"authority"`
	LegacyArchiveHigher bool    `json:"legacy_archive_higher,omitempty"`
}

func reconcileArchivedCredits(snapshot usageQuerySnapshot, filter UsageFilter, facts *usageFactsResult) error {
	if len(snapshot.CreditRows) == 0 {
		return nil
	}
	totals := map[creditDayKey]map[string]float64{}
	for _, row := range snapshot.CreditRows {
		key := creditDayKey{row.Day, row.Session.Machine, row.Source}
		if totals[key] == nil {
			totals[key] = map[string]float64{}
		}
		totals[key][row.Authority] += row.Credits
	}
	winners := map[creditDayKey]string{}
	for key, amounts := range totals {
		winner := "cli-meter"
		if key.Source == "kiro-crew" {
			winner = "crew-ledger"
			if amounts["legacy-cli-archive"] > amounts["crew-ledger"] {
				winner = "legacy-cli-archive"
			}
		}
		winners[key] = winner
	}
	// Rebuild only the credit-derived groups from their native measurements.
	// Provider-reported token groups and their existing dedup stay unchanged.
	covered := map[string]map[string]bool{}
	for _, row := range snapshot.CreditRows {
		if covered[row.Session.ID] == nil {
			covered[row.Session.ID] = map[string]bool{}
		}
		covered[row.Session.ID][row.Day] = true
	}
	groups := facts.Groups[:0]
	for _, group := range facts.Groups {
		if group.Model != parser.KiroCreditEstimateModel || !covered[group.SessionID][group.Date] {
			groups = append(groups, group)
		}
	}
	facts.Groups = groups
	type sessionDayKey struct{ Session, Day string }
	selected := map[sessionDayKey]archivedCreditRow{}
	days := map[creditDayKey]CreditDay{}
	for _, row := range snapshot.CreditRows {
		key := creditDayKey{row.Day, row.Session.Machine, row.Source}
		if row.Authority != winners[key] || !row.Session.PassesFilter || !usageRollupModelPasses(filter, parser.KiroCreditEstimateModel) {
			continue
		}
		k := sessionDayKey{row.Session.ID, row.Day}
		old, exists := selected[k]
		if exists {
			old.Credits += row.Credits
		} else {
			old = row
		}
		selected[k] = old
		day := days[key]
		day.Date, day.Machine, day.Source, day.Authority = key.Day, key.Machine, key.Source, row.Authority
		day.Credits += row.Credits
		day.LegacyArchiveHigher = key.Source == "kiro-crew" && row.Authority == "legacy-cli-archive" && totals[key]["crew-ledger"] > 0
		days[key] = day
	}
	for _, row := range selected {
		estimate, err := parser.EstimateKiroCredits(row.Credits)
		if err != nil {
			return err
		}
		facts.Groups = append(facts.Groups, usageFactsGroup{
			SessionID: row.Session.ID, Date: row.Day, Project: row.Session.Project,
			Agent: row.Session.Agent, Machine: row.Session.Machine, ProviderID: "kiro",
			Model: parser.KiroCreditEstimateModel, PricedModel: parser.KiroCreditEstimateModel,
			InputTokens: estimate.Input, OutputTokens: estimate.Output, CacheReadTokens: estimate.CacheRead,
			CostMicrodollars: estimate.Cost.Microdollars, ReportedCount: 1,
		})
	}
	facts.Credits = nil
	for _, day := range days {
		facts.Credits = append(facts.Credits, day)
	}
	sort.Slice(facts.Credits, func(i, j int) bool {
		a, b := facts.Credits[i], facts.Credits[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		if a.Machine != b.Machine {
			return a.Machine < b.Machine
		}
		return a.Source < b.Source
	})
	for sessionID := range covered {
		delete(facts.MatchingSessions, sessionID)
	}
	for _, group := range facts.Groups {
		if covered[group.SessionID] != nil {
			facts.MatchingSessions[group.SessionID] = UsageSessionInfo{Project: group.Project, Agent: group.Agent}
		}
	}
	return nil
}
