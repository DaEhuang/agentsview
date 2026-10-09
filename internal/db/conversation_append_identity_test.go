package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/config"
)

func TestConversationAccountingRowsDoNotChangeOpaqueIdentity(t *testing.T) {
	d := testDB(t)
	require.NoError(t, d.UpsertSession(t.Context(), Session{ID: "chat", Agent: "codex", Machine: "fixture", Project: "sample"}))
	messages := []Message{
		{SessionID: "chat", Ordinal: 0, Role: "user", Content: "question"},
		{SessionID: "chat", Ordinal: 1, Role: "assistant", SourceSubtype: "final_answer", Content: "answer"},
		{SessionID: "chat", Ordinal: 2, Role: "user", Content: "question"},
	}
	require.NoError(t, d.ReplaceSessionMessages(t.Context(), "chat", messages))
	before, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{})
	require.NoError(t, err)
	require.Len(t, before.Changes, 3)
	messages[1].Ordinal = 2
	messages[2].Ordinal = 3
	withAccounting := append([]Message{messages[0], {SessionID: "chat", Ordinal: 1, Role: "assistant", SourceSubtype: "final_answer", TokenUsage: []byte(`{"input_tokens":40,"output_tokens":2}`)}}, messages[1:]...)
	require.NoError(t, d.ReplaceSessionMessages(t.Context(), "chat", withAccounting))
	after, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{})
	require.NoError(t, err)
	require.Len(t, after.Changes, 3, "empty final accounting does not become missing dialogue")
	ids := map[string]bool{}
	for _, c := range before.Changes {
		ids[c.MessageID] = true
	}
	for _, c := range after.Changes {
		assert.True(t, ids[c.MessageID])
		assert.False(t, c.Deleted)
		assert.NotEqual(t, "identity_ambiguous", c.Gap)
	}
}

func TestConversationCompleteReparsePreservesVerifiedAppendIdentity(t *testing.T) {
	for _, policy := range []config.ArchiveContent{config.ArchiveContentFull, config.ArchiveContentDialogue} {
		t.Run(string(policy), func(t *testing.T) {
			d := testDB(t)
			d.SetArchiveContent(policy)
			require.NoError(t, d.UpsertSession(t.Context(), Session{ID: "chat", Agent: "codex", Machine: "fixture", Project: "sample"}))
			messages := []Message{{SessionID: "chat", Ordinal: 0, Role: "user", Content: "question"},
				{SessionID: "chat", Ordinal: 1, Role: "assistant", SourceSubtype: "final_answer", Content: "answer"}}
			require.NoError(t, d.ReplaceSessionMessages(t.Context(), "chat", messages))
			first, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{})
			require.NoError(t, err)
			require.Len(t, first.Changes, 2)
			messages = append(messages, Message{SessionID: "chat", Ordinal: 2, Role: "user", Content: "next question"})
			require.NoError(t, d.ReplaceSessionMessages(t.Context(), "chat", messages))
			delta, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Checkpoint: first.Checkpoint})
			require.NoError(t, err)
			require.Len(t, delta.Changes, 1)
			assert.Equal(t, "identity_unavailable", delta.Changes[0].Gap)
			all, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{})
			require.NoError(t, err)
			require.Len(t, all.Changes, 3)
			assert.Equal(t, first.Changes[0].MessageID, all.Changes[0].MessageID)
			assert.Equal(t, first.Changes[1].MessageID, all.Changes[1].MessageID)
			// A changed old prefix still requires reconciliation.
			messages[0].Content = "different question"
			require.NoError(t, d.ReplaceSessionMessages(t.Context(), "chat", messages))
			changed, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Checkpoint: delta.Checkpoint})
			require.NoError(t, err)
			assert.True(t, changed.Changes[0].Deleted)
			assert.Equal(t, "identity_ambiguous", changed.Changes[len(changed.Changes)-1].Gap)
		})
	}
}
