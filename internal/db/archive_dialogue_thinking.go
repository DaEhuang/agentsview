package db

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
)

// ExtractTextContent renders provider thinking blocks into Content as well as
// ThinkingText. Dialogue policy removes that reserved rendering too; clearing
// ThinkingText alone is not sufficient. Unclosed markers remain ordinary text.
var dialogueThinkingRendering = regexp.MustCompile(`(?s)\[Thinking\]\n.*?\n\[/Thinking\](?:\r?\n)?`)

func dialogueReplyText(content string) string {
	return dialogueThinkingRendering.ReplaceAllString(content, "")
}

// Project existing rows in place, including orphaned sessions. The export
// refresh uses the same physical message coordinates and preserves opaque IDs.
// It emits new revisions instead of requiring source files or resetting cursors.
func projectStoredDialogueThinkingTx(ctx context.Context, tx *sql.Tx, where string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,session_id,content FROM messages WHERE role='assistant' AND instr(content,'[Thinking]')>0 AND `+where)
	if err != nil {
		return err
	}
	defer rows.Close()
	changed := map[string]bool{}
	for rows.Next() {
		var id int64
		var session, content string
		if err := rows.Scan(&id, &session, &content); err != nil {
			return err
		}
		projected := dialogueReplyText(content)
		if projected == content {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET content=?,content_length=?,thinking_text='',has_thinking=0 WHERE id=?`, projected, len(projected), id); err != nil {
			return err
		}
		changed[session] = true
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for session := range changed {
		if err := bumpTranscriptRevisionTx(contextTransaction{ctx: ctx, tx: tx}, session); err != nil {
			return err
		}
	}
	if len(changed) > 0 {
		return refreshConversationMessagesFromArchiveTx(ctx, tx, where)
	}
	return nil
}

func migrateDialogueThinkingTx(ctx context.Context, tx *sql.Tx) error {
	var done bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM archive_metadata WHERE key='dialogue_thinking_projection_v1')`).Scan(&done); err != nil {
		return err
	}
	if done {
		return nil
	}
	if err := projectStoredDialogueThinkingTx(ctx, tx, "1=1"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO archive_metadata(key,value) VALUES('dialogue_thinking_projection_v1','1')`)
	return err
}
