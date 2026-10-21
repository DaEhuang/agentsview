package parser

import (
	"encoding/json/v2"
	"path/filepath"
	"strings"
)

func qoderCNPath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if strings.EqualFold(part, ".qoder-cn") {
			return true
		}
	}
	return false
}

// QoderCN reports cached input as a subset of input_tokens, unlike Claude.
// Keep its native fields but normalize the uncached bucket exactly once.
func normalizeQoderCNCachedInput(result *ParseResult) {
	result.Session.AgentLabel = "qoder-cn"
	for i := range result.Messages {
		msg := &result.Messages[i]
		if len(msg.TokenUsage) == 0 {
			continue
		}
		var usage map[string]any
		if json.Unmarshal(msg.TokenUsage, &usage) != nil {
			continue
		}
		input, inputOK := usage["input_tokens"].(float64)
		cached, cacheOK := usage["cache_read_input_tokens"].(float64)
		if !inputOK || !cacheOK || input < 0 || cached < 0 {
			continue
		}
		usage["input_tokens"] = max(input-cached, 0)
		normalized, err := json.Marshal(usage)
		if err != nil {
			continue
		}
		msg.TokenUsage = normalized
		if msg.HasContextTokens {
			msg.ContextTokens = max(msg.ContextTokens-int(cached), 0)
		}
	}
	// Recompute derived totals after normalization, rather than retaining the
	// Claude parser's inclusive-cache context maximum.
	result.Session.TotalOutputTokens = 0
	result.Session.PeakContextTokens = 0
	accumulateMessageTokenUsage(&result.Session, result.Messages)
}
