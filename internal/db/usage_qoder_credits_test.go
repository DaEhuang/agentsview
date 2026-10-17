package db

import (
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
