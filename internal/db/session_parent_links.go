package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"go.kenn.io/agentsview/internal/parser"
)

// ErrSessionParentLinkInvalid identifies invalid parent link input.
var ErrSessionParentLinkInvalid = errors.New("invalid session parent link")

// SessionParentLink is a parent recorded by an outside process, such as an
// orchestrator that launched the session as a separate agent. A parser-derived
// parent or a spawn edge takes precedence over it.
type SessionParentLink struct {
	SessionID        string `json:"session_id"`
	ParentSessionID  string `json:"parent_session_id"`
	RelationshipType string `json:"relationship_type"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
	// Applied reports whether the session row now carries this parent. It is
	// false while the session is not synced yet or when the parser found one.
	Applied bool `json:"applied"`
}

// applyParentLinksSQL writes each link in scope onto its row when the row is
// already link-owned or has no parent, no parser parent and no spawn edge.
// scope is a condition on l, or "" for every link. Applying needs no loop
// check because SetSessionParentLink rejects any link that would close one.
func applyParentLinksSQL(scope string) string {
	if scope != "" {
		scope = " AND " + scope
	}
	return `
	UPDATE sessions AS s SET
		parent_session_id = l.parent_session_id,
		relationship_type = l.relationship_type,
		parent_from_link = 1,
		local_modified_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
	FROM session_parent_links AS l
	WHERE s.id = l.session_id` + scope + `
		AND (s.parent_from_link OR (
			COALESCE(s.parent_session_id, '') = ''
			AND COALESCE(s.parser_parent_session_id, '') = ''
			AND NOT EXISTS (
				SELECT 1 FROM tool_calls tc
				WHERE tc.subagent_session_id = s.id AND tc.session_id IS NOT s.id
			)
		))
		AND NOT (s.parent_from_link
			AND s.parent_session_id IS l.parent_session_id
			AND s.relationship_type IS l.relationship_type)`
}

const parentLinkLookupSQL = `
	SELECT parent_session_id, relationship_type
	FROM session_parent_links WHERE session_id = ?`

const spawnEdgeExistsSQL = `
	SELECT EXISTS (SELECT 1 FROM tool_calls
		WHERE subagent_session_id = ?1 AND session_id IS NOT ?1)`

// parserNamesParent reports whether a parser write of s carries its own parent.
func parserNamesParent(s Session) bool {
	p := parserParentSessionID(s)
	return (s.ParentSessionID != nil && *s.ParentSessionID != "") || (p != nil && *p != "")
}

// linkedParent returns the link a parser write of s should carry in place of
// the parser's values, or nil when the parser named a parent, no link exists,
// or a spawn edge claims the row.
func linkedParent(
	ctx context.Context,
	queryRow func(context.Context, string, ...any) rowScanner,
	s Session,
) (*SessionParentLink, error) {
	if parserNamesParent(s) {
		return nil, nil
	}
	var l SessionParentLink
	err := queryRow(ctx, parentLinkLookupSQL, s.ID).Scan(&l.ParentSessionID, &l.RelationshipType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading parent link for %s: %w", s.ID, err)
	}
	return linkUnlessSpawned(ctx, queryRow, s.ID, &l)
}

// linkUnlessSpawned returns l unless a spawn edge names a parent for id; the
// linker pass after every batch then claims the row.
func linkUnlessSpawned(
	ctx context.Context,
	queryRow func(context.Context, string, ...any) rowScanner,
	id string,
	l *SessionParentLink,
) (*SessionParentLink, error) {
	var spawned bool
	if err := queryRow(ctx, spawnEdgeExistsSQL, id).Scan(&spawned); err != nil {
		return nil, fmt.Errorf("checking spawn edge for %s: %w", id, err)
	}
	if spawned {
		return nil, nil
	}
	return l, nil
}

// parentLinkLoopSQL reports whether the session bound second is reachable
// from the one bound first through stored parents and every stored link,
// applied or not. It returns NULL when it isn't, 0 when a path avoids every
// link and 1 when every path crosses one. UNION ends the walk, so it needs no
// depth bound.
const parentLinkLoopSQL = `
	WITH RECURSIVE up(id, via_link) AS (
		SELECT ?1, 0
		UNION
		SELECT s.parent_session_id, up.via_link OR s.parent_from_link FROM up
		CROSS JOIN sessions s ON s.id = up.id
		WHERE COALESCE(s.parent_session_id, '') != ''
		UNION
		SELECT l.parent_session_id, 1 FROM up
		CROSS JOIN session_parent_links l ON l.session_id = up.id
	)
	SELECT MIN(via_link) FROM up WHERE id = ?2`

// SetSessionParentLink stores an external parent for one session and applies
// it when the parser and spawn edges supply none. The session may not be
// synced yet; the link then applies when the session is written. An empty
// parentID removes the link and restores what the parser wrote.
func (db *DB) SetSessionParentLink(
	ctx context.Context,
	sessionID string,
	parentID string,
	relationship string,
) (SessionParentLink, error) {
	if err := db.requireWritable(); err != nil {
		return SessionParentLink{}, err
	}
	sessionID = strings.TrimSpace(sessionID)
	parentID = strings.TrimSpace(parentID)
	relationship = strings.TrimSpace(relationship)
	if sessionID == "" {
		return SessionParentLink{}, fmt.Errorf("%w: session_id is required", ErrSessionParentLinkInvalid)
	}
	if parentID != "" {
		if relationship == "" {
			relationship = string(parser.RelSubagent)
		}
		switch parser.RelationshipType(relationship) {
		case parser.RelSubagent, parser.RelFork, parser.RelContinuation:
		default:
			return SessionParentLink{}, fmt.Errorf(
				"%w: relationship_type must be subagent, fork, or continuation",
				ErrSessionParentLinkInvalid,
			)
		}
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	tx, err := db.getWriter().BeginTx(ctx, nil)
	if err != nil {
		return SessionParentLink{}, fmt.Errorf("beginning session parent link: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if parentID == "" {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM session_parent_links WHERE session_id = ?`, sessionID,
		); err != nil {
			return SessionParentLink{}, fmt.Errorf("deleting session parent link: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE sessions SET parent_session_id = NULL,
				relationship_type = parser_relationship_type,
				parent_from_link = 0,
				local_modified_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE id = ? AND parent_from_link`, sessionID,
		); err != nil {
			return SessionParentLink{}, fmt.Errorf("restoring parser parent: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return SessionParentLink{}, fmt.Errorf("committing session parent link: %w", err)
		}
		return SessionParentLink{SessionID: sessionID}, nil
	}

	var loop sql.NullBool
	if err := tx.QueryRowContext(ctx, parentLinkLoopSQL, parentID, sessionID).Scan(&loop); err != nil {
		return SessionParentLink{}, fmt.Errorf("checking session parent loop: %w", err)
	}
	if loop.Valid && loop.Bool {
		return SessionParentLink{}, fmt.Errorf(
			"%w: %s is already an ancestor of %s through a stored parent link; clear that link first",
			ErrSessionParentLinkInvalid, sessionID, parentID,
		)
	}
	if loop.Valid {
		return SessionParentLink{}, fmt.Errorf(
			"%w: %s is already an ancestor of %s",
			ErrSessionParentLinkInvalid, sessionID, parentID,
		)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO session_parent_links (session_id, parent_session_id, relationship_type)
		VALUES (?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			parent_session_id = excluded.parent_session_id,
			relationship_type = excluded.relationship_type,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		sessionID, parentID, relationship,
	); err != nil {
		return SessionParentLink{}, fmt.Errorf("saving session parent link: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		applyParentLinksSQL("l.session_id = ?"), sessionID,
	); err != nil {
		return SessionParentLink{}, fmt.Errorf("applying session parent link: %w", err)
	}

	var link SessionParentLink
	if err := tx.QueryRowContext(ctx, `
		SELECT l.session_id, l.parent_session_id, l.relationship_type,
			l.created_at, l.updated_at, COALESCE(s.parent_from_link, 0)
		FROM session_parent_links l
		LEFT JOIN sessions s ON s.id = l.session_id
		WHERE l.session_id = ?`, sessionID,
	).Scan(
		&link.SessionID, &link.ParentSessionID, &link.RelationshipType,
		&link.CreatedAt, &link.UpdatedAt, &link.Applied,
	); err != nil {
		return SessionParentLink{}, fmt.Errorf("loading saved session parent link: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return SessionParentLink{}, fmt.Errorf("committing session parent link: %w", err)
	}
	return link, nil
}

// SessionIDsWithLinkedParent maps each session whose parent came from an
// external link to the relationship the parser wrote for it.
func (db *DB) SessionIDsWithLinkedParent(ctx context.Context) (map[string]string, error) {
	rows, err := db.getReader().QueryContext(ctx,
		`SELECT id, parser_relationship_type FROM sessions WHERE parent_from_link`)
	if err != nil {
		return nil, fmt.Errorf("listing linked sessions: %w", err)
	}
	defer rows.Close()
	ids := make(map[string]string)
	for rows.Next() {
		var id, parserRel string
		if err := rows.Scan(&id, &parserRel); err != nil {
			return nil, fmt.Errorf("scanning linked session: %w", err)
		}
		ids[id] = parserRel
	}
	return ids, rows.Err()
}
