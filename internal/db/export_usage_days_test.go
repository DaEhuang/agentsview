package db

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/parser"
)

func seedCreditDay(t *testing.T, d *DB, id, machine, agent, label, source, timestamp string, credits float64) {
	t.Helper()
	require.NoError(t, d.UpsertSession(t.Context(), Session{ID: id, Machine: machine, Agent: agent, AgentLabel: label, Project: "sample", StartedAt: new(timestamp)}))
	estimate, err := parser.EstimateKiroCredits(credits)
	require.NoError(t, err)
	require.NoError(t, d.ReplaceSessionMessages(t.Context(), id, []Message{{SessionID: id, Role: "system", IsSystem: true, Timestamp: timestamp, SourceSubtype: "metering_credit", SourceUUID: id + ":bill", TokenUsage: []byte(fmt.Sprintf(`{"measurement":"credit","credits":%.15g,"source":%q}`, credits, source))}}))
	require.NoError(t, d.ReplaceSessionUsageEvents(t.Context(), id, []UsageEvent{{SessionID: id, Source: "session", Model: parser.KiroCreditEstimateModel, ProviderID: "kiro", InputTokens: int(estimate.Input), OutputTokens: int(estimate.Output), CacheReadInputTokens: int(estimate.CacheRead), OccurredAt: timestamp, Cost: &estimate.Cost, CostStatus: "estimated", DedupKey: "bill"}}))
}

func TestUsageDaysNativeCreditsAndLegacyDailyHandover(t *testing.T) {
	for _, native := range []float64{.5, 1, 2} {
		t.Run(fmt.Sprint(native), func(t *testing.T) {
			d := testDB(t)
			seedCreditDay(t, d, "kiro:older", "local", "kiro", "kiro-crew", "kiro-crew", "2026-06-01T12:00:00Z", 1)
			seedCreditDay(t, d, "kiro-crew:usage:2026-06-01", "local", "kiro-crew", "kiro-crew", "kiro-crew", "2026-06-01T12:01:00Z", native)
			// Another machine and standalone CLI must not be shadowed by Crew.
			seedCreditDay(t, d, "remote:kiro:other", "remote", "kiro", "kiro-crew", "kiro-crew", "2026-06-01T12:00:00Z", 3)
			seedCreditDay(t, d, "kiro:standalone", "local", "kiro", "kiro-cli", "kiro-cli", "2026-06-01T12:00:00Z", .25)
			// Native coverage of a later day never deletes older legacy history.
			seedCreditDay(t, d, "kiro:history", "local", "kiro", "kiro-crew", "kiro-crew", "2026-05-31T12:00:00Z", 4)
			options := UsageDaysOptions{From: "2026-05-31", To: "2026-06-01", Timezone: "UTC"}
			for range 2 {
				got, err := d.ExportUsageDays(t.Context(), options)
				require.NoError(t, err)
				require.Len(t, got.Days, 4)
				var crew UsageDay
				for _, row := range got.Days {
					if row.Machine == "local" && row.Source == "kiro-crew" && row.Date == "2026-06-01" {
						crew = row
					}
				}
				require.NotNil(t, crew.Credits)
				assert.Equal(t, max(native, 1), *crew.Credits)
				estimate, err := parser.EstimateKiroCredits(max(native, 1))
				require.NoError(t, err)
				assert.Equal(t, estimate.Total, crew.TotalTokens)
				assert.Equal(t, "estimated", crew.Quality)
				assert.Equal(t, native < 1, crew.LegacyArchiveHigher)
				daily, err := d.GetDailyUsage(t.Context(), UsageFilter{From: "2026-06-01", To: "2026-06-01", Timezone: "UTC", Agent: "kiro-crew", Machine: "local"})
				require.NoError(t, err)
				assert.Equal(t, int(estimate.Total), daily.Totals.InputTokens+daily.Totals.OutputTokens+daily.Totals.CacheReadTokens)
			}
			legacy, err := d.GetAllMessages(t.Context(), "kiro:older")
			require.NoError(t, err)
			require.Len(t, legacy, 1, "handover preserves stored native credit evidence")
		})
	}
}

