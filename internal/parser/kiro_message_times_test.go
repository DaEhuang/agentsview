package parser

import (
	"github.com/stretchr/testify/assert"
	"github.com/tidwall/gjson"
	"testing"
	"time"
)

func TestKiroMessageTimeUnitsAndIdentity(t *testing.T) {
	expected := time.Date(2026, 9, 30, 10, 50, 46, 0, time.UTC)
	for _, raw := range []string{`1790765446`, `1790765446000`, `"2026-09-30T10:50:46Z"`} {
		assert.Equal(t, expected, kiroCurrentMessageTimestamp(gjson.Parse(raw)))
	}
	meta := &kiroMeta{}
	meta.SessionState.ConversationMetadata.Turns = []kiroCreditTurn{
		{MessageIDs: []string{"prompt", "reply"}, EndTimestamp: "2026-09-30T10:50:49Z"},
		{MessageIDs: []string{"ambiguous"}, EndTimestamp: "2026-09-30T10:50:49Z"},
		{MessageIDs: []string{"ambiguous"}, EndTimestamp: "2026-09-30T10:51:49Z"},
	}
	messages := []ParsedMessage{
		{Role: RoleUser, SourceUUID: "prompt"},
		{Role: RoleAssistant, SourceUUID: "reply"},
		{Role: RoleAssistant, SourceUUID: "reply", Timestamp: expected},
		{Role: RoleAssistant, SourceUUID: "unknown"},
		{Role: RoleAssistant, SourceUUID: "ambiguous"},
	}
	restoreKiroReplyTimes(messages, meta)
	assert.True(t, messages[0].Timestamp.IsZero())
	assert.Equal(t, "2026-09-30T10:50:49Z", messages[1].Timestamp.Format(time.RFC3339))
	assert.Equal(t, expected, messages[2].Timestamp)
	assert.True(t, messages[3].Timestamp.IsZero())
	assert.True(t, messages[4].Timestamp.IsZero())
}
