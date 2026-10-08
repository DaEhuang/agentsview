package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConversationExportSourceIdentity(t *testing.T) {
	for _, tc := range []struct{ agent, id, stored, source, raw string }{
		{"codex", "codex:chat", "", "codex", "chat"},
		{"claude", "chat", "native-id", "claude", "native-id"},
		{"kiro", "kiro:chat", "", "kiro-cli", "chat"},
		{"kiro-crew", "kiro-crew:chat", "", "kiro-crew", "chat"},
		{"unknown-provider", "opaque-id", "", "unknown-provider", ""},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			d := testDB(t)
			session := Session{ID: tc.id, Project: "sample", Machine: "fixture-machine", Agent: tc.agent, SourceSessionID: tc.stored}
			require.NoError(t, d.UpsertSession(t.Context(), session))
			require.NoError(t, d.InsertMessages(t.Context(), []Message{{SessionID: tc.id, Ordinal: 0, Role: "user", Content: "Retained text", SourceUUID: "native-message"}}))
			initial, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{})
			require.NoError(t, err)
			require.Len(t, initial.Changes, 1)
			change := initial.Changes[0]
			assert.Equal(t, tc.source, change.Source)
			assert.Equal(t, "fixture-machine", change.Machine)
			assert.Equal(t, tc.raw, change.SourceSessionID)
			options := ConversationMessageOptions{DatabaseID: initial.DatabaseID, SessionID: tc.id, MessageID: change.MessageID, Revision: change.Revision}
			body, err := d.GetConversationMessage(t.Context(), options)
			require.NoError(t, err)
			assert.Equal(t, change.Source, body.Source)
			assert.Equal(t, change.Machine, body.Machine)
			assert.Equal(t, change.SourceSessionID, body.SourceSessionID)
			session.SourceSessionID = "corrected-source-id"
			require.NoError(t, d.UpsertSession(t.Context(), session))
			later, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Checkpoint: initial.Checkpoint})
			require.NoError(t, err)
			require.Len(t, later.Changes, 1)
			assert.Equal(t, "session", later.Changes[0].Type)
			assert.Equal(t, "corrected-source-id", later.Changes[0].SourceSessionID)
			body, err = d.GetConversationMessage(t.Context(), options)
			require.NoError(t, err)
			assert.Equal(t, change.Revision, body.Revision)
			assert.Equal(t, "corrected-source-id", body.SourceSessionID)
		})
	}
}
