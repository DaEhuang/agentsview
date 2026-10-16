package db

import (
	"context"
	"database/sql"
	"errors"
)

// ConversationReference names a saved gap. Missing identities are not proof
// of deletion; only an explicitly returned tombstone can resolve that gap.
type ConversationReference struct {
	SessionID string `json:"session_id"`
	MessageID string `json:"message_id"`
}

type ConversationStates struct {
	SchemaVersion int                      `json:"schema_version"`
	ArchiveID     string                   `json:"archive_id"`
	DatabaseID    string                   `json:"database_id"`
	Changes       []ConversationChange     `json:"changes"`
	Diagnostics   []ConversationDiagnostic `json:"diagnostics,omitempty"`
}

func (db *DB) ExportConversationStates(ctx context.Context, refs []ConversationReference, diagnostics ...bool) (ConversationStates, error) {
	result := ConversationStates{SchemaVersion: 1, Changes: []ConversationChange{}}
	if len(refs) == 0 || len(refs) > 32 {
		return result, errors.New("expected between 1 and 32 conversation references")
	}
	tx, err := db.getReader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	result.ArchiveID, result.DatabaseID, err = conversationArchiveIdentity(ctx, tx)
	if err != nil {
		return result, err
	}
	seen := make(map[ConversationReference]bool)
	for _, ref := range refs {
		if ref.SessionID == "" || seen[ref] {
			return result, errors.New("expected unique named conversation references")
		}
		seen[ref] = true
		var change ConversationChange
		if ref.MessageID == "" {
			change.Type, change.SessionID = "session", ref.SessionID
			err = tx.QueryRowContext(ctx, `SELECT CAST(revision AS TEXT),deleted,gap FROM conversation_session_changes WHERE session_id=?`, ref.SessionID).
				Scan(&change.Revision, &change.Deleted, &change.Gap)
		} else {
			change.Type = "message"
			err = scanConversationChange(tx.QueryRowContext(ctx, `SELECT `+conversationChangeColumns+` FROM conversation_messages WHERE session_id=? AND message_id=?`, ref.SessionID, ref.MessageID), &change)
		}
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return result, err
		}
		result.Changes = append(result.Changes, change)
	}
	if err := attachConversationSources(ctx, tx, result.Changes); err != nil {
		return result, err
	}
	if err := db.attachConversationProjects(ctx, tx, result.ArchiveID, result.Changes); err != nil {
		return result, err
	}
	if len(diagnostics) > 0 && diagnostics[0] {
		sessions := map[string]ConversationSessionDiagnostic{}
		for _, c := range result.Changes {
			session, ok := sessions[c.SessionID]
			if !ok {
				session, err = conversationSessionDiagnostic(ctx, tx, c.SessionID)
				if err != nil {
					return result, err
				}
				sessions[c.SessionID] = session
			}
			diagnostic, err := conversationDiagnostic(ctx, tx, c, session)
			if err != nil {
				return result, err
			}
			result.Diagnostics = append(result.Diagnostics, diagnostic)
		}
	}
	return result, tx.Commit()
}
