package db

import (
	"context"
	"errors"
)

// ReconcileCanonicalConversation is the ordinary-write counterpart of the
// batch transaction. Recheck the entire projected body in one snapshot before
// publishing any recovery; concurrent changes fail instead of clearing gaps.
func (db *DB) ReconcileCanonicalConversation(ctx context.Context, sessionID string, messages []Message) error {
	if db.usageOnlyStorage() {
		return nil
	}
	msgs := append([]Message(nil), messages...)
	_ = ValidateAndSanitize(nil, msgs, nil)
	msgs = db.messagesForStorage(msgs)
	db.mu.Lock()
	defer db.mu.Unlock()
	tx, err := db.getWriter().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := conversationRowsTx(tx, sessionID)
	if err != nil {
		return err
	}
	var expected []conversationRow
	for _, m := range msgs {
		m.SessionID = sessionID
		if row, ok := conversationRowFromMessage(m); ok {
			expected = append(expected, row)
		}
	}
	if len(rows) != len(expected) {
		return errors.New("canonical conversation changed before reconciliation")
	}
	for i := range rows {
		if !conversationRowsEqual(rows[i], expected[i]) {
			return errors.New("canonical conversation changed before reconciliation")
		}
	}
	if err := resolveCanonicalConversationTx(tx, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// Invalidate only affected parser rows. Normal sync must read the original
// source again; this migration alone never claims that a gap is repaired.
func prepareCanonicalConversationRepairTx(tx transactionQueries) error {
	var exists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM archive_metadata WHERE key='conversation_primary_repair_v2')`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := tx.Exec(`UPDATE sessions SET data_version=0 WHERE agent='codex' AND id IN
	 (SELECT session_id FROM conversation_messages WHERE gap='identity_ambiguous' AND removed=0)`); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO archive_metadata(key,value) VALUES('conversation_primary_repair_v2','1')`)
	return err
}

// A freshly parsed snapshot bound by the native thread index can establish the
// current opaque identities once. Historical tombstones stay tombstones. Later
// arbitrary rewrites still use the normal ambiguity rules and cannot repeat
// this initial recovery or conflate repeated provider UUIDs.
func resolveCanonicalConversationTx(tx transactionQueries, sessionID string) error {
	key := "conversation_primary_repaired_v2:" + sessionID
	var exists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM archive_metadata WHERE key=?)`, key).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := tx.Exec(`UPDATE conversation_messages SET gap='identity_unavailable'
	 WHERE session_id=? AND removed=0 AND source_id='' AND gap='identity_ambiguous'`, sessionID); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO archive_metadata(key,value) VALUES(?,'1')`, key)
	return err
}
