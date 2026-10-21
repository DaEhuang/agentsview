package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/testjsonl"
)

func TestCodexForkResolvesRetainedParentRollouts(t *testing.T) {
	const parentID = "11111111-1111-4111-8111-111111111111"
	const childID = "22222222-2222-4222-8222-222222222222"
	root := t.TempDir()
	parent := func(id, turn string) string {
		return testjsonl.JoinJSONL(
			testjsonl.CodexSessionMetaJSON(id, "/workspace/project", "codex_cli_rs", tsEarly),
			testjsonl.CodexTurnContextWithIDJSON("gpt-5.4", turn, tsEarlyS1),
			testjsonl.CodexMsgJSON("assistant", "parent answer", tsEarlyS5),
		)
	}
	first := filepath.Join(root, "rollout-2026-06-01T10-00-00-"+parentID+"_33333333-3333-4333-8333-333333333333.jsonl")
	second := filepath.Join(root, "rollout-2026-06-01T11-00-00-"+parentID+"_44444444-4444-4444-8444-444444444444.jsonl")
	require.NoError(t, os.WriteFile(first, []byte(parent(parentID, "earlier-turn")), 0600))
	require.NoError(t, os.WriteFile(second, []byte(parent(parentID, "later-turn")), 0600))
	writeCodexProviderSessionContent(t, root, childID, testjsonl.JoinJSONL(
		testjsonl.CodexForkedSessionMetaJSON(childID, parentID, "/workspace/project", "codex_cli_rs", tsEarly),
		testjsonl.CodexTurnContextWithIDJSON("gpt-5.4", "earlier-turn", tsEarlyS1),
		testjsonl.CodexMsgJSON("assistant", "replayed earlier answer", tsEarlyS5),
		testjsonl.CodexTurnContextWithIDJSON("gpt-5.4", "later-turn", tsEarlyS1),
		testjsonl.CodexMsgJSON("assistant", "replayed later answer", tsEarlyS5),
		testjsonl.CodexTurnContextWithIDJSON("gpt-5.4", "child-turn", tsEarlyS1),
		testjsonl.CodexMsgJSON("user", "child task", tsEarlyS1),
		testjsonl.CodexMsgJSON("assistant", "child answer", tsEarlyS5),
	))
	provider, ok := NewProvider(AgentCodex, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	source := requireCodexProviderSource(t, provider, childID)
	for range 2 {
		outcome, err := provider.Parse(t.Context(), ParseRequest{Source: source})
		require.NoError(t, err)
		require.Len(t, outcome.Results, 1)
		r := outcome.Results[0]
		assert.Equal(t, DataVersionCurrent, r.DataVersion)
		assert.Empty(t, r.RetryReason)
		require.Len(t, r.Result.Messages, 2)
		assert.Equal(t, "child task", r.Result.Messages[0].Content)
		assert.Equal(t, "child answer", r.Result.Messages[1].Content)
	}
	// A later parent version must invalidate the cached turn set.
	require.NoError(t, os.WriteFile(first, []byte(parent(parentID, "new-parent-turn")), 0600))
	p := provider.(*codexProvider)
	turns, ok := p.parentTurnResolver(t.Context(), source.DisplayPath)(parentID)
	require.True(t, ok)
	assert.Contains(t, turns, "new-parent-turn")
	assert.NotContains(t, turns, "earlier-turn")
	// A forged filename cannot resolve a missing parent or suppress child text.
	require.NoError(t, os.WriteFile(first, []byte(parent(childID, "child-turn")), 0600))
	require.NoError(t, os.Remove(second))
	outcome, err := provider.Parse(t.Context(), ParseRequest{Source: source})
	require.NoError(t, err)
	require.Len(t, outcome.Results, 1)
	assert.Equal(t, DataVersionNeedsRetry, outcome.Results[0].DataVersion)
	assert.Len(t, outcome.Results[0].Result.Messages, 4)
}
