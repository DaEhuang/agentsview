package parser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/tidwall/gjson"
)

func crewDigest(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}

func readCrewRows(ctx context.Context, path string, visit func(gjson.Result) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	lr := newLineReader(f, maxLineSize)
	defer releaseLineReader(lr)
	var pending string
	for {
		line, ok := lr.next()
		if !ok {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if pending != "" {
			if !gjson.Valid(pending) {
				return fmt.Errorf("invalid interior Crew record")
			}
			if err := visit(gjson.Parse(pending)); err != nil {
				return err
			}
		}
		pending = line
	}
	if err := lr.Err(); err != nil {
		return err
	}
	// A writer can leave an unfinished final row. It is reconsidered on the
	// next file change; malformed interior rows never become silent omissions.
	if pending != "" && gjson.Valid(pending) {
		return visit(gjson.Parse(pending))
	}
	return nil
}

func parseCrewDialogue(ctx context.Context, files []string, stem, machine string) (ParseResult, error) {
	result := ParseResult{Session: ParsedSession{ID: "kiro-crew:dialogue:" + stem,
		Agent: AgentKiroCrew, AgentLabel: "kiro-crew", Machine: machine, Project: "unknown"}}
	seen := map[string]int{}
	for _, path := range files {
		occurrences := map[string]int{}
		err := readCrewRows(ctx, path, func(row gjson.Result) error {
			if row.Get("_type").Str == "metadata" {
				if cwd := row.Get("execution_context.cwd").Str; cwd != "" {
					result.Session.Cwd = cwd
				}
				if project := row.Get("project").Str; project != "" {
					result.Session.Project = project
				}
				return nil
			}
			role, text := row.Get("role").Str, row.Get("content").Str
			if (role != "user" && role != "assistant") || text == "" || row.Get("meta.kind").Str == "compaction" {
				return nil
			}
			timestamp, err := time.Parse(time.RFC3339Nano, row.Get("ts").Str)
			if err != nil {
				return fmt.Errorf("invalid Crew conversation timestamp")
			}
			identity := row.Get("meta.mid").Str
			if identity == "" {
				base := crewDigest(role + "\x00" + row.Get("ts").Str + "\x00" + text)
				occurrences[base]++
				identity = base + ":" + strconv.Itoa(occurrences[base])
			}
			msg := ParsedMessage{Role: RoleType(role), Content: text, ContentLength: len(text), Timestamp: timestamp,
				SourceUUID: result.Session.ID + ":" + identity, SourceType: "crew_transcript"}
			if role == "assistant" {
				// Crew attaches turn_stats only to a completed response. An
				// unmarked last row can still be streaming or interrupted.
				msg.SourceSubtype = "commentary"
				if row.Get("meta.turn_stats").IsObject() {
					msg.SourceSubtype = "final_answer"
				}
			}
			if index, exists := seen[identity]; exists {
				msg.Ordinal = index
				result.Messages[index] = msg
			} else {
				msg.Ordinal = len(result.Messages)
				seen[identity] = msg.Ordinal
				result.Messages = append(result.Messages, msg)
			}
			return nil
		})
		if err != nil {
			return ParseResult{}, err
		}
	}
	for _, msg := range result.Messages {
		if result.Session.StartedAt.IsZero() || msg.Timestamp.Before(result.Session.StartedAt) {
			result.Session.StartedAt = msg.Timestamp
		}
		if msg.Timestamp.After(result.Session.EndedAt) {
			result.Session.EndedAt = msg.Timestamp
		}
		if msg.Role == RoleUser {
			result.Session.UserMessageCount++
			if result.Session.FirstMessage == "" {
				result.Session.FirstMessage = truncate(msg.Content, 300)
			}
		}
	}
	result.Session.MessageCount = len(result.Messages)
	return result, nil
}

func parseCrewBilling(ctx context.Context, path, shard, machine string) (ParseResult, error) {
	result := ParseResult{Session: ParsedSession{ID: "kiro-crew:usage:" + shard,
		Agent: AgentKiroCrew, AgentLabel: "kiro-crew", Machine: machine, Project: "usage"}}
	seen := map[string]bool{}
	err := readCrewRows(ctx, path, func(row gjson.Result) error {
		if row.Get("_type").Str != "tokens" {
			return nil
		}
		// Credits are emitted once per completed turn across every surface and
		// phase, including session_start. They are not cumulative snapshots.
		credits := row.Get("credits")
		if credits.Type != gjson.Number {
			return fmt.Errorf("invalid Crew credits")
		}
		if row.Get("provider").Str != "acp" {
			return fmt.Errorf("Crew billing provider is not supported")
		}
		var meta kiroMeta
		meta.SessionState.AgentName = "kirocrew"
		turn := kiroCreditTurn{EndTimestamp: row.Get("ts").Str}
		turn.Metering = append(turn.Metering, struct {
			Unit  string  `json:"unit"`
			Value float64 `json:"value"`
		}{Unit: "credit", Value: credits.Float()})
		meta.SessionState.ConversationMetadata.Turns = []kiroCreditTurn{turn}
		messages, events, err := kiroCreditAccounting(result.Session.ID, len(result.Messages), &meta)
		if err != nil {
			return err
		}
		key := crewDigest(row.Get("ts").Str + "\x00" + row.Get("slot").Str + "\x00" + row.Get("surface").Str + "\x00" + row.Get("phase").Str + "\x00" + credits.Raw)
		if seen[key] {
			return nil
		}
		seen[key] = true
		for i := range messages {
			messages[i].SourceUUID = result.Session.ID + ":" + key
			if result.Session.StartedAt.IsZero() || messages[i].Timestamp.Before(result.Session.StartedAt) {
				result.Session.StartedAt = messages[i].Timestamp
			}
			if messages[i].Timestamp.After(result.Session.EndedAt) {
				result.Session.EndedAt = messages[i].Timestamp
			}
		}
		for i := range events {
			events[i].DedupKey = key
		}
		result.Messages = append(result.Messages, messages...)
		result.UsageEvents = append(result.UsageEvents, events...)
		return nil
	})
	result.Session.MessageCount = len(result.Messages)
	return result, err
}
