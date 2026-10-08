package sync

import (
	"database/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/parser"
	"go.kenn.io/agentsview/internal/testjsonl"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexNativeBindingStopsAlternatingOriginals(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "archived_sessions")
	require.NoError(t, os.MkdirAll(root, 0700))
	id := "01000000-0000-7000-8000-000000000001"
	sessionID := "codex:" + id
	oldPath := filepath.Join(root, "rollout-2026-09-01T10-00-00-"+id+".jsonl")
	selected := filepath.Join(root, "rollout-2026-09-01T11-00-00-"+id+"_01000000-0000-7000-8000-000000000002.jsonl")
	content := func(text string) string {
		return testjsonl.JoinJSONL(testjsonl.CodexSessionMetaJSON(id, "/fixture", "codex_cli_rs", "2026-09-01T10:00:00Z"), `{"type":"response_item","timestamp":"2026-09-01T10:00:01Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"`+text+`"}]}}`)
	}
	require.NoError(t, os.WriteFile(oldPath, []byte(content("old original")), 0600))
	database := openTestDB(t)
	database.SetArchiveContent(config.ArchiveContentDialogue)
	cfg := EngineConfig{AgentDirs: map[parser.AgentType][]string{parser.AgentCodex: {root}}, Machine: "local", ArchiveContent: config.ArchiveContentDialogue}
	first := NewEngine(t.Context(), database, cfg)
	require.Zero(t, first.SyncAll(t.Context(), nil).Failed)
	first.Close()
	require.NoError(t, database.ReplaceSessionMessages(t.Context(), sessionID, []db.Message{{SessionID: sessionID, Role: "user", Content: "ambiguous previous copy"}}))
	require.NoError(t, os.WriteFile(selected, []byte(content("selected original")), 0600))
	native, err := sql.Open("sqlite3", filepath.Join(home, "state_5.sqlite"))
	require.NoError(t, err)
	_, err = native.Exec("CREATE TABLE threads(id TEXT,rollout_path TEXT)")
	require.NoError(t, err)
	_, err = native.Exec("INSERT INTO threads VALUES(?,?)", id, selected)
	require.NoError(t, err)
	require.NoError(t, native.Close())
	engine := NewEngine(t.Context(), database, cfg)
	t.Cleanup(engine.Close)
	stats := engine.SyncAll(t.Context(), nil)
	require.Zero(t, stats.Failed)
	page, err := database.ExportConversationChanges(t.Context(), db.ConversationExportOptions{})
	require.NoError(t, err)
	visible := 0
	for _, c := range page.Changes {
		if !c.Deleted && c.Type == "message" {
			visible++
			assert.NotEqual(t, "identity_ambiguous", c.Gap)
			body, err := database.GetConversationMessage(t.Context(), db.ConversationMessageOptions{DatabaseID: page.DatabaseID, SessionID: c.SessionID, MessageID: c.MessageID, Revision: c.Revision})
			require.NoError(t, err)
			assert.Equal(t, "selected original", *body.Text)
		}
	}
	assert.Equal(t, 1, visible)
	require.Zero(t, engine.SyncAll(t.Context(), nil).Failed)
	after, err := database.ExportConversationChanges(t.Context(), db.ConversationExportOptions{Checkpoint: page.Checkpoint})
	require.NoError(t, err)
	assert.Empty(t, after.Changes)
	assert.FileExists(t, oldPath)
}