func TestUsageDaysSourcesAndTimezone(t *testing.T) {
	d := testDB(t)
	for _, fixture := range []struct {
		id, agent, label string
		input            int
	}{{"qoder:cn", "qoder", "qoder-cn", 100}, {"qoder:global", "qoder", "", 200}, {"codex:sample", "codex", "", 300}} {
		require.NoError(t, d.UpsertSession(t.Context(), Session{ID: fixture.id, Agent: fixture.agent, AgentLabel: fixture.label, Project: "sample", Machine: "local", StartedAt: new("2026-06-01T16:01:00Z")}))
		require.NoError(t, d.ReplaceSessionMessages(t.Context(), fixture.id, []Message{{SessionID: fixture.id, Role: "assistant", Model: "sample-model", Timestamp: "2026-06-01T16:01:00Z", TokenUsage: []byte(fmt.Sprintf(`{"input_tokens":%d,"output_tokens":20}`, fixture.input))}}))
	}
	options := UsageDaysOptions{From: "2026-06-02", To: "2026-06-02", Timezone: "Asia/Shanghai", Source: "qoder-cn"}
	got, err := d.ExportUsageDays(t.Context(), options)
	require.NoError(t, err)
	require.Len(t, got.Days, 1)
	assert.Equal(t, int64(120), got.Days[0].TotalTokens)
	assert.Equal(t, "measured", got.Days[0].Quality)
	assert.Nil(t, got.Days[0].Credits)
	daily, err := d.GetDailyUsage(t.Context(), UsageFilter{From: options.From, To: options.To, Timezone: options.Timezone, Agent: "qoder-cn"})
	require.NoError(t, err)
	assert.Equal(t, 100, daily.Totals.InputTokens)
	options.From, options.To = "2026-06-01", "2026-06-01"
	got, err = d.ExportUsageDays(t.Context(), options)
	require.NoError(t, err)
	assert.Empty(t, got.Days)
}

func TestUsageDaysRejectsInvalidWindow(t *testing.T) {
	d := testDB(t)
	for _, options := range []UsageDaysOptions{{From: "2026-06-01", To: "2026-06-01"}, {From: "2026-06-02", To: "2026-06-01", Timezone: "UTC"}, {From: "2026-06-01", To: "2028-06-01", Timezone: "UTC"}, {From: "2026-06-01", To: "2026-06-01", Timezone: "invalid"}} {
		_, err := d.ExportUsageDays(t.Context(), options)
		require.Error(t, err)
	}
}

func TestUsageDaysWithholdsOnlyUnreconciledSourceDays(t *testing.T) {
	d := testDB(t)
	for _, f := range []struct {
		id, machine, day string
		current          bool
	}{
		{"codex:retry", "local", "2026-06-01", false},
		{"codex:ready", "local", "2026-06-02", true},
		{"codex:other", "remote", "2026-06-01", true},
	} {
		stamp := f.day + "T12:00:00Z"
		require.NoError(t, d.UpsertSession(t.Context(), Session{ID: f.id, Machine: f.machine, Agent: "codex", Project: "sample", StartedAt: new(stamp), EndedAt: new(stamp), FilePath: new("/synthetic/" + f.id)}))
		require.NoError(t, d.ReplaceSessionMessages(t.Context(), f.id, []Message{{SessionID: f.id, Role: "assistant", Model: "model", Timestamp: stamp, TokenUsage: []byte(`{"input_tokens":100,"output_tokens":20}`)}}))
		if f.current {
			require.NoError(t, d.SetSessionDataVersion(t.Context(), f.id, CurrentDataVersion()))
		}
	}
	seedCreditDay(t, d, "kiro:valid", "local", "kiro", "kiro-cli", "kiro-cli", "2026-06-01T12:00:00Z", 1)
	opts := UsageDaysOptions{From: "2026-06-01", To: "2026-06-02", Timezone: "UTC"}
	got, err := d.ExportUsageDays(t.Context(), opts)
	require.NoError(t, err)
	require.Len(t, got.Blocked, 1)
	assert.Equal(t, UsageDayBlock{Date: "2026-06-01", Machine: "local", Source: "codex", Reason: "parser-reconciliation-required"}, got.Blocked[0])
	require.Len(t, got.Days, 3)
	for _, day := range got.Days {
		assert.False(t, day.Date == "2026-06-01" && day.Machine == "local" && day.Source == "codex")
	}
	require.NoError(t, d.SetSessionDataVersion(t.Context(), "codex:retry", CurrentDataVersion()))
	got, err = d.ExportUsageDays(t.Context(), opts)
	require.NoError(t, err)
	assert.Empty(t, got.Blocked)
	assert.Len(t, got.Days, 4)
}
