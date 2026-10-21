package db

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestConversationParentSuffixRequiresNativeProofAndStaleParse(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		proof, stale, repeated bool
		preserved              bool
	}{
		{"verified repair", true, true, false, true},
		{"ordinary rewrite", false, true, false, false},
		{"current parser", true, false, false, false},
		{"repeated anchor", true, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := testDB(t)
			require.NoError(t, d.UpsertSession(t.Context(), Session{ID: "child", Agent: "codex", Machine: "local", Project: "fixture"}))
			old := []Message{{SessionID: "child", Ordinal: 0, Role: "user", Content: "parent replay"}, {SessionID: "child", Ordinal: 1, Role: "user", Content: "child question"}, {SessionID: "child", Ordinal: 2, Role: "assistant", SourceSubtype: "final_answer", Content: "child answer"}}
			if tc.repeated {
				old[0].Content = old[1].Content
			}
			require.NoError(t, d.ReplaceSessionMessages(t.Context(), "child", old))
			version := CurrentDataVersion()
			if tc.stale {
				version--
			}
			require.NoError(t, d.SetSessionDataVersion(t.Context(), "child", version))
			before, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{})
			require.NoError(t, err)
			require.Len(t, before.Changes, 3)
			next := append([]Message(nil), old[1:]...)
			for i := range next {
				next[i].Ordinal = i
				next[i].ParentReplayResolved = tc.proof
			}
			require.NoError(t, d.ReplaceSessionMessages(t.Context(), "child", next))
			after, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Retained: true})
			require.NoError(t, err)
			require.Len(t, after.Changes, 2)
			for i, c := range after.Changes {
				if tc.preserved {
					require.Equal(t, before.Changes[i+1].MessageID, c.MessageID)
					require.Equal(t, "identity_unavailable", c.Gap)
				} else {
					require.Equal(t, "identity_ambiguous", c.Gap)
				}
			}
			require.NoError(t, d.ReplaceSessionMessages(t.Context(), "child", next))
			delta, err := d.ExportConversationChanges(t.Context(), ConversationExportOptions{Checkpoint: after.Checkpoint, Retained: true})
			require.NoError(t, err)
			require.Empty(t, delta.Changes)
		})
	}
}
