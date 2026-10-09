package db

import (
	"database/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestRetentionExportRejectsIncompleteAndSharedSources(t *testing.T) {
	cases := []struct{ name, query, reason string }{
		{"complete", "", ""},
		{"stable archive identity", `UPDATE conversation_messages SET gap='identity_unavailable'`, ""},
		{"shared database", `UPDATE sessions SET file_path='/fixture/history.db'`, "shared-or-unsupported-source"},
		{"malformed", `UPDATE sessions SET parser_malformed_lines=1`, "parser-incomplete"},
		{"truncated", `UPDATE sessions SET is_truncated=1`, "parser-incomplete"},
		{"old parser", `UPDATE sessions SET data_version=0`, "source-version-unproven"},
		{"missing hash", `UPDATE sessions SET file_hash=NULL`, "source-version-unproven"},
		{"unowned", `DELETE FROM local_session_source_baselines`, "source-version-unproven"},
		{"linked parent", `UPDATE sessions SET parent_session_id='parent'`, "linked-or-shared-source"},
		{"identity conflict", `UPDATE conversation_messages SET gap='identity_ambiguous'`, "archive-gap"},
		{"message time", `UPDATE conversation_messages SET timestamp=''`, "message-incomplete"},
		{"session time", `UPDATE sessions SET ended_at=NULL`, "activity-unproven"},
		{"active", `UPDATE sessions SET termination_status='running'`, "session-active"},
		{"unfinished", `UPDATE conversation_messages SET role='user'`, "session-not-finished"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := testDB(t)
			require.NoError(t, d.UpsertSession(t.Context(), Session{ID: "thread", SourceSessionID: "native", Project: "fixture", Machine: "local", Agent: "codex"}))
			require.NoError(t, d.InsertMessages(t.Context(), []Message{{SessionID: "thread", Role: "assistant", Content: "Saved final answer", SourceUUID: "reply", Timestamp: "2026-08-01T10:00:01Z"}}))
			require.NoError(t, d.Update(t.Context(), func(tx *sql.Tx) error {
				_, err := tx.ExecContext(t.Context(), `UPDATE sessions SET file_path='/fixture/history.jsonl',file_size=1024,file_hash=?,started_at='2026-08-01T10:00:00Z',ended_at='2026-08-01T10:00:01Z',data_version=?`, strings.Repeat("a", 64), CurrentDataVersion())
				if err != nil {
					return err
				}
				_, err = tx.ExecContext(t.Context(), `INSERT INTO local_session_source_baselines VALUES('thread','local','codex','/fixture/history.jsonl')`)
				if err != nil {
					return err
				}
				if tc.query != "" {
					_, err = tx.ExecContext(t.Context(), tc.query)
				}
				return err
			}))
			result, err := d.ExportRetentionSources(t.Context(), RetentionOptions{Machine: "local", Limit: 1})
			require.NoError(t, err)
			require.Len(t, result.Sources, 1)
			assert.Equal(t, tc.reason, result.Sources[0].Reason)
			if tc.reason != "" {
				assert.Empty(t, result.Sources[0].Messages)
			} else {
				assert.Len(t, result.Sources[0].Messages, 1)
			}
			foreign, err := d.ExportRetentionSources(t.Context(), RetentionOptions{Machine: "other", Limit: 1})
			require.NoError(t, err)
			assert.Empty(t, foreign.Sources)
		})
	}
}
