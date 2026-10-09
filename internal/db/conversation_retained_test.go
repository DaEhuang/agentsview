package db

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConversationRetainedSkipsChurnBeforePagination(t *testing.T) {
	for _, size := range []int{10, 20000} {
		d := testDB(t)
		require.NoError(t, d.UpsertSession(t.Context(), Session{ID: "chat", Agent: "codex", Machine: "local", Project: "sample"}))
		require.NoError(t, d.Update(t.Context(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<?)
			 INSERT INTO conversation_messages(session_id,message_id,ordinal,role,gap,deleted,removed)
			 SELECT 'chat','old-'||x,x,'assistant','visible_text_unavailable',1,1 FROM n`, size)
			return err
		}))
		legacy, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Limit: 1})
		require.NoError(t, err)
		require.NotEmpty(t, legacy.NextCursor)
		require.NoError(t, d.InsertMessages(t.Context(), []Message{
			{SessionID: "chat", Ordinal: 0, Role: "user", Content: "Retained question", SourceUUID: "q"},
			{SessionID: "chat", Ordinal: 1, Role: "assistant", Content: "Retained answer", SourceUUID: "a"},
			{SessionID: "chat", Ordinal: 2, Role: "assistant"},
		}))
		// An existing unfiltered cursor can narrow its view without resetting.
		oldEnd, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Retained: true, Cursor: legacy.NextCursor, Limit: 1})
		require.NoError(t, err)
		assert.Empty(t, oldEnd.Changes, "new writes are beyond the frozen upper bound")
		require.NotEmpty(t, oldEnd.Checkpoint)
		first, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Retained: true, Checkpoint: oldEnd.Checkpoint, Limit: 1})
		require.NoError(t, err)
		require.Len(t, first.Changes, 1)
		last, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Retained: true, Cursor: first.NextCursor, Limit: 1})
		require.NoError(t, err)
		require.Len(t, last.Changes, 1)
		require.NotEmpty(t, last.Checkpoint, "textless tail does not consume another page")
		_, err = d.ExportConversationChanges(t.Context(), ConversationExportOptions{Checkpoint: last.Checkpoint})
		require.ErrorIs(t, err, ErrInvalidCursor, "a narrowed checkpoint must not silently widen later")
		states, err := d.ExportConversationStates(t.Context(), []ConversationReference{
			{SessionID: "chat", MessageID: "old-1"}, {SessionID: "chat", MessageID: "missing"},
			{SessionID: "chat", MessageID: first.Changes[0].MessageID},
		})
		require.NoError(t, err)
		require.Len(t, states.Changes, 2, "absence is not synthesized as a deletion")
		assert.True(t, states.Changes[0].Deleted)
		assert.False(t, states.Changes[1].Deleted)
		rows, err := d.getReader().QueryContext(t.Context(), `EXPLAIN QUERY PLAN SELECT revision FROM conversation_messages INDEXED BY idx_conversation_messages_retained_revision WHERE revision>? AND revision<=? AND `+retainedConversationPredicate+` ORDER BY revision LIMIT 2`, 0, size+100)
		require.NoError(t, err)
		var plans []string
		for rows.Next() {
			var a, b, c int
			var detail string
			require.NoError(t, rows.Scan(&a, &b, &c, &detail))
			plans = append(plans, detail)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		assert.Contains(t, strings.Join(plans, "\n"), "idx_conversation_messages_retained_revision")
	}
}

func TestConversationRetainedPreservesGapsDeletionAndRestoration(t *testing.T) {
	d := testDB(t)
	require.NoError(t, d.UpsertSession(t.Context(), Session{ID: "chat", Agent: "codex", Machine: "local", Project: "sample"}))
	require.NoError(t, d.InsertMessages(t.Context(), []Message{{SessionID: "chat", Role: "user", Content: "Saved", SourceUUID: "q"}}))
	require.NoError(t, d.Update(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE conversation_messages SET gap='identity_ambiguous' WHERE session_id='chat'`)
		return err
	}))
	initial, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Retained: true})
	require.NoError(t, err)
	require.Len(t, initial.Changes, 1)
	assert.Equal(t, "identity_ambiguous", initial.Changes[0].Gap)
	require.NoError(t, d.SoftDeleteSession(t.Context(), "chat"))
	deleted, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Retained: true, Checkpoint: initial.Checkpoint})
	require.NoError(t, err)
	require.Len(t, deleted.Changes, 1)
	assert.Equal(t, "session", deleted.Changes[0].Type)
	states, err := d.ExportConversationStates(t.Context(), []ConversationReference{{SessionID: "chat", MessageID: initial.Changes[0].MessageID}, {SessionID: "chat"}})
	require.NoError(t, err)
	require.Len(t, states.Changes, 2)
	assert.True(t, states.Changes[0].Deleted)
	assert.True(t, states.Changes[1].Deleted)
	_, err = d.RestoreSession(t.Context(), "chat")
	require.NoError(t, err)
	restored, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Retained: true, Checkpoint: deleted.Checkpoint})
	require.NoError(t, err)
	require.Len(t, restored.Changes, 2)
	assert.Equal(t, initial.Changes[0].MessageID, restored.Changes[0].MessageID)
}
