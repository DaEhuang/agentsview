package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/config"
)

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
