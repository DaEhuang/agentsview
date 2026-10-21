package db

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestUsageDaysQoderCreditsAreDeduplicatedAndNeverInventTokens(t *testing.T) {
	d := testDB(t)
	for _, f := range []struct{ id, machine, stamp, payload string }{
		{"qoder:a", "local", "2026-06-01T16:01:00Z", `{"request_id":"request-a","credits":1.25,"billable":true,"input_tokens":0,"output_tokens":0}`},
		{"qoder:replay", "local", "2026-06-01T16:01:00Z", `{"request_id":"request-a","credits":1.25,"billable":true,"input_tokens":0,"output_tokens":0}`},
		{"qoder:free", "local", "2026-06-01T16:02:00Z", `{"request_id":"request-b","credits":99,"billable":false,"input_tokens":0,"output_tokens":0}`},
		{"qoder:unknown", "local", "2026-06-01T16:03:00Z", `{"request_id":"request-c","credits":100,"input_tokens":0,"output_tokens":0}`},
		{"qoder:next", "local", "2026-06-02T16:01:00Z", `{"request_id":"request-d","credits":2,"billable":true,"input_tokens":10,"output_tokens":2}`},
		{"qoder:other", "other", "2026-06-01T16:01:00Z", `{"request_id":"request-a","credits":1.25,"billable":true,"input_tokens":0,"output_tokens":0}`},
	} {
		require.NoError(t, d.UpsertSession(t.Context(), Session{ID: f.id, Machine: f.machine, Agent: "qoder", AgentLabel: "qoder-cn", Project: "sample", StartedAt: new(f.stamp)}))
		require.NoError(t, d.ReplaceSessionMessages(t.Context(), f.id, []Message{{SessionID: f.id, Role: "assistant", Model: "example", Timestamp: f.stamp, TokenUsage: []byte(f.payload)}}))
	}
	options := UsageDaysOptions{From: "2026-06-02", To: "2026-06-03", Timezone: "Asia/Shanghai", Source: "qoder-cn"}
	for range 2 {
		result, err := d.ExportUsageDays(t.Context(), options)
		require.NoError(t, err)
		require.Len(t, result.Days, 3)
		for _, day := range result.Days {
			require.NotNil(t, day.Credits)
			assert.Equal(t, "qoder-request-usage", day.CreditAuthority)
			assert.Empty(t, day.EstimatePolicy)
			assert.Equal(t, "measured", day.Quality)
			if day.Date == "2026-06-02" {
				assert.Equal(t, 1.25, *day.Credits)
				assert.Zero(t, day.TotalTokens)
				require.NotNil(t, day.TokenUsageKnown)
				assert.False(t, *day.TokenUsageKnown)
			} else {
				assert.Equal(t, float64(2), *day.Credits)
				assert.Equal(t, int64(12), day.TotalTokens)
				assert.Nil(t, day.TokenUsageKnown)
			}
		}
	}
}

func TestUsageDaysQoderConflictingRequestFailsClosed(t *testing.T) {
	d := testDB(t)
	stamp := "2026-06-01T12:00:00Z"
	require.NoError(t, d.UpsertSession(t.Context(), Session{ID: "qoder:conflict", Machine: "local", Agent: "qoder", AgentLabel: "qoder-cn", Project: "sample", StartedAt: new(stamp)}))
	require.NoError(t, d.ReplaceSessionMessages(t.Context(), "qoder:conflict", []Message{
		{SessionID: "qoder:conflict", Ordinal: 0, Role: "assistant", Model: "example", Timestamp: stamp, TokenUsage: []byte(`{"request_id":"same","credits":1,"billable":true,"input_tokens":0}`)},
		{SessionID: "qoder:conflict", Ordinal: 1, Role: "assistant", Model: "example", Timestamp: stamp, TokenUsage: []byte(`{"request_id":"same","credits":2,"billable":true,"input_tokens":0}`)},
	}))
	_, err := d.ExportUsageDays(t.Context(), UsageDaysOptions{From: "2026-06-01", To: "2026-06-01", Timezone: "UTC"})
	require.ErrorContains(t, err, "conflicting Qoder")
}

