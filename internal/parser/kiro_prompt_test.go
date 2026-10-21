package parser

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKiroPromptSeparatesInjectedContext(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"assembled", "[AGENT SYSTEM PROMPT]\ncontext\n[END AGENT SYSTEM PROMPT]\n[CURRENT USER REQUEST — respond to this]\nactual question", "actual question"},
		{"warm", "[RUNTIME] dashboard\n[REPLY FORMAT RULES]\nrules\n[CURRENT USER REQUEST — respond to this]\nfollow-up", "follow-up"},
		{"quoted", "explain [CURRENT USER REQUEST — respond to this]\nmarker", "explain [CURRENT USER REQUEST — respond to this]\nmarker"},
		{"incomplete", "[AGENT SYSTEM PROMPT]\nno boundary", "[AGENT SYSTEM PROMPT]\nno boundary"},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, preprocessKiroPrompt(tc.input)) })
	}
}

func TestKiroBackgroundSessionIsNotHumanDialogue(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "background.jsonl")
	writeSourceFile(t, path, kiroProviderJSONLFixture("background task"))
	writeSourceFile(t, filepath.Join(root, "background.json"), `{"session_id":"background","session_state":{"agent_name":"kirocrew-lite"}}`)
	provider, ok := NewProvider(AgentKiro, ProviderConfig{Roots: []string{root}, Machine: "local"})
	require.True(t, ok)
	sess, msgs, err := provider.(*kiroProvider).parseLegacySessionContext(t.Context(), path, "local")
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.NotEmpty(t, msgs)
	assert.Empty(t, sess.FirstMessage)
	assert.Zero(t, sess.UserMessageCount)
	for _, msg := range msgs {
		assert.True(t, msg.IsSystem)
	}
}
