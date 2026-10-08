package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Retention exports are evidence of the exact source version committed by the
// normal parser. They never parse sources, remove files, or grant remote ACKs.
// The caller must verify the full raw SHA, inactivity, and downstream coverage.
type RetentionOptions struct {
	Machine, After, SessionID string
	Limit                     int
}
type RetentionSource struct {
	SessionID     string               `json:"session_id"`
	Source        string               `json:"source"`
	Machine       string               `json:"machine"`
	Path          string               `json:"path"`
	SHA256        string               `json:"sha256"`
	Bytes         int64                `json:"bytes"`
	LastActivity  string               `json:"last_activity"`
	FirstActivity string               `json:"first_activity"`
	Reason        string               `json:"reason"`
	Messages      []ConversationChange `json:"messages"`
}
type RetentionExport struct {
	Schema     string            `json:"schema"`
	ArchiveID  string            `json:"archive_id"`
	DatabaseID string            `json:"database_id"`
	Sources    []RetentionSource `json:"sources"`
	Next       string            `json:"next"`
}

func (d *DB) ExportRetentionSources(ctx context.Context, opts RetentionOptions) (RetentionExport, error) {
	result := RetentionExport{Schema: "agentsview.retention-sources/v1", Sources: []RetentionSource{}}
	if opts.Machine == "" || opts.Limit < 1 || opts.Limit > 8 || opts.After != "" && opts.SessionID != "" {
		return result, errors.New("invalid retention scope")
	}
	tx, err := d.getReader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	result.ArchiveID, result.DatabaseID, err = conversationArchiveIdentity(ctx, tx)
	if err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM sessions WHERE machine=? AND deleted_at IS NULL AND source_missing_at IS NULL
 AND id>? AND (?='' OR id=?) ORDER BY id LIMIT ?`, opts.Machine, opts.After, opts.SessionID, opts.SessionID, opts.Limit+1)
	if err != nil {
		return result, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return result, err
		}
		ids = append(ids, id)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return result, err
	}
	if len(ids) > opts.Limit {
		ids = ids[:opts.Limit]
		result.Next = ids[len(ids)-1]
	}
	for _, id := range ids {
		source, err := retentionSource(ctx, tx, id)
		if err != nil {
			return result, err
		}
		result.Sources = append(result.Sources, source)
	}
	return result, tx.Commit()
}

func retentionSource(ctx context.Context, tx *sql.Tx, id string) (RetentionSource, error) {
	r := RetentionSource{SessionID: id, Messages: []ConversationChange{}}
	var agent, started, ended, parent, kind, termination string
	var malformed, version int
	var truncated, shared, related, baseline bool
	err := tx.QueryRowContext(ctx, `SELECT s.agent,`+usageAgentSQL+`,s.machine,COALESCE(s.file_path,''),COALESCE(s.file_hash,''),COALESCE(s.file_size,0),
 COALESCE(s.started_at,''),COALESCE(s.ended_at,''),COALESCE(s.parent_session_id,''),s.relationship_type,COALESCE(s.termination_status,''),
 s.parser_malformed_lines,s.is_truncated,s.data_version,
 EXISTS(SELECT 1 FROM sessions sibling WHERE sibling.file_path=s.file_path AND sibling.id<>s.id),
 EXISTS(SELECT 1 FROM sessions child WHERE child.parent_session_id=s.id OR child.parser_parent_session_id=s.id),
 EXISTS(SELECT 1 FROM local_session_source_baselines b WHERE b.session_id=s.id AND b.machine=s.machine AND b.agent=s.agent AND b.file_path=s.file_path)
 FROM sessions s WHERE s.id=?`, id).Scan(&agent, &r.Source, &r.Machine, &r.Path, &r.SHA256, &r.Bytes, &started, &ended, &parent, &kind, &termination, &malformed, &truncated, &version, &shared, &related, &baseline)
	if err != nil {
		return r, err
	}
	r.Source = publicUsageSource(r.Source)
	block := func(reason string) (RetentionSource, error) {
		r.Reason = reason
		r.Messages = []ConversationChange{}
		return r, nil
	}
	// Shared containers, sidecars, and providers without a one-file parse boundary
	// remain intact. A future provider may explicitly supply the same guarantee.
	if !strings.HasSuffix(strings.ToLower(r.Path), ".jsonl") || (agent != "codex" && agent != "claude" && agent != "qoder" && agent != "qoder-cn") {
		return block("shared-or-unsupported-source")
	}
	if shared || parent != "" || related || kind != "" {
		return block("linked-or-shared-source")
	}
	if !baseline || len(r.SHA256) != 64 || r.Bytes <= 0 || version != CurrentDataVersion() {
		return block("source-version-unproven")
	}
	if malformed != 0 || truncated {
		return block("parser-incomplete")
	}
	if termination == "running" {
		return block("session-active")
	}
	var sessionGap bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_session_changes WHERE session_id=? AND (gap<>'' OR deleted<>0))`, id).Scan(&sessionGap); err != nil {
		return r, err
	}
	if sessionGap {
		return block("archive-gap")
	}
	var first, last time.Time
	for _, value := range []string{started, ended} {
		parsed, e := time.Parse(time.RFC3339Nano, value)
		if e != nil {
			return block("activity-unproven")
		}
		if first.IsZero() || parsed.Before(first) {
			first = parsed
		}
		if parsed.After(last) {
			last = parsed
		}
	}
	// All stored message timestamps, including accounting/tool rows, participate
	// in the activity boundary. Unplaced usage must not disappear behind a date.
	times, err := tx.QueryContext(ctx, `SELECT COALESCE(timestamp,''),COALESCE(token_usage,'') FROM messages WHERE session_id=?`, id)
	if err != nil {
		return r, err
	}
	unknown := false
	for times.Next() {
		var ts, usage string
		if err = times.Scan(&ts, &usage); err != nil {
			_ = times.Close()
			return r, err
		}
		if ts == "" {
			if usage != "" && usage != "{}" {
				unknown = true
			}
			continue
		}
		v, e := time.Parse(time.RFC3339Nano, ts)
		if e != nil {
			unknown = true
			continue
		}
		if v.Before(first) {
			first = v
		}
		if v.After(last) {
			last = v
		}
	}
	if err = errors.Join(times.Err(), times.Close()); err != nil {
		return r, err
	}
	if unknown {
		return block("activity-unproven")
	}
	r.FirstActivity, r.LastActivity = first.UTC().Format(time.RFC3339Nano), last.UTC().Format(time.RFC3339Nano)
	msgs, err := tx.QueryContext(ctx, `SELECT `+conversationChangeColumns+` FROM conversation_messages WHERE session_id=? AND removed=0 ORDER BY ordinal LIMIT 4097`, id)
	if err != nil {
		return r, err
	}
	all := []ConversationChange{}
	for msgs.Next() {
		m := ConversationChange{Type: "message"}
		if err = scanConversationChange(msgs, &m); err != nil {
			_ = msgs.Close()
			return r, err
		}
		all = append(all, m)
	}
	if err = errors.Join(msgs.Err(), msgs.Close()); err != nil {
		return r, err
	}
	if len(all) > 4096 {
		return block("proof-budget-exceeded")
	}
	for _, m := range all {
		if m.Deleted || m.Gap != "" && m.Gap != "visible_text_unavailable" && m.Gap != "identity_unavailable" {
			return block("archive-gap")
		}
		if m.TextBytes == 0 {
			continue
		}
		if m.Timestamp == nil || (m.Gap != "" && m.Gap != "identity_unavailable") || m.TextBytes > 8388608 {
			return block("message-incomplete")
		}
		r.Messages = append(r.Messages, m)
	}
	if len(r.Messages) == 0 {
		return block("dialogue-unavailable")
	}
	if r.Messages[len(r.Messages)-1].Role != "assistant" {
		return block("session-not-finished")
	}
	if err = attachConversationSources(ctx, tx, r.Messages); err != nil {
		return r, err
	}
	for _, m := range r.Messages {
		if m.Machine != r.Machine || m.Source != r.Source || m.SourceSessionID == "" {
			return block("source-identity-unproven")
		}
	}
	return r, nil
}
