package sync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/parser"
	"go.kenn.io/agentsview/internal/testjsonl"
)

func TestCodexEmptyFinalAppendUsageAndExport(t *testing.T) {
	for _, policy := range []config.ArchiveContent{config.ArchiveContentFull, config.ArchiveContentDialogue} {
		t.Run(string(policy), func(t *testing.T) {
			root := t.TempDir()
			id := "01000000-0000-7000-8000-000000000004"
			path := filepath.Join(root, "rollout-2026-09-01T10-00-00-"+id+".jsonl")
			prefix := testjsonl.JoinJSONL(
				testjsonl.CodexSessionMetaJSON(id, "/fixture", "user", "2026-09-01T10:00:00Z"),
				testjsonl.CodexTurnContextJSON("gpt-5.4", "2026-09-01T10:00:01Z"),
				testjsonl.CodexMsgJSON("user", "check", "2026-09-01T10:00:01Z"),
				testjsonl.CodexMsgJSON("assistant", "checking", "2026-09-01T10:00:02Z"),
				testjsonl.CodexTokenCountJSON("2026-09-01T10:00:02Z", 10000, 300, 6000),
			)
			require.NoError(t, os.WriteFile(path, []byte(prefix), 0o600))
			database := openTestDB(t)
			engine := NewEngine(t.Context(), database, EngineConfig{AgentDirs: map[parser.AgentType][]string{parser.AgentCodex: {root}}, Machine: "local", ArchiveContent: policy})
			t.Cleanup(engine.Close)
			require.Zero(t, engine.SyncAll(t.Context(), nil).Failed)
			tail := testjsonl.JoinJSONL(
				`{"type":"response_item","timestamp":"2026-09-01T10:00:03Z","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":""}]}}`,
				testjsonl.CodexTokenCountJSON("2026-09-01T10:00:03Z", 47180, 42, 39040),
				testjsonl.CodexTokenCountJSON("2026-09-01T10:00:03Z", 47180, 42, 39040),
			)
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			require.NoError(t, err)
			_, err = f.WriteString(tail)
			require.NoError(t, err)
			require.NoError(t, f.Close())
			for range 2 {
				require.NoError(t, engine.SyncPathsContext(t.Context(), []string{path}))
				messages, err := database.GetMessages(t.Context(), "codex:"+id, 0, 100, true)
				require.NoError(t, err)
				require.Len(t, messages, 3)
				assert.JSONEq(t, `{"input_tokens":8140,"output_tokens":42,"cache_read_input_tokens":39040}`, string(messages[2].TokenUsage))
				page, err := database.ExportConversationChanges(t.Context(), db.ConversationExportOptions{})
				require.NoError(t, err)
				for _, change := range page.Changes {
					if !change.Deleted {
						assert.NotEqual(t, "visible_text_unavailable", change.Gap, "an explicitly empty reply is not missing text")
					}
				}
			}
		})
	}
}
