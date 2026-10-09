package parser

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/testjsonl"
)

func TestCodexEqualRequestsAdvanceCumulativeUsage(t *testing.T) {
	event := func(total int) string {
		return fmt.Sprintf(`{"timestamp":"2026-06-01T12:00:00Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":80,"output_tokens":20},"total_token_usage":{"input_tokens":%d,"output_tokens":%d}}}}`, total*100, total*20)
	}
	content := testjsonl.JoinJSONL(
		testjsonl.CodexSessionMetaJSON("equal-requests", "/tmp", "user", tsEarly),
		testjsonl.CodexTurnContextJSON("gpt-5.4", tsEarlyS1),
		testjsonl.CodexMsgJSON("user", "first", tsEarlyS1),
		testjsonl.CodexMsgJSON("assistant", "one", tsEarlyS5), event(1), event(1),
		testjsonl.CodexMsgJSON("user", "second", tsLate),
		testjsonl.CodexMsgJSON("assistant", "two", tsLateS5), event(2), event(2),
	)
	sess, msgs := runCodexParserTest(t, "equal.jsonl", content, false)
	require.Len(t, msgs, 4)
	assert.NotEmpty(t, msgs[1].TokenUsage)
	assert.NotEmpty(t, msgs[3].TokenUsage)
	assert.Equal(t, 40, sess.TotalOutputTokens)
}

func TestCodexPreservesResponsePhase(t *testing.T) {
	content := testjsonl.JoinJSONL(
		testjsonl.CodexSessionMetaJSON("phase", "/tmp", "user", tsEarly),
		`{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"working"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"done"}]}}`,
	)
	_, msgs := runCodexParserTest(t, "phase.jsonl", content, false)
	require.Len(t, msgs, 2)
	assert.Equal(t, "commentary", msgs[0].SourceSubtype)
	assert.Equal(t, "final_answer", msgs[1].SourceSubtype)
}

func TestQoderCNCachedInputIsNotCountedTwice(t *testing.T) {
	for _, dir := range []string{".qoder-cn", ".qoder"} {
		t.Run(dir, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), dir, "projects", "example", "11111111-1111-4111-8111-111111111111.jsonl")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, []byte(`{"type":"assistant","uuid":"a1","timestamp":"2026-06-01T12:00:00Z","sessionId":"11111111-1111-4111-8111-111111111111","message":{"id":"m1","role":"assistant","model":"auto","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":100,"cache_read_input_tokens":80,"output_tokens":20}}}`+"\n"), 0o600))
			results, err := ParseQoderSession(path, "example", "local")
			require.NoError(t, err)
			require.Len(t, results, 1)
			require.Len(t, results[0].Messages, 1)
			var usage map[string]int
			require.NoError(t, json.Unmarshal(results[0].Messages[0].TokenUsage, &usage))
			wantInput := 100
			if dir == ".qoder-cn" {
				wantInput = 20
				assert.Equal(t, "qoder-cn", results[0].Session.AgentLabel)
			}
			assert.Equal(t, wantInput, usage["input_tokens"])
			assert.Equal(t, 80, usage["cache_read_input_tokens"])
			assert.Equal(t, 20, usage["output_tokens"])
		})
	}
}
