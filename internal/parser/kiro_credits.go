package parser

import (
	"encoding/json/v2"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/agentsview/internal/money"
)

// Credits are the native measurement. The separate usage event is an explicit
// cost-equivalent estimate, never a claim of provider-reported token counts.
const kiroCreditEstimateModel = "kiro-credit-equivalent-v1"

type kiroCreditState struct {
	AgentName            string `json:"agent_name"`
	ConversationMetadata struct {
		Turns []kiroCreditTurn `json:"user_turn_metadatas"`
	} `json:"conversation_metadata"`
}
type kiroCreditTurn struct {
	EndTimestamp string `json:"end_timestamp"`
	Metering     []struct {
		Unit  string  `json:"unit"`
		Value float64 `json:"value"`
	} `json:"metering_usage"`
}

func kiroCreditHarness(meta *kiroMeta) string {
	if meta != nil && (meta.SessionState.AgentName == "kirocrew" || strings.HasPrefix(meta.SessionState.AgentName, "kirocrew-")) {
		return "kiro-crew"
	}
	return "kiro-cli"
}

func kiroCreditAccounting(sessionID string, ordinal int, meta *kiroMeta) ([]ParsedMessage, []ParsedUsageEvent, error) {
	if meta == nil {
		return nil, nil, nil
	}
	var messages []ParsedMessage
	var events []ParsedUsageEvent
	for i, turn := range meta.SessionState.ConversationMetadata.Turns {
		var credits float64
		for _, meter := range turn.Metering {
			if meter.Unit != "credit" {
				continue
			}
			if math.IsNaN(meter.Value) || math.IsInf(meter.Value, 0) || meter.Value < 0 {
				return nil, nil, fmt.Errorf("invalid credit meter in turn %d", i)
			}
			credits += meter.Value
		}
		if credits == 0 {
			continue
		}
		timestamp, err := time.Parse(time.RFC3339Nano, turn.EndTimestamp)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid credit timestamp in turn %d", i)
		}
		// $0.04 / credit; weighted price = .96*.20 + .035*4 + .005*20
		// = $0.432 per million tokens. Allocate the rounded total without drift.
		totalFloat := credits * 2_500_000 / 27
		if math.IsInf(totalFloat, 0) || totalFloat >= float64(math.MaxInt64) {
			return nil, nil, fmt.Errorf("credit estimate overflows in turn %d", i)
		}
		total := int64(math.Round(totalFloat))
		cached := int64(math.Round(float64(total) * .96))
		input := int64(math.Round(float64(total) * .035))
		output := total - cached - input
		cost := money.Money{Microdollars: int64(math.Round(credits * 40_000))}
		key := "credit-turn:" + strconv.Itoa(i)
		payload, err := json.Marshal(map[string]any{
			"measurement": "credit", "credits": credits, "source": kiroCreditHarness(meta),
			"estimate_policy": kiroCreditEstimateModel, "estimated_tokens": total,
			"estimated_input_tokens": input, "estimated_cache_read_tokens": cached, "estimated_output_tokens": output,
			"cost_microdollars": cost.Microdollars,
		})
		if err != nil {
			return nil, nil, err
		}
		messages = append(messages, ParsedMessage{Ordinal: ordinal + len(messages), Role: RoleSystem, IsSystem: true,
			SourceType: "metering", SourceSubtype: "metering_credit", SourceUUID: key, Timestamp: timestamp, TokenUsage: payload})
		events = append(events, ParsedUsageEvent{SessionID: sessionID, Source: "session", DedupKey: key,
			Model: kiroCreditEstimateModel, ProviderID: "kiro", InputTokens: int(input), OutputTokens: int(output),
			CacheReadInputTokens: int(cached), Cost: &cost, CostStatus: "estimated", CostSource: "kiro_credits_equivalent_v1",
			OccurredAt: timestamp.UTC().Format(time.RFC3339Nano)})
	}
	return messages, events, nil
}