func TestUsageDaysQoderNonBillableOnlyEstablishesZeroChargedCredits(t *testing.T) {
	d := testDB(t)
	stamp := "2026-06-01T12:00:00Z"
	require.NoError(t, d.UpsertSession(t.Context(), Session{ID: "qoder:free", Machine: "local", Agent: "qoder", AgentLabel: "qoder-cn", Project: "sample", StartedAt: new(stamp)}))
	require.NoError(t, d.ReplaceSessionMessages(t.Context(), "qoder:free", []Message{{SessionID: "qoder:free", Role: "assistant", Timestamp: stamp, TokenUsage: []byte(`{"request_id":"free","credits":99,"billable":false,"input_tokens":0}`)}}))
	result, err := d.ExportUsageDays(t.Context(), UsageDaysOptions{From: "2026-06-01", To: "2026-06-01", Timezone: "UTC"})
	require.NoError(t, err)
	require.Len(t, result.Days, 1)
	require.NotNil(t, result.Days[0].Credits)
	assert.Zero(t, *result.Days[0].Credits)
	require.NotNil(t, result.Days[0].TokenUsageKnown)
	assert.False(t, *result.Days[0].TokenUsageKnown)
}

func TestUsageDaysQoderUnmeasuredBasisSeparatesDiscountsFreeAndRealTokens(t *testing.T) {
	for _, source := range []string{"qoder-cn", "qoder"} {
		t.Run(source, func(t *testing.T) {
			d := testDB(t)
			stamp := "2026-10-12T12:00:00Z"
			for i, payload := range []string{
				`{"request_id":"discount","credits":1,"original_credits":4,"billable":true}`,
				`{"request_id":"free","credits":2,"original_credits":3,"billable":false}`,
				`{"request_id":"real","credits":5,"original_credits":10,"billable":true,"input_tokens":100,"output_tokens":20}`,
				`{"request_id":"discount","credits":1,"original_credits":4,"billable":true}`,
				`{"request_id":"fallback","credits":2,"billable":true}`,
			} {
				id := fmt.Sprintf("qoder:%d", i)
				require.NoError(t, d.UpsertSession(t.Context(), Session{ID: id, Machine: "local", Agent: "qoder", AgentLabel: source, Project: "p", StartedAt: new(stamp)}))
				require.NoError(t, d.ReplaceSessionMessages(t.Context(), id, []Message{{SessionID: id, Role: "assistant", Model: "example", Timestamp: stamp, TokenUsage: []byte(payload)}}))
			}
			result, err := d.ExportUsageDays(t.Context(), UsageDaysOptions{From: "2026-10-12", To: "2026-10-12", Timezone: "UTC", Source: source})
			require.NoError(t, err)
			require.Len(t, result.Days, 1)
			day := result.Days[0]
			assert.Equal(t, float64(8), *day.Credits)
			assert.Equal(t, float64(9), *day.UnmeasuredCredits)
			assert.Equal(t, int64(120), day.TotalTokens)
			assert.Equal(t, "measured", day.Quality)
			// A real observation replaces the missing-token basis, even on a replay copy.
			require.NoError(t, d.ReplaceSessionMessages(t.Context(), "qoder:3", []Message{{SessionID: "qoder:3", Role: "assistant", Model: "example", Timestamp: stamp, TokenUsage: []byte(`{"request_id":"discount","credits":1,"original_credits":4,"billable":true,"input_tokens":30}`)}}))
			result, err = d.ExportUsageDays(t.Context(), UsageDaysOptions{From: "2026-10-12", To: "2026-10-12", Timezone: "UTC", Source: source})
			require.NoError(t, err)
			assert.Equal(t, float64(5), *result.Days[0].UnmeasuredCredits)
		})
	}
}
