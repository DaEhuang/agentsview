package sync

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/stringutil"
)

// Read only the session being changed. During a rebuild, include the original
// archive as well as any earlier write to the replacement. Missing source
// segments never imply deletion of persistent dialogue.
func (e *Engine) retainNativeArchiveMessages(ctx context.Context, session *db.Session, incoming []db.Message) ([]db.Message, error) {
	stores := []db.Store{}
	if e.archiveStore != nil {
		stores = append(stores, e.archiveStore)
	}
	stores = append(stores, e.db)
	var merged []db.Message
	for _, store := range stores {
		stored, err := store.GetSessionFull(ctx, session.ID)
		if err != nil {
			return nil, err
		}
		if stored == nil {
			continue
		}
		messages, err := store.GetAllMessages(ctx, session.ID)
		if err != nil {
			return nil, err
		}
		merged, err = mergeNativeArchiveMessages(ctx, merged, messages)
		if err != nil {
			return nil, err
		}
		session.StartedAt = earlierSessionTime(stored.StartedAt, session.StartedAt)
		session.EndedAt = laterSessionTime(stored.EndedAt, session.EndedAt)
	}
	merged, err := mergeNativeArchiveMessages(ctx, merged, incoming)
	if err != nil {
		return nil, err
	}
	for _, message := range merged {
		if message.Role == "user" {
			first := stringutil.SafeTruncate(message.Content, 300)
			session.FirstMessage = &first
			break
		}
	}
	return merged, nil
}

func mergeNativeArchiveMessages(ctx context.Context, stored, incoming []db.Message) ([]db.Message, error) {
	merged := append([]db.Message(nil), stored...)
	indexes := make(map[string]int, len(stored)+len(incoming))
	for i, message := range merged {
		if message.SourceUUID == "" {
			return nil, fmt.Errorf("rotating transcript has no native message identity")
		}
		if _, exists := indexes[message.SourceUUID]; exists {
			return nil, fmt.Errorf("rotating transcript has duplicate native message identities")
		}
		indexes[message.SourceUUID] = i
	}
	seen := make(map[string]bool, len(incoming))
	for _, message := range incoming {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if message.SourceUUID == "" || seen[message.SourceUUID] {
			return nil, fmt.Errorf("rotating transcript has missing or duplicate native message identities")
		}
		seen[message.SourceUUID] = true
		if i, exists := indexes[message.SourceUUID]; exists {
			old := merged[i]
			if old.Role != message.Role {
				return nil, fmt.Errorf("rotating transcript changed a native message role")
			}
			// A stale overlapping segment cannot replace a completed response
			// with its earlier streaming state. Completed corrections still apply.
			if old.SourceSubtype == "final_answer" && message.SourceSubtype == "commentary" {
				continue
			}
			merged[i] = message
		} else {
			indexes[message.SourceUUID] = len(merged)
			merged = append(merged, message)
		}
	}
	stamps := make(map[string]time.Time, len(merged))
	for _, message := range merged {
		stamp, err := time.Parse(time.RFC3339Nano, message.Timestamp)
		if err != nil {
			return nil, fmt.Errorf("rotating transcript has invalid message timestamp")
		}
		stamps[message.SourceUUID] = stamp
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return stamps[merged[i].SourceUUID].Before(stamps[merged[j].SourceUUID])
	})
	for i := range merged {
		merged[i].Ordinal = i
	}
	return merged, nil
}
