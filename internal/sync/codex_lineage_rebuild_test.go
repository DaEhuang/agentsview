package sync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"

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
			write(child, testjsonl.JoinJSONL(testjsonl.CodexForkedSessionMetaJSON(child, parent, "/project", "codex_cli_rs", at), testjsonl.CodexTurnContextWithIDJSON("gpt-5.4", "parent-turn", at), testjsonl.CodexMsgJSON("user", "parent replay", at), testjsonl.CodexTurnContextWithIDJSON("gpt-5.4", "child-turn", at), testjsonl.CodexMsgJSON("user", "child question", at), testjsonl.CodexMsgJSON("assistant", "child answer", at), testjsonl.CodexTokenCountJSON(at, 1000, 100, 600)))
			for range 2 {
				stats := engine.ResyncAll(t.Context(), nil)
				require.False(t, stats.Aborted)
				require.Equal(t, 1, stats.CodexLineagePending())
				require.False(t, stats.ProcessingComplete())
				require.False(t, database.NeedsResync())
				messages, err := database.GetAllMessages(t.Context(), "codex:"+child)
				require.NoError(t, err)
				require.Len(t, messages, 3)
				require.Equal(t, "child answer", messages[2].Content)
				usage, err := database.ExportUsageDays(t.Context(), db.UsageDaysOptions{From: "2026-10-01", To: "2026-10-01", Timezone: "UTC"})
				require.NoError(t, err)
				require.Len(t, usage.Blocked, 1)
				require.Empty(t, usage.Days)
				_, err = database.ExportConversationChanges(t.Context(), db.ConversationExportOptions{})
				require.NoError(t, err)
			}
			write(parent+"_33333333-3333-4333-8333-333333333333", testjsonl.JoinJSONL(testjsonl.CodexSessionMetaJSON(parent, "/project", "codex_cli_rs", at), testjsonl.CodexTurnContextWithIDJSON("gpt-5.4", "parent-turn", at)))
			// Historical originals may be compressed and moved after ingestion.
			// Both child and retained parent must be found without expanding in place.
			encoder, err := zstd.NewWriter(nil)
			require.NoError(t, err)
			entries, err := os.ReadDir(root)
			require.NoError(t, err)
			for _, entry := range entries {
				path := filepath.Join(root, entry.Name())
				raw, err := os.ReadFile(path)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path+".zst", encoder.EncodeAll(raw, nil), 0600))
				require.NoError(t, os.Remove(path))
			}
			encoder.Close()
			stats := engine.SyncAll(t.Context(), nil)
			require.True(t, stats.ProcessingComplete())
			require.Zero(t, stats.CodexLineagePending())
			usage, err := database.ExportUsageDays(t.Context(), db.UsageDaysOptions{From: "2026-10-01", To: "2026-10-01", Timezone: "UTC"})
			require.NoError(t, err)
			require.Empty(t, usage.Blocked)
			all, err := database.ExportConversationChanges(t.Context(), db.ConversationExportOptions{})
			require.NoError(t, err)
			active := 0
			for _, change := range all.Changes {
				if change.SessionID == "codex:"+child && !change.Deleted {
					active++
					require.NotEqual(t, "identity_ambiguous", change.Gap)
				}
			}
			require.Equal(t, 2, active)
			again := engine.SyncAll(t.Context(), nil)
			require.True(t, again.ProcessingComplete())
			delta, err := database.ExportConversationChanges(t.Context(), db.ConversationExportOptions{Checkpoint: all.Checkpoint})
			require.NoError(t, err)
			require.Empty(t, delta.Changes)
		})
	}
}

func TestCodexLineagePendingNeverHidesOtherFailures(t *testing.T) {
	for _, s := range []SyncStats{{Deferred: 1}, {Deferred: 2, codexLineageDeferred: 1}, {Deferred: 1, codexLineageDeferred: 1, Aborted: true}, {Deferred: 1, codexLineageDeferred: 1, Failed: 1}, {Deferred: 1, codexLineageDeferred: 1, providerFailures: 1}} {
		require.Zero(t, s.CodexLineagePending())
	}
}
