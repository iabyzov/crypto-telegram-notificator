package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/iabyzov/coinmarketcap-telegram-bot/internal/domain/alerts"
	openai "github.com/sashabaranov/go-openai"
)

// AlertIntent is the structured result of LLM-based intent extraction from
// a natural-language alert request.
type AlertIntent struct {
	Symbol      string
	TargetPrice float64
	Type        alerts.AlertType
	Confidence  float64
	Explanation string
}

// LLMClient abstracts LLM calls so providers (OpenAI, OpenWebUI, OpenRouter,
// Groq, Ollama, mock) can be swapped without touching handlers. Any
// OpenAI-compatible endpoint works because they all speak the /v1/chat/completions
// protocol.
type LLMClient interface {
	ParseAlertIntent(ctx context.Context, text string) (*AlertIntent, error)
}

// alertIntentSystemPrompt instructs the model to return strict JSON describing
// the user's alert intent.
//
// Why this matters (the real "LLM engineering" part of this feature):
//   - Strict JSON output means we can parse it deterministically instead of
//     regex-scraping prose. We enable the JSON response format on the request
//     to enforce this.
//   - Temperature 0 makes output reproducible — for extracting a fixed shape
//     of data we want the model to be "greedy", not creative.
//   - The direction rules resolve the trickiest ambiguity: "drops below" vs
//     "rises above". Without these the model often confuses the two.
//   - Confidence lets the handler reject low-certainty parses instead of
//     creating bogus alerts from typos.
const alertIntentSystemPrompt = `You extract cryptocurrency price-alert intents from natural language.

Return ONLY a JSON object (no prose, no markdown fences) with this schema:
{
  "symbol": "BTC",            // CoinMarketCap ticker symbol, UPPERCASE
  "target_price": 80000.0,    // numeric target price in USD
  "direction": "below",       // "above" or "below"
  "confidence": 0.95,         // 0.0..1.0 — how sure you are
  "explanation": "..."        // one short sentence restating the intent
}

Direction rules:
  "drops", "falls", "decreases", "below", "under", "less than", "dip", "crash" -> "below"
  "rises", "exceeds", "above", "over", "more than", "reaches", "pump", "moon"  -> "above"
"above" means: alert me when the price goes UP to or above the target.
"below" means: alert me when the price goes DOWN to or below the target.

Map coin names to tickers: bitcoin->BTC, ethereum->ETH, solana->SOL, etc.
If any field cannot be determined, set confidence to 0 and explain in "explanation".`

// rawAlertIntent is the JSON wire shape returned by the model. Keeping it
// separate from AlertIntent lets us validate/normalize before exposing a
// clean domain-friendly struct to callers.
type rawAlertIntent struct {
	Symbol      string  `json:"symbol"`
	TargetPrice float64 `json:"target_price"`
	Direction   string  `json:"direction"`
	Confidence  float64 `json:"confidence"`
	Explanation string  `json:"explanation"`
}

// OpenAIClient implements LLMClient using OpenAI chat completions with JSON
// response format. It works against any OpenAI-compatible endpoint.
type OpenAIClient struct {
	client *openai.Client
	model  string
}

// NewOpenAIClient creates an OpenAI-compatible LLMClient.
//
// baseURL lets you point the client at any OpenAI-compatible endpoint — your
// OpenWebUI instance (e.g. "https://your-host/v1"), OpenRouter, Groq, or
// Ollama — instead of the public OpenAI API. When empty, the default OpenAI
// base URL is used.
//
// model selects the chat model; pass "" to default to gpt-4o-mini. You will
// typically set this via the LLM_MODEL env var to match a model available on
// your endpoint.
func NewOpenAIClient(apiKey, baseURL, model string) *OpenAIClient {
	if model == "" {
		model = openai.GPT4oMini
	}
	cfg := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		cfg.BaseURL = baseURL
	}
	return &OpenAIClient{
		client: openai.NewClientWithConfig(cfg),
		model:  model,
	}
}

// ParseAlertIntent sends the user's text to the LLM and decodes the returned
// JSON into a validated AlertIntent.
func (c *OpenAIClient) ParseAlertIntent(ctx context.Context, text string) (*AlertIntent, error) {
	resp, err := c.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: c.model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: alertIntentSystemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: text},
		},
		// Force the model to emit valid JSON (supported by most OpenAI-compatible
		// endpoints; if your endpoint ignores this, the json.Unmarshal below will
		// still catch non-JSON output with a clear error).
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		},
		Temperature: 0,
	})
	if err != nil {
		return nil, fmt.Errorf("llm request: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("llm returned no choices")
	}
	content := resp.Choices[0].Message.Content

	var raw rawAlertIntent
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil, fmt.Errorf("parse llm json: %w (raw: %s)", err, content)
	}

	// Normalize the model output into the domain types.
	intent := &AlertIntent{
		Symbol:      strings.ToUpper(strings.TrimSpace(raw.Symbol)),
		TargetPrice: raw.TargetPrice,
		Confidence:  raw.Confidence,
		Explanation: strings.TrimSpace(raw.Explanation),
	}
	switch strings.ToLower(strings.TrimSpace(raw.Direction)) {
	case "above":
		intent.Type = alerts.More
	case "below":
		intent.Type = alerts.Less
	default:
		return nil, fmt.Errorf("unknown direction %q (raw: %s)", raw.Direction, content)
	}
	return intent, nil
}