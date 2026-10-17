package db

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"math"
	"sort"
	"time"
)

// Qoder CN's request ledger is already retained in token_usage. Credits are
// native billing measurements, not token estimates. Replayed branches and
// split assistant messages must not bill the same request twice.
func loadQoderCreditDays(ctx context.Context, tx *sql.Tx, sessions []usageQuerySession, filter UsageFilter) ([]CreditDay, error) {
	type requestKey struct{ Machine, ID string }
	type measurement struct {
		Day      string
		Credits  float64
		Billable bool
		Selected bool
	}
	requests := map[requestKey]measurement{}
	for _, session := range sessions {
		if session.Agent != "qoder-cn" {
			continue
		}
		rows, err := tx.QueryContext(ctx, `SELECT timestamp, token_usage FROM messages WHERE session_id=? AND token_usage IS NOT NULL AND token_usage!='' ORDER BY ordinal`, session.ID)
		if err != nil {
			return nil, err
		}
		err = func() error {
			defer rows.Close()
			for rows.Next() {
				var stamp, payload string
				if err := rows.Scan(&stamp, &payload); err != nil {
					return err
				}
				var meter struct {
					RequestID string   `json:"request_id"`
					Credits   *float64 `json:"credits"`
					Billable  *bool    `json:"billable"`
				}
				if err := json.Unmarshal([]byte(payload), &meter); err != nil {
					return fmt.Errorf("invalid Qoder credit measurement")
				}
				// Older records without billing evidence cannot establish charged usage.
				if meter.Credits == nil || meter.Billable == nil || meter.RequestID == "" {
					continue
				}
				if math.IsNaN(*meter.Credits) || math.IsInf(*meter.Credits, 0) || *meter.Credits < 0 {
					return fmt.Errorf("invalid Qoder credits")
				}
				timestamp, err := time.Parse(time.RFC3339Nano, stamp)
				if err != nil {
					return fmt.Errorf("invalid Qoder credit timestamp")
				}
				day := timestamp.In(filter.location()).Format("2006-01-02")
				key := requestKey{session.Machine, meter.RequestID}
				selected := session.PassesFilter && filter.Model == ""
				value := measurement{day, *meter.Credits, *meter.Billable, selected}
				if prior, ok := requests[key]; ok {
					if prior.Day != value.Day || prior.Credits != value.Credits || prior.Billable != value.Billable {
						return fmt.Errorf("conflicting Qoder request credit measurements")
					}
					value.Selected = value.Selected || prior.Selected
				}
				requests[key] = value
			}
			return rows.Err()
		}()
		if err != nil {
			return nil, err
		}
	}
	keys := make([]requestKey, 0, len(requests))
	for key := range requests {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Machine != keys[j].Machine {
			return keys[i].Machine < keys[j].Machine
		}
		return keys[i].ID < keys[j].ID
	})
	days := map[creditDayKey]CreditDay{}
	for _, key := range keys {
		row := requests[key]
		if !row.Selected || (filter.From != "" && row.Day < filter.From) || (filter.To != "" && row.Day > filter.To) {
			continue
		}
		k := creditDayKey{row.Day, key.Machine, "qoder-cn"}
		day := days[k]
		day.Date, day.Machine, day.Source, day.Authority = k.Day, k.Machine, k.Source, "qoder-request-usage"
		if row.Billable {
			day.Credits += row.Credits
		}
		days[k] = day
	}
	out := make([]CreditDay, 0, len(days))
	for _, day := range days {
		out = append(out, day)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].Machine < out[j].Machine
	})
	return out, nil
}
