package db

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCanonicalConversationRepairIsOnceAndKeepsOpaqueIDs(t *testing.T) {
	d := testDB(t)
	require.NoError(t, d.UpsertSession(t.Context(), Session{ID: "chat", Agent: "codex", Project: "fixture", Machine: "local"}))
	replace := func(text string) {
		require.NoError(t, d.ReplaceSessionMessages(t.Context(), "chat", []Message{{SessionID: "chat", Ordinal: 0, Role: "user", Content: text}}))
	}
	replace("previous original")
	replace("selected original")
	before, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{})
	require.NoError(t, err)
	var current ConversationChange
	for _, c := range before.Changes {
		if !c.Deleted {
			current = c
		}
	}
	require.Equal(t, "identity_ambiguous", current.Gap)
	repair := func() {
		tx, err := d.getWriter().Begin(t.Context())
		require.NoError(t, err)
		require.NoError(t, resolveCanonicalConversationTx(tx, "chat"))
		require.NoError(t, tx.Commit())
	}
	repair()
	after, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Checkpoint: before.Checkpoint})
	require.NoError(t, err)
	require.Len(t, after.Changes, 1)
	assert.Equal(t, current.MessageID, after.Changes[0].MessageID)
	assert.Equal(t, "identity_unavailable", after.Changes[0].Gap)
	replace("later arbitrary rewrite")
	repair()
	final, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Checkpoint: after.Checkpoint})
	require.NoError(t, err)
	for _, c := range final.Changes {
		if !c.Deleted {
			assert.Equal(t, "identity_ambiguous", c.Gap, "the native binding does not authorize repeated rewrites")
		}
	}
}
