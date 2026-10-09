package db

import (
	"sort"
	"time"
)

// UsageDayBlock withholds a whole source/day when an archived parser result is
// awaiting reconciliation. A monotonic consumer must never retain a temporary
// overcount from replayed parent history as an irreversible high-water mark.
type UsageDayBlock struct {
	Date    string `json:"date"`
	Machine string `json:"machine"`
	Source  string `json:"source"`
	Reason  string `json:"reason"`
}

func usageDayBlocks(snapshot usageQuerySnapshot, options UsageDaysOptions, loc *time.Location, from, to time.Time) []UsageDayBlock {
	blocks := map[string]UsageDayBlock{}
	for _, session := range snapshot.Sessions {
		if session.ProviderAgent != "codex" || session.ParserDataVersion >= CurrentDataVersion() {
			continue
		}
		machine, source := session.Machine, publicUsageSource(session.Agent)
		if options.Source != "" && options.Source != source {
			continue
		}
		// Missing dates cannot establish a safe boundary, so withhold the requested
		// range. This is a gap, never a synthesized zero or an estimated total.
		first, last := from, to
		if parsed, err := time.Parse(time.RFC3339Nano, session.StartedAt); err == nil {
			day := parsed.In(loc).Format("2006-01-02")
			if day > first.Format("2006-01-02") {
				first, _ = time.ParseInLocation("2006-01-02", day, loc)
			}
		}
		if parsed, err := time.Parse(time.RFC3339Nano, session.EndedAt); err == nil {
			day := parsed.In(loc).Format("2006-01-02")
			if day < last.Format("2006-01-02") {
				last, _ = time.ParseInLocation("2006-01-02", day, loc)
			}
		}
		for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
			block := UsageDayBlock{Date: day.Format("2006-01-02"), Machine: machine, Source: source, Reason: "parser-reconciliation-required"}
			blocks[block.Date+"\x00"+machine+"\x00"+source] = block
		}
	}
	result := make([]UsageDayBlock, 0, len(blocks))
	for _, b := range blocks {
		result = append(result, b)
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		if a.Machine != b.Machine {
			return a.Machine < b.Machine
		}
		return a.Source < b.Source
	})
	return result
}
