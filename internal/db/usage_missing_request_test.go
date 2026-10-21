package db

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeSnapshotIndexUpgradePreservesArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.db")
	d, err := Open(t.Context(), path)
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })
	insertSession(t, d, "retained", "example")
	require.NoError(t, d.ReplaceSessionMessages(t.Context(), "retained", []Message{{
		SessionID: "retained", Role: "assistant", Content: "retained answer", Model: "claude-sonnet",
		ClaudeMessageID: "native-message", Timestamp: "2026-06-01T12:00:00Z", TokenUsage: []byte(`{"input_tokens":100,"output_tokens":20}`),
	}}))
	_, err = d.getWriter().Exec(t.Context(), `DROP INDEX idx_messages_claude_snapshot;
	 CREATE INDEX idx_messages_claude_snapshot ON messages(claude_message_id, claude_request_id, timestamp, session_id, ordinal)
	 WHERE token_usage != '' AND model != '' AND model != '<synthetic>' AND claude_message_id != '' AND claude_request_id != ''`)
	require.NoError(t, err)
	d.Close()
	d, err = Open(t.Context(), path)
	require.NoError(t, err)
	// Forcing this index also verifies that rows without request IDs are covered.
	var content string
	err = d.getReader().QueryRow(t.Context(), `SELECT content FROM messages INDEXED BY idx_messages_claude_snapshot
	 WHERE token_usage != '' AND model != '' AND model != '<synthetic>' AND claude_message_id != ''`).Scan(&content)
	require.NoError(t, err)
	assert.Equal(t, "retained answer", content)
}

func TestClaudeUsageSnapshotWithoutRequestID(t *testing.T) {
	d := testDB(t)
	for _, id := range []string{"original", "fork"} {
		require.NoError(t, d.UpsertSession(t.Context(), Session{ID: id, Project: "example", Machine: "local", Agent: "claude", StartedAt: new("2026-06-01T12:00:00Z")}))
	}
	require.NoError(t, d.ReplaceSessionMessages(t.Context(), "original", []Message{
		{SessionID: "original", Ordinal: 0, Role: "assistant", Timestamp: "2026-06-01T12:00:00Z", Model: "claude-sonnet", ClaudeMessageID: "message-one", TokenUsage: []byte(`{"input_tokens":100,"output_tokens":5,"cache_read_input_tokens":800}`)},
		{SessionID: "original", Ordinal: 1, Role: "assistant", Timestamp: "2026-06-01T12:00:01Z", Model: "claude-sonnet", ClaudeMessageID: "message-one", TokenUsage: []byte(`{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":800}`)},
	}))
	require.NoError(t, d.ReplaceSessionMessages(t.Context(), "fork", []Message{
		{SessionID: "fork", Ordinal: 0, Role: "assistant", Timestamp: "2026-06-01T12:00:00Z", Model: "claude-sonnet", ClaudeMessageID: "message-one", TokenUsage: []byte(`{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":800}`)},
		{SessionID: "fork", Ordinal: 1, Role: "assistant", Timestamp: "2026-06-01T12:00:02Z", Model: "claude-sonnet", ClaudeMessageID: "message-two", TokenUsage: []byte(`{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":800}`)},
	}))
	report, err := d.GetDailyUsage(t.Context(), UsageFilter{From: "2026-06-01", To: "2026-06-01", Timezone: "UTC"})
	require.NoError(t, err)
	assert.Equal(t, 200, report.Totals.InputTokens)
	assert.Equal(t, 40, report.Totals.OutputTokens)
	assert.Equal(t, 1600, report.Totals.CacheReadTokens)
}
