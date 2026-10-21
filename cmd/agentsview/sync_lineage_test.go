package main

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/testjsonl"
)

func TestOfflineSyncReportsOnlyRetainedCodexLineage(t *testing.T) {
	env := newSyncCLIEnv(t)
	t.Setenv("AGENTSVIEW_NO_DAEMON", "1")
	root := t.TempDir()
	const child = "22222222-2222-4222-8222-222222222222"
	const parent = "11111111-1111-4111-8111-111111111111"
	const at = "2026-10-01T10:00:00Z"
	require.NoError(t, os.WriteFile(filepath.Join(env.DataDir, "config.toml"), []byte(fmt.Sprintf("[agents.codex]\ndirs = [%q]\n", filepath.ToSlash(root))), 0600))
	data := testjsonl.JoinJSONL(testjsonl.CodexForkedSessionMetaJSON(child, parent, "/project", "codex_cli_rs", at), testjsonl.CodexTurnContextWithIDJSON("gpt-5.4", "child-turn", at), testjsonl.CodexMsgJSON("user", "child", at), testjsonl.CodexMsgJSON("assistant", "answer", at), testjsonl.CodexTokenCountJSON(at, 1000, 100, 600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "rollout-2026-10-01T10-00-00-"+child+".jsonl"), []byte(data), 0600))
	for _, full := range []bool{true, false} {
		var incomplete bool
		out := captureStdout(t, func() { incomplete = doSync(SyncConfig{Full: full}) })
		require.True(t, incomplete)
		lines := strings.Split(strings.TrimSpace(out), "\n")
		var result map[string]any
		require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &result))
		require.Equal(t, "xplan.archive-sync/v1", result["schema"])
		require.Equal(t, "codex-lineage-pending", result["status"])
		require.Equal(t, float64(1), result["pending"])
	}
}
