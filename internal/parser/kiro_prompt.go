package parser

import "strings"

// Crew's assembler emits this boundary after its injected context and
// neutralizes copies inside the request. Restrict stripping to recognizable
// assembled envelopes so ordinary prompts quoting a marker remain unchanged.
func preprocessKiroPrompt(content string) string {
	const boundary = "[CURRENT USER REQUEST — respond to this]\n"
	framed := false
	for _, prefix := range []string{"[AGENT SYSTEM PROMPT]", "[SESSION CONTEXT", "[CRITICAL RULES", "[RUNTIME]"} {
		if strings.HasPrefix(content, prefix) {
			framed = true
			break
		}
	}
	if !framed {
		return content
	}
	at := strings.LastIndex(content, boundary)
	if at < 0 {
		return content
	}
	return strings.TrimSpace(content[at+len(boundary):])
}
