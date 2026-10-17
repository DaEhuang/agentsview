package db

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/config"
)

func dialogueFixture() []Message {
	return []Message{
		{SessionID: "dialogue", Ordinal: 0, Role: "user", Content: "question"},
		{SessionID: "dialogue", Ordinal: 1, Role: "assistant", Content: "working", SourceSubtype: "commentary", Model: "model", TokenUsage: []byte(`{"input_tokens":10,"output_tokens":2}`)},
		{SessionID: "dialogue", Ordinal: 2, Role: "assistant", Content: "tool arguments", HasToolUse: true, ToolCalls: []ToolCall{{ToolUseID: "t1", ToolName: "Read", InputJSON: `{"path":"example"}`, ResultContent: "private"}}},
		{SessionID: "dialogue", Ordinal: 3, Role: "user", SourceSubtype: "tool_result", Content: "private output"},
		{SessionID: "dialogue", Ordinal: 4, Role: "assistant", Content: "[Thinking]\nhidden reasoning\n[/Thinking]\nanswer", SourceSubtype: "final_answer", ThinkingText: "hidden reasoning", HasThinking: true, Model: "model", TokenUsage: []byte(`{"input_tokens":30,"output_tokens":4}`)},
		{SessionID: "dialogue", Ordinal: 5, Role: "user", Content: "legacy question"},
		{SessionID: "dialogue", Ordinal: 6, Role: "assistant", Content: "legacy progress"},
		{SessionID: "dialogue", Ordinal: 7, Role: "assistant", Content: "legacy answer"},
		{SessionID: "dialogue", Ordinal: 8, Role: "user", Content: "unfinished question"},
		{SessionID: "dialogue", Ordinal: 9, Role: "assistant", Content: "unfinished progress", SourceSubtype: "commentary"},
	}
}

func TestDialogueStorageAndOrphanCopyPreserveUsage(t *testing.T) {
	for _, copyArchive := range []bool{false, true} {
		name := "write"
		if copyArchive {
			name = "copy-without-source"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.db")
			source := testDBAtPath(t, path, "source")
			if !copyArchive {
				source.SetArchiveContent(config.ArchiveContentDialogue)
			}
			require.NoError(t, source.UpsertSession(t.Context(), Session{ID: "dialogue", Agent: "codex", Project: "example", Machine: "local", DataVersion: dataVersion}))
			require.NoError(t, source.ReplaceSessionMessages(t.Context(), "dialogue", dialogueFixture()))
			require.NoError(t, source.SetSessionDataVersion(t.Context(), "dialogue", dataVersion))
			destination := source
			if copyArchive {
				destination = testDB(t)
				destination.SetArchiveContent(config.ArchiveContentDialogue)
				_, err := destination.CopyOrphanedDataFrom(path)
				require.NoError(t, err)
			}
			rows, err := destination.GetMessages(t.Context(), "dialogue", 0, 100, true)
			require.NoError(t, err)
			require.Len(t, rows, 10)
			expected := []string{"question", "", "", "", "answer", "legacy question", "", "legacy answer", "unfinished question", ""}
			for i, m := range rows {
				assert.Equal(t, expected[i], m.Content, "ordinal %d", i)
				assert.Empty(t, m.ThinkingText)
				assert.Empty(t, m.ToolCalls)
			}
			assert.JSONEq(t, `{"input_tokens":10,"output_tokens":2}`, string(rows[1].TokenUsage))
			assert.JSONEq(t, `{"input_tokens":30,"output_tokens":4}`, string(rows[4].TokenUsage))
		})
	}
}

func TestDialogueProjectionDoesNotMutateSourceAndIsIdempotent(t *testing.T) {
	messages := dialogueFixture()
	projected := dialogueMessages(messages)
	assert.Equal(t, "working", messages[1].Content)
	assert.Equal(t, projected, dialogueMessages(projected))
	require.Len(t, projected, len(messages))
}

