package db

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConversationDiagnosticsBoundedReadOnly(t *testing.T) {
	d := testDB(t)
	source := filepath.Join(t.TempDir(), "source.jsonl.zst")
	require.NoError(t, os.WriteFile(source, []byte("original"), 0600))
	require.NoError(t, d.UpsertSession(t.Context(), Session{ID: "chat", Agent: "codex", Machine: "local", FilePath: &source}))
	require.NoError(t, d.InsertMessages(t.Context(), []Message{{SessionID: "chat", Role: "user", Content: "private message", SourceUUID: "private-native"}}))
	initial, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{})
	require.NoError(t, err)
	require.Len(t, initial.Changes, 1)
	ref := ConversationReference{SessionID: "chat", MessageID: initial.Changes[0].MessageID}
	require.NoError(t, d.Update(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<12)
   INSERT INTO conversation_messages(session_id,message_id,ordinal,role,timestamp,source_id,digest,text_bytes,gap,deleted,removed)
   SELECT session_id,'prior-'||x,x,role,timestamp,source_id,digest,text_bytes,'identity_ambiguous',1,1 FROM conversation_messages,n WHERE message_id=?`, ref.MessageID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO archive_metadata(key,value) VALUES('conversation_rollout_repaired_v3:chat','1')`)
		return err
	}))
	before, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{})
	require.NoError(t, err)
	plain, err := d.ExportConversationStates(t.Context(), []ConversationReference{ref})
	require.NoError(t, err)
	assert.Empty(t, plain.Diagnostics)
	result, err := d.ExportConversationStates(t.Context(), []ConversationReference{ref, {SessionID: "missing", MessageID: "missing"}}, true)
	require.NoError(t, err)
	require.Len(t, result.Diagnostics, 1)
	got := result.Diagnostics[0]
	assert.True(t, got.NativeIdentity)
	assert.Equal(t, 13, got.NativeOccurrences)
	assert.Equal(t, 12, got.PriorMatches)
	assert.Len(t, got.Candidates, 8)
	assert.Equal(t, 12, got.Session.RemovedVisible)
	assert.Equal(t, 1, got.Session.ActiveVisible)
	assert.True(t, got.Session.CanonicalRepaired)
	assert.False(t, got.Session.CompressedRepaired)
	assert.Equal(t, "jsonl.zst", got.Session.SourceFormat)
	assert.Equal(t, "present", got.Session.SourceStatus)
	assert.EqualValues(t, 8, got.Session.SourceBytes)
	encoded, err := json.Marshal(result.Diagnostics)
	require.NoError(t, err)
	for _, private := range []string{source, "private message", "private-native"} {
		assert.NotContains(t, string(encoded), private)
	}
	after, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{})
	require.NoError(t, err)
	assert.Equal(t, before, after)
	original, err := os.ReadFile(source)
	require.NoError(t, err)
	assert.Equal(t, "original", string(original))
	require.NoError(t, os.Remove(source))
	missing, err := d.ExportConversationStates(t.Context(), []ConversationReference{ref}, true)
	require.NoError(t, err)
	assert.Equal(t, "missing", missing.Diagnostics[0].Session.SourceStatus)
}
