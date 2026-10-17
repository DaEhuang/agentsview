package db

import (
	"context"
	"database/sql"
	"strings"

	"go.kenn.io/agentsview/internal/parser"
)

// Stored identity is attached inside the same read snapshot as the change or
// body. Missing deleted sessions stay unknown; consumers retain prior identity.
func attachConversationSources(ctx context.Context, tx *sql.Tx, changes []ConversationChange) error {
	ids := make([]any, 0, len(changes))
	seen := make(map[string]bool)
	for _, change := range changes {
		if !seen[change.SessionID] {
			ids = append(ids, change.SessionID)
			seen[change.SessionID] = true
		}
	}
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	rows, err := tx.QueryContext(ctx, `SELECT s.id, s.agent, `+usageAgentSQL+`, s.machine, s.source_session_id FROM sessions s WHERE s.id IN (`+placeholders+`)`, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	type identity struct{ source, machine, session string }
	bySession := make(map[string]identity)
	for rows.Next() {
		var id, agent, source, machine, raw string
		if err := rows.Scan(&id, &agent, &source, &machine, &raw); err != nil {
			return err
		}
		if raw == "" {
			if definition, ok := parser.AgentByType(parser.AgentType(agent)); ok {
				raw = parser.ProviderRawSessionIDFromFull(definition, id)
			}
		}
		bySession[id] = identity{publicUsageSource(source), machine, raw}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range changes {
		identity := bySession[changes[i].SessionID]
		changes[i].Source, changes[i].Machine, changes[i].SourceSessionID = identity.source, identity.machine, identity.session
	}
	return nil
}

// Both public exports name the CLI separately from native Crew sessions.
func publicUsageSource(agent string) string {
	if agent == "kiro" {
		return "kiro-cli"
	}
	return agent
}
