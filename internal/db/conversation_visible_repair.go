package db

// Preserve visible identities only when the entire old visible sequence is an
// unchanged prefix. Empty accounting records have no exported conversation body.
func conversationVisiblePrefix(old, incoming []conversationRow) map[int]conversationRow {
	var visible []conversationRow
	for _, row := range old {
		if row.body != nil {
			visible = append(visible, row)
		}
	}
	result := map[int]conversationRow{}
	index := 0
	for i, row := range incoming {
		if row.body == nil {
			continue
		}
		if index < len(visible) {
			if !conversationRowsEqual(visible[index], row) {
				return nil
			}
			result[i] = visible[index]
		}
		index++
	}
	if index < len(visible) {
		return nil
	}
	return result
}

// Recover a canonical source upgrade without creating new identities.
// This is called only after a fresh canonical native parse. The complete prior
// visible projection must still exist as unambiguous tombstones; a shorter
// suffix additionally requires verified parent replay and a unique anchor.
func recoverCanonicalProjectionTx(tx transactionQueries, sessionID string, messages []Message) error {
	key := "canonical_projection_repaired_v2:" + sessionID
	var eligible bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM sessions WHERE id=? AND agent='codex') AND NOT EXISTS(SELECT 1 FROM archive_metadata WHERE key=?)`, sessionID, key).Scan(&eligible); err != nil {
		return err
	}
	if !eligible {
		return nil
	}
	rows, err := conversationRowsTx(tx, sessionID)
	if err != nil {
		return err
	}
	var current []conversationRow
	for _, row := range rows {
		if row.body != nil {
			if row.Gap != "identity_ambiguous" || row.sourceID != "" {
				return nil
			}
			current = append(current, row)
		}
	}
	if len(current) == 0 {
		return nil
	}
	priorRows, err := tx.Query(`SELECT `+conversationChangeColumns+`,source_id FROM conversation_messages WHERE session_id=? AND removed=1 AND text_bytes>0 AND gap!='identity_ambiguous' ORDER BY ordinal,revision`, sessionID)
	if err != nil {
		return err
	}
	var prior []conversationRow
	for priorRows.Next() {
		var row conversationRow
		if err := priorRows.Scan(&row.SessionID, &row.MessageID, &row.Revision, &row.Ordinal, &row.Role, &row.Timestamp, &row.Deleted, &row.Gap, &row.Digest, &row.TextBytes, &row.sourceID); err != nil {
			priorRows.Close()
			return err
		}
		prior = append(prior, row)
	}
	if err := priorRows.Err(); err != nil {
		priorRows.Close()
		return err
	}
	if err := priorRows.Close(); err != nil {
		return err
	}
	// Multiple historical generations or repeated coordinates cannot prove which
	// former identity survived. Refuse the entire repair instead of picking one.
	for i, row := range prior {
		if row.sourceID != "" || row.Gap != "identity_unavailable" || (i > 0 && row.Ordinal <= prior[i-1].Ordinal) {
			return nil
		}
	}
	offset := len(prior) - len(current)
	if offset < 0 {
		return nil
	}
	equal := func(a, b conversationRow) bool {
		return a.Role == b.Role && ((a.Timestamp == nil && b.Timestamp == nil) || (a.Timestamp != nil && b.Timestamp != nil && *a.Timestamp == *b.Timestamp)) && a.sourceID == b.sourceID && a.Digest == b.Digest && a.TextBytes == b.TextBytes
	}
	for i, row := range current {
		if !equal(prior[offset+i], row) {
			return nil
		}
	}
	if offset > 0 {
		if len(messages) == 0 {
			return nil
		}
		for _, m := range messages {
			if !m.ParentReplayResolved {
				return nil
			}
		}
		anchors := 0
		for _, row := range prior {
			if equal(row, current[0]) {
				anchors++
			}
		}
		if anchors != 1 {
			return nil
		}
	}
	for i, row := range current {
		if _, err := tx.Exec(`UPDATE conversation_messages SET deleted=1,removed=1,body=NULL WHERE session_id=? AND message_id=?`, sessionID, row.MessageID); err != nil {
			return err
		}
		row.MessageID = prior[offset+i].MessageID
		row.Gap = prior[offset+i].Gap
		if err := putConversationRowTx(tx, row); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`INSERT INTO archive_metadata(key,value) VALUES(?,'1')`, key)
	return err
}

func prepareCanonicalProjectionRepairTx(tx transactionQueries) error {
	const key = "canonical_projection_repair_v2"
	var exists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM archive_metadata WHERE key=?)`, key).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := tx.Exec(`UPDATE sessions SET data_version=0 WHERE agent='codex' AND id IN(SELECT session_id FROM conversation_messages WHERE removed=0 AND gap='identity_ambiguous')`); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO archive_metadata(key,value) VALUES(?,'1')`, key)
	return err
}
