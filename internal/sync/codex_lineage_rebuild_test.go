package sync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/parser"
	"go.kenn.io/agentsview/internal/testjsonl"
)

func TestCodexLineageRebuildRetainsBodiesWithholdsUsageAndRecovers(t *testing.T) {
	for _, policy := range []config.ArchiveContent{config.ArchiveContentFull, config.ArchiveContentDialogue} {
		t.Run(string(policy), func(t *testing.T) {
			root := t.TempDir()
			database, err := db.OpenWithArchiveContent(t.Context(), filepath.Join(t.TempDir(), "archive.db"), policy)
			require.NoError(t, err)
			defer database.Close()
			engine := NewEngine(t.Context(), database, EngineConfig{AgentDirs: map[parser.AgentType][]string{parser.AgentCodex: {root}}, Machine: "local", ArchiveContent: policy})
			defer engine.Close()
			const child = "22222222-2222-4222-8222-222222222222"
			const parent = "11111111-1111-4111-8111-111111111111"
			const at = "2026-10-01T10:00:00Z"
			write := func(id, body string) {
				require.NoError(t, os.WriteFile(filepath.Join(root, "rollout-2026-10-01T10-00-00-"+id+".jsonl"), []byte(body), 0600))
			}
			write(child, testjsonl.JoinJSONL(testjsonl.CodexForkedSessionMetaJSON(child, parent, "/project", "codex_cli_rs", at), testjsonl.CodexTurnContextWithIDJSON("gpt-5.4", "child-turn", at), testjsonl.CodexMsgJSON("user", "child question", at), testjsonl.CodexMsgJSON("assistant", "child answer", at), testjsonl.CodexTokenCountJSON(at, 1000, 100, 600)))
			for range 2 {
				stats := engine.ResyncAll(t.Context(), nil)
				require.False(t, stats.Aborted)
				require.Equal(t, 1, stats.CodexLineagePending())
				require.False(t, stats.ProcessingComplete())
				require.False(t, database.NeedsResync())
				messages, err := database.GetAllMessages(t.Context(), "codex:"+child)
				require.NoError(t, err)
				require.Len(t, messages, 2)
				require.Equal(t, "child answer", messages[1].Content)
				usage, err := database.ExportUsageDays(t.Context(), db.UsageDaysOptions{From: "2026-10-01", To: "2026-10-01", Timezone: "UTC"})
				require.NoError(t, err)
				require.Len(t, usage.Blocked, 1)
				require.Empty(t, usage.Days)
				_, err = database.ExportConversationChanges(t.Context(), db.ConversationExportOptions{})
				require.NoError(t, err)
			}
			write(parent, testjsonl.JoinJSONL(testjsonl.CodexSessionMetaJSON(parent, "/project", "codex_cli_rs", at)))
			stats := engine.SyncAll(t.Context(), nil)
			require.True(t, stats.ProcessingComplete())
			require.Zero(t, stats.CodexLineagePending())
			usage, err := database.ExportUsageDays(t.Context(), db.UsageDaysOptions{From: "2026-10-01", To: "2026-10-01", Timezone: "UTC"})
			require.NoError(t, err)
			require.Empty(t, usage.Blocked)
		})
	}
}

func TestCodexLineagePendingNeverHidesOtherFailures(t *testing.T) {
	for _, s := range []SyncStats{{Deferred: 1}, {Deferred: 2, codexLineageDeferred: 1}, {Deferred: 1, codexLineageDeferred: 1, Aborted: true}, {Deferred: 1, codexLineageDeferred: 1, Failed: 1}, {Deferred: 1, codexLineageDeferred: 1, providerFailures: 1}} {
		require.Zero(t, s.CodexLineagePending())
	}
}
