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
		{SessionID: "dialogue", Ordinal: 4, Role: "assistant", Content: "answer", SourceSubtype: "final_answer", ThinkingText: "hidden reasoning", HasThinking: true, Model: "model", TokenUsage: []byte(`{"input_tokens":30,"output_tokens":4}`)},
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
