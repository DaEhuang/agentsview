package db

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
)

// dialogueMessages keeps bodies only for real prompts and final responses.
// Accounting rows retain ordinals and usage even when their text is excluded.
// Explicit phases are authoritative. For older sources without phases, the
// last assistant row before the next real prompt is the reply candidate; a
// tool-calling candidate is not a final response. Narrowed archives disable
// append checkpoints, so subsequent source changes reproject complete turns.
func dialogueMessages(messages []Message) []Message {
	stored := slices.Clone(messages)
	lastAssistant := make(map[string]bool)
	for i := len(stored) - 1; i >= 0; i-- {
		m := &stored[i]
		user := m.Role == "user" && !m.IsSystem && !isToolOutputRow(*m) && m.Content != ""
		keep := user
		if user {
			delete(lastAssistant, m.SessionID)
		}
		if m.Role == "assistant" && !m.IsSystem {
			keep = !m.IsSystem && !m.HasToolUse && len(m.ToolCalls) == 0 &&
				(m.SourceSubtype == "final_answer" || (m.SourceSubtype == "" && !lastAssistant[m.SessionID]))
			lastAssistant[m.SessionID] = true
		}
		if !keep {
			m.Content = ""
		}
		m.ContentLength = len(m.Content)
		m.ThinkingText = ""
		m.HasThinking = false
		m.ToolCalls = usageOnlyToolCalls(m.ToolCalls)
		m.ToolResults = nil
		m.HasToolUse = len(m.ToolCalls) > 0
	}
	return stored
}

// Copied orphans have no source to reparse. Apply the same turn boundaries and
// phase rules to their stored rows before publishing the replacement archive.
func compactCopiedSessionsForDialogueTx(ctx context.Context, tx *sql.Tx, table string) error {
	if err := dropCopiedToolContentTx(ctx, tx, table); err != nil {
		return err
	}
	inCopied := ` IN (SELECT id FROM ` + table + `)`
	statements := []string{
		`UPDATE messages AS m SET content='',content_length=0
   WHERE m.session_id` + inCopied + ` AND NOT (
    (m.role='user' AND m.content!='' AND m.is_system=0 AND COALESCE(m.source_subtype,'')!='tool_result') OR
    (m.role='assistant' AND m.is_system=0 AND m.has_tool_use=0 AND
     (m.source_subtype='final_answer' OR
      (COALESCE(m.source_subtype,'')='' AND NOT EXISTS (
       SELECT 1 FROM messages AS a WHERE a.session_id=m.session_id
       AND a.ordinal>m.ordinal AND a.role='assistant' AND a.is_system=0 AND NOT EXISTS (
        SELECT 1 FROM messages AS u WHERE u.session_id=m.session_id
        AND u.ordinal>m.ordinal AND u.ordinal<a.ordinal AND u.role='user'
        AND u.is_system=0 AND u.content!='' AND COALESCE(u.source_subtype,'')!='tool_result'
       )
      )))))`,
		`UPDATE messages SET thinking_text='',has_thinking=0 WHERE session_id` + inCopied,
		`DELETE FROM tool_result_events WHERE session_id` + inCopied,
		`DELETE FROM tool_calls WHERE session_id` + inCopied + `
   AND NOT (COALESCE(subagent_session_id,'')!='' OR category='Task' OR tool_name LIKE '%subagent%')`,
		`UPDATE tool_calls SET tool_name='subagent',category='Task',input_json=NULL,
    result_content=NULL,result_content_length=NULL,skill_name=NULL,file_path=NULL WHERE session_id` + inCopied,
		`UPDATE messages SET has_tool_use=EXISTS (SELECT 1 FROM tool_calls tc WHERE tc.message_id=messages.id) WHERE session_id` + inCopied,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("project copied dialogue: %w", err)
		}
	}
	return nil
}
