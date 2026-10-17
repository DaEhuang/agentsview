package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
)

const claudeSnapshotIndexDDL = `CREATE INDEX IF NOT EXISTS idx_messages_claude_snapshot
 ON messages(claude_message_id, claude_request_id, timestamp, session_id, ordinal)
 WHERE token_usage != '' AND model != '' AND model != '<synthetic>'
 AND claude_message_id != ''`

// Older archives indexed only messages with a request ID. Widen the predicate
// without touching archived rows, including sessions whose sources are gone.
func ensureClaudeSnapshotIndexLocked(ctx context.Context, w *writerHandle) error {
	tx, err := w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master
 WHERE type='index' AND name='idx_messages_claude_snapshot'`).Scan(&existing)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("checking Claude snapshot index: %w", err)
	}
	normalize := func(ddl string) string {
		return strings.ReplaceAll(strings.ToLower(strings.Join(strings.Fields(ddl), "")), "ifnotexists", "")
	}
	if existing != "" && normalize(existing) != normalize(claudeSnapshotIndexDDL) {
		log.Print("rebuilding SQLite Claude snapshot index; startup waits for the archive index migration")
		if _, err := tx.ExecContext(ctx, `DROP INDEX idx_messages_claude_snapshot`); err != nil {
			return fmt.Errorf("dropping stale Claude snapshot index: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, claudeSnapshotIndexDDL); err != nil {
		return fmt.Errorf("creating Claude snapshot index: %w", err)
	}
	return tx.Commit()
}