func TestCreditLedgerSurvivesNarrowPoliciesAndCopy(t *testing.T) {
	for _, policy := range []config.ArchiveContent{config.ArchiveContentDialogue, config.ArchiveContentUsage} {
		t.Run(string(policy), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credits.db")
			source := testDBAtPath(t, path, "source")
			source.SetArchiveContent(policy)
			require.NoError(t, source.UpsertSession(t.Context(), Session{ID: "credits", Agent: "kiro", Project: "example", Machine: "local"}))
			require.NoError(t, source.ReplaceSessionMessages(t.Context(), "credits", []Message{{SessionID: "credits", Ordinal: 0, Role: "system", IsSystem: true, SourceSubtype: "metering_credit", TokenUsage: []byte(`{"measurement":"credit","credits":3}`)}}))
			destination := testDB(t)
			destination.SetArchiveContent(policy)
			_, err := destination.CopyOrphanedDataFrom(path)
			require.NoError(t, err)
			for _, database := range []*DB{source, destination} {
				rows, err := database.GetMessages(t.Context(), "credits", 0, 10, true)
				require.NoError(t, err)
				require.Len(t, rows, 1)
				assert.Equal(t, "metering_credit", rows[0].SourceSubtype)
				assert.JSONEq(t, `{"measurement":"credit","credits":3}`, string(rows[0].TokenUsage))
			}
		})
	}
}

func TestDialogueThinkingProjectionPreservesArchiveIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.db")
	source := testDBAtPath(t, path, "source")
	require.NoError(t, source.UpsertSession(t.Context(), Session{ID: "thinking", Agent: "claude", Machine: "local"}))
	raw := "[Thinking]\nprivate reasoning\n[/Thinking]\nFinal response"
	message := Message{SessionID: "thinking", Ordinal: 0, Role: "assistant", Content: raw, HasThinking: true, ThinkingText: "private reasoning", Timestamp: "2026-10-07T00:00:00Z", TokenUsage: []byte(`{"input_tokens":19,"output_tokens":7}`)}
	require.NoError(t, source.ReplaceSessionMessages(t.Context(), "thinking", []Message{message}))
	before, err := source.ExportConversationChanges(t.Context(), ConversationExportOptions{})
	require.NoError(t, err)
	require.Len(t, before.Changes, 1)
	require.NoError(t, source.Close())
	narrowed, err := OpenWithArchiveContent(t.Context(), path, config.ArchiveContentDialogue)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, narrowed.Close()) })
	after, err := narrowed.ExportConversationChanges(t.Context(), ConversationExportOptions{Checkpoint: before.Checkpoint})
	require.NoError(t, err)
	require.Len(t, after.Changes, 1)
	assert.Equal(t, before.DatabaseID, after.DatabaseID)
	assert.Equal(t, before.Changes[0].MessageID, after.Changes[0].MessageID)
	assert.NotEqual(t, before.Changes[0].Revision, after.Changes[0].Revision)
	rows, err := narrowed.GetMessages(t.Context(), "thinking", 0, 10, true)
	require.NoError(t, err)
	assert.Equal(t, "Final response", rows[0].Content)
	assert.JSONEq(t, string(message.TokenUsage), string(rows[0].TokenUsage))
	require.NoError(t, narrowed.ReplaceSessionMessages(t.Context(), "thinking", []Message{message}))
	replay, err := narrowed.ExportConversationChanges(t.Context(), ConversationExportOptions{Checkpoint: after.Checkpoint})
	require.NoError(t, err)
	assert.Empty(t, replay.Changes, "reparse preserves migrated no-source-ID identity")
	for _, candidate := range []string{"text without markers", "[Thinking]\nunclosed", "A user quoting [Thinking]\nexample\n[/Thinking]"} {
		assert.Equal(t, candidate, dialogueMessages([]Message{{Role: "user", Content: candidate}})[0].Content)
	}
}
