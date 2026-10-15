package db

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestConversationVisiblePrefixIgnoresAccountingChanges(t *testing.T) {
	d := testDB(t)
	require.NoError(t, d.UpsertSession(t.Context(), Session{ID: "chat", Agent: "codex", Machine: "local", Project: "fixture"}))
	old := []Message{{SessionID: "chat", Ordinal: 0, Role: "user", Content: "question"}, {SessionID: "chat", Ordinal: 1, Role: "assistant"}, {SessionID: "chat", Ordinal: 2, Role: "assistant", Content: "answer"}}
	require.NoError(t, d.ReplaceSessionMessages(t.Context(), "chat", old))
	before, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Retained: true})
	require.NoError(t, err)
	require.Len(t, before.Changes, 2)
	next := []Message{old[0], old[2]}
	next[1].Ordinal = 1
	require.NoError(t, d.ReplaceSessionMessages(t.Context(), "chat", next))
	after, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Retained: true})
	require.NoError(t, err)
	require.Len(t, after.Changes, 2)
	for i, c := range after.Changes {
		require.Equal(t, before.Changes[i].MessageID, c.MessageID)
		require.Equal(t, "identity_unavailable", c.Gap)
	}
	next[1].Content = "changed answer"
	require.NoError(t, d.ReplaceSessionMessages(t.Context(), "chat", next))
	changed, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Retained: true})
	require.NoError(t, err)
	require.Equal(t, "identity_ambiguous", changed.Changes[0].Gap)
}

func TestCompressedConversationRecoveryRequiresWholeOrderedProof(t *testing.T) {
	for _, tc := range []struct {
		name                                         string
		suffix, proof, compressed, repeated, changed bool
		want                                         bool
	}{
		{name: "unchanged visible projection", compressed: true, want: true},
		{name: "verified parent suffix", suffix: true, proof: true, compressed: true, want: true},
		{name: "missing parent proof", suffix: true, compressed: true},
		{name: "ordinary source", proof: true},
		{name: "repeated suffix anchor", suffix: true, proof: true, compressed: true, repeated: true},
		{name: "changed body", compressed: true, changed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := testDB(t)
			file := "rollout.jsonl"
			if tc.compressed {
				file += ".zst"
			}
			require.NoError(t, d.UpsertSession(t.Context(), Session{ID: "chat", Agent: "codex", Machine: "local", Project: "fixture", FilePath: &file}))
			old := []Message{{SessionID: "chat", Ordinal: 0, Role: "user", Content: "parent"}, {SessionID: "chat", Ordinal: 1, Role: "user", Content: "question"}, {SessionID: "chat", Ordinal: 2, Role: "assistant", Content: "answer"}}
			if !tc.suffix {
				old = old[1:]
				for i := range old {
					old[i].Ordinal = i
				}
			}
			if tc.repeated {
				old[0].Content = old[1].Content
			}
			require.NoError(t, d.ReplaceSessionMessages(t.Context(), "chat", old))
			_, err := d.getWriter().ExecContext(t.Context(), `INSERT INTO archive_metadata(key,value) VALUES('conversation_rollout_repaired_v3:chat','1')`)
			require.NoError(t, err)
			before, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Retained: true})
			require.NoError(t, err)
			// Reproduce the released bad transition without relying on the now-fixed
			// prefix implementation: prior IDs remain as tombstones, new IDs ambiguous.
			tx, err := d.getWriter().Begin(t.Context())
			require.NoError(t, err)
			prior, err := conversationRowsTx(tx, "chat")
			require.NoError(t, err)
			_, err = tx.Exec(`UPDATE conversation_messages SET removed=1,deleted=1,body=NULL WHERE session_id='chat'`)
			require.NoError(t, err)
			start := 0
			if tc.suffix {
				start = 1
			}
			var fresh []Message
			for i, m := range old[start:] {
				m.Ordinal = i
				m.ParentReplayResolved = tc.proof
				if tc.changed && i == 0 {
					m.Content = "different"
				}
				fresh = append(fresh, m)
				row, ok := conversationRowFromMessage(m)
				require.True(t, ok)
				row.MessageID = "bad-" + prior[start+i].MessageID
				row.Gap = "identity_ambiguous"
				require.NoError(t, putConversationRowTx(tx, row))
			}
			require.NoError(t, tx.Commit())
			require.NoError(t, d.ReconcileCanonicalConversation(t.Context(), "chat", fresh))
			after, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Retained: true})
			require.NoError(t, err)
			require.Len(t, after.Changes, len(fresh))
			for i, c := range after.Changes {
				if tc.want {
					require.Equal(t, before.Changes[start+i].MessageID, c.MessageID)
					require.Equal(t, "identity_unavailable", c.Gap)
				} else {
					require.Equal(t, "bad-"+before.Changes[start+i].MessageID, c.MessageID)
					require.Equal(t, "identity_ambiguous", c.Gap)
				}
			}
			require.NoError(t, d.ReconcileCanonicalConversation(t.Context(), "chat", fresh))
			again, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Checkpoint: after.Checkpoint, Retained: true})
			require.NoError(t, err)
			require.Empty(t, again.Changes)
		})
	}
}
