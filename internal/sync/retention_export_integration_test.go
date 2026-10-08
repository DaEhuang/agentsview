package sync_test

import (
	"crypto/sha256"
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/dbtest"
	"go.kenn.io/agentsview/internal/parser"
	"go.kenn.io/agentsview/internal/sync"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetentionExportProvesParsedFileAndPreservesDeletedOriginal(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "project-a", "conversation-a.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	source := fmt.Sprintf(`{"type":"user","uuid":"user-a","sessionId":"conversation-a","cwd":%q,"timestamp":"2026-08-01T10:00:00Z","message":{"role":"user","content":"Check α"}}
{"type":"assistant","uuid":"reply-a","sessionId":"conversation-a","parentUuid":"user-a","timestamp":"2026-08-01T10:00:01Z","message":{"id":"response-a","model":"claude-sonnet-4-5","role":"assistant","content":[{"type":"text","text":"Done."}],"usage":{"input_tokens":20,"output_tokens":5}}}
`, root)
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	database := dbtest.OpenTestDB(t)
	engine := sync.NewEngine(t.Context(), database, sync.EngineConfig{AgentDirs: map[parser.AgentType][]string{parser.AgentClaude: {root}}, Machine: "local", ArchiveContent: config.ArchiveContentDialogue})
	t.Cleanup(engine.Close)
	require.Equal(t, 1, engine.SyncAll(t.Context(), nil).Synced)
	proof, err := database.ExportRetentionSources(t.Context(), db.RetentionOptions{Machine: "local", Limit: 1})
	require.NoError(t, err)
	require.Len(t, proof.Sources, 1)
	row := proof.Sources[0]
	require.Empty(t, row.Reason)
	assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(source))), row.SHA256)
	assert.Equal(t, int64(len(source)), row.Bytes)
	require.Len(t, row.Messages, 2)
	assert.Equal(t, "2026-08-01T10:00:01Z", row.LastActivity)
	usageOptions := db.UsageDaysOptions{From: "2026-08-01", To: "2026-08-01", Timezone: "UTC"}
	before, err := database.ExportUsageDays(t.Context(), usageOptions)
	require.NoError(t, err)
	require.NotEmpty(t, before.Days)
	require.NoError(t, os.Remove(path))
	engine.SyncAll(t.Context(), nil)
	after, err := database.ExportUsageDays(t.Context(), usageOptions)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	replacement := strings.ReplaceAll(strings.ReplaceAll(source, "conversation-a", "conversation-b"), "2026-08-01", "2026-09-01")
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(path), "conversation-b.jsonl"), []byte(replacement), 0o600))
	rebuilt := engine.ResyncAll(t.Context(), nil)
	require.False(t, rebuilt.Aborted)
	require.Zero(t, rebuilt.Failed)
	retainedUsage, err := database.ExportUsageDays(t.Context(), usageOptions)
	require.NoError(t, err)
	assert.Equal(t, before.Days, retainedUsage.Days)
	changes, err := database.ExportConversationChanges(t.Context(), db.ConversationExportOptions{})
	require.NoError(t, err)
	for _, m := range row.Messages {
		for _, current := range changes.Changes {
			if current.MessageID == m.MessageID {
				m.Revision = current.Revision
			}
		}
		body, err := database.GetConversationMessage(t.Context(), db.ConversationMessageOptions{DatabaseID: changes.DatabaseID, SessionID: m.SessionID, MessageID: m.MessageID, Revision: m.Revision})
		require.NoError(t, err)
		assert.NotEmpty(t, *body.Text)
	}
}
