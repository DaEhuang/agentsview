package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
)

// ConversationDiagnostic contains no message body, native identifier or path.
// Counts are scoped to a requested session; candidates are limited to eight.
type ConversationDiagnostic struct {
	SessionID         string                        `json:"session_id"`
	MessageID         string                        `json:"message_id"`
	NativeIdentity    bool                          `json:"native_identity"`
	NativeOccurrences int                           `json:"native_occurrences"`
	Removed           bool                          `json:"removed"`
	PriorMatches      int                           `json:"prior_matches"`
	Candidates        []ConversationChange          `json:"candidates"`
	Session           ConversationSessionDiagnostic `json:"session"`
}

type ConversationSessionDiagnostic struct {
	SourceFormat       string `json:"source_format"`
	SourceStatus       string `json:"source_status"`
	SourceBytes        int64  `json:"source_bytes"`
	StoredBytes        int64  `json:"stored_bytes"`
	DataVersion        int    `json:"data_version"`
	SourceMissing      bool   `json:"source_missing"`
	Deleted            bool   `json:"deleted"`
	ActiveVisible      int    `json:"active_visible"`
	Ambiguous          int    `json:"ambiguous"`
	RemovedVisible     int    `json:"removed_visible"`
	CanonicalRepaired  bool   `json:"canonical_repaired"`
	CompressedRepaired bool   `json:"compressed_repaired"`
}

func conversationSessionDiagnostic(ctx context.Context, tx *sql.Tx, id string) (ConversationSessionDiagnostic, error) {
	var d ConversationSessionDiagnostic
	var path string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(file_path,''),COALESCE(file_size,0),data_version,source_missing_at IS NOT NULL,deleted_at IS NOT NULL FROM sessions WHERE id=?`, id).
		Scan(&path, &d.StoredBytes, &d.DataVersion, &d.SourceMissing, &d.Deleted)
	if err != nil {
		return d, err
	}
	d.SourceFormat = "other"
	if strings.HasSuffix(path, ".jsonl.zst") {
		d.SourceFormat = "jsonl.zst"
	} else if strings.HasSuffix(path, ".jsonl") {
		d.SourceFormat = "jsonl"
	}
	d.SourceStatus = "unavailable"
	if path != "" {
		info, statErr := os.Stat(path)
		switch {
		case statErr == nil && info.Mode().IsRegular():
			d.SourceStatus = "present"
			d.SourceBytes = info.Size()
		case statErr == nil:
			d.SourceStatus = "not-file"
		case errors.Is(statErr, os.ErrNotExist):
			d.SourceStatus = "missing"
		case errors.Is(statErr, os.ErrPermission):
			d.SourceStatus = "denied"
		}
	}
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(removed=0 AND text_bytes>0),0),COALESCE(SUM(removed=0 AND gap='identity_ambiguous'),0),COALESCE(SUM(removed=1 AND text_bytes>0),0) FROM conversation_messages WHERE session_id=?`, id).
		Scan(&d.ActiveVisible, &d.Ambiguous, &d.RemovedVisible)
	if err != nil {
		return d, err
	}
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM archive_metadata WHERE key=?),EXISTS(SELECT 1 FROM archive_metadata WHERE key=?)`, "conversation_rollout_repaired_v3:"+id, "compressed_projection_repaired_v1:"+id).
		Scan(&d.CanonicalRepaired, &d.CompressedRepaired)
	return d, err
}

func conversationDiagnostic(ctx context.Context, tx *sql.Tx, c ConversationChange, s ConversationSessionDiagnostic) (ConversationDiagnostic, error) {
	d := ConversationDiagnostic{SessionID: c.SessionID, MessageID: c.MessageID, Session: s, Candidates: []ConversationChange{}}
	if c.Type != "message" {
		return d, nil
	}
	var native string
	err := tx.QueryRowContext(ctx, `SELECT source_id,removed FROM conversation_messages WHERE session_id=? AND message_id=?`, c.SessionID, c.MessageID).Scan(&native, &d.Removed)
	if err != nil {
		return d, err
	}
	d.NativeIdentity = native != ""
	if d.NativeIdentity {
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_messages WHERE session_id=? AND source_id=?`, c.SessionID, native).Scan(&d.NativeOccurrences)
		if err != nil {
			return d, err
		}
	}
	// Equality alone is evidence, not authorization to pick an identity or repair.
	predicate := `session_id=? AND message_id!=? AND removed=1 AND digest=? AND text_bytes=? AND role=? AND NULLIF(timestamp,'') IS ?`
	args := []any{c.SessionID, c.MessageID, c.Digest, c.TextBytes, c.Role, c.Timestamp}
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_messages WHERE `+predicate, args...).Scan(&d.PriorMatches)
	if err != nil {
		return d, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+conversationChangeColumns+` FROM conversation_messages WHERE `+predicate+` ORDER BY revision DESC LIMIT 8`, args...)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		candidate := ConversationChange{Type: "message"}
		if err := scanConversationChange(rows, &candidate); err != nil {
			return d, err
		}
		d.Candidates = append(d.Candidates, candidate)
	}
	return d, rows.Err()
}
