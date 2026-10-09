package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/testjsonl"
)

func TestCodexEmptyFinalRetainsItsOwnUsage(t *testing.T) {
	content := testjsonl.JoinJSONL(
		testjsonl.CodexSessionMetaJSON("empty-final", "/fixture", "user", tsEarly),
		testjsonl.CodexTurnContextJSON("gpt-5.4", tsEarlyS1),
		testjsonl.CodexMsgJSON("user", "run the check", tsEarlyS1),
		testjsonl.CodexFunctionCallJSON("exec_command", "check", tsEarlyS5),
		testjsonl.CodexTokenCountJSON(tsEarlyS5, 10000, 300, 6000),
		`{"type":"response_item","timestamp":"2024-01-01T12:00:00Z","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":""}]}}`,
		testjsonl.CodexTokenCountJSON(tsLate, 47180, 42, 39040),
		testjsonl.CodexTokenCountJSON(tsLate, 47180, 42, 39040),
	)
	session, messages := runCodexParserTest(t, "test.jsonl", content, false)
	require.Len(t, messages, 3)
	assert.Equal(t, 300, messages[1].OutputTokens)
	assert.Empty(t, messages[2].Content)
	assert.Equal(t, "final_answer", messages[2].SourceSubtype)
	assert.JSONEq(t, `{"input_tokens":8140,"output_tokens":42,"cache_read_input_tokens":39040}`, string(messages[2].TokenUsage))
	assert.Equal(t, 342, session.TotalOutputTokens)
	assert.Equal(t, 47180, session.PeakContextTokens)
}
