package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/kraftbj/mailreaper/internal/config"
)

// LLMResponse holds the parsed result from an LLM call.
type LLMResponse struct {
	IsTimeSensitive bool    `json:"isTimeSensitive"`
	ExpiresAt       string  `json:"expiresAt"`
	Reason          string  `json:"reason"`
	Confidence      float64 `json:"confidence"`
	Matches         bool    `json:"matches"`
}

var mdFenceRe = regexp.MustCompile("(?s)^```[a-zA-Z]*\\n?(.*?)\\n?```$")

// ParseJSONResponse strips optional markdown code fences and unmarshals the
// LLM JSON output. It normalises snake_case alternate key names and clamps
// Confidence to [0, 1].
func ParseJSONResponse(text string) (*LLMResponse, error) {
	text = strings.TrimSpace(text)

	// Strip markdown code fences if present.
	if m := mdFenceRe.FindStringSubmatch(text); len(m) == 2 {
		text = strings.TrimSpace(m[1])
	}

	// Unmarshal into a generic map first so we can handle alternate key names.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, fmt.Errorf("parsing LLM JSON: %w", err)
	}

	resp := &LLMResponse{}

	// isTimeSensitive / is_time_sensitive
	if v, ok := firstRaw(raw, "isTimeSensitive", "is_time_sensitive"); ok {
		_ = json.Unmarshal(v, &resp.IsTimeSensitive)
	}

	// expiresAt / expires_at — allow JSON null
	if v, ok := firstRaw(raw, "expiresAt", "expires_at"); ok {
		var s *string
		if err := json.Unmarshal(v, &s); err == nil && s != nil {
			resp.ExpiresAt = *s
		}
	}

	// reason / explanation
	if v, ok := firstRaw(raw, "reason", "explanation"); ok {
		_ = json.Unmarshal(v, &resp.Reason)
	}

	// confidence / score
	if v, ok := firstRaw(raw, "confidence", "score"); ok {
		_ = json.Unmarshal(v, &resp.Confidence)
	}

	// matches
	if v, ok := firstRaw(raw, "matches"); ok {
		_ = json.Unmarshal(v, &resp.Matches)
	}

	// Clamp confidence.
	if resp.Confidence < 0 {
		resp.Confidence = 0
	}
	if resp.Confidence > 1 {
		resp.Confidence = 1
	}

	return resp, nil
}

// firstRaw returns the first raw JSON value found under any of the provided keys.
func firstRaw(m map[string]json.RawMessage, keys ...string) (json.RawMessage, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v, true
		}
	}
	return nil, false
}

// CallLLM dispatches to the configured provider and returns a parsed LLMResponse.
func CallLLM(ctx context.Context, cfg *config.LLMConfig, systemPrompt, userContent string) (*LLMResponse, error) {
	switch strings.ToLower(cfg.Provider) {
	case "gemini":
		return callGemini(ctx, &cfg.Gemini, systemPrompt, userContent)
	case "ollama":
		return callOllama(ctx, &cfg.Ollama, systemPrompt, userContent)
	default:
		return nil, fmt.Errorf("unknown LLM provider %q", cfg.Provider)
	}
}

// --- Gemini ---

type geminiRequest struct {
	SystemInstruction geminiContent    `json:"systemInstruction"`
	Contents          []geminiContent  `json:"contents"`
	GenerationConfig  geminiGenConfig  `json:"generationConfig"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenConfig struct {
	Temperature      float64 `json:"temperature"`
	MaxOutputTokens  int     `json:"maxOutputTokens"`
	ResponseMimeType string  `json:"responseMimeType"`
}

type geminiResponse struct {
	Candidates []struct {
		Content geminiContent `json:"content"`
	} `json:"candidates"`
}

func callGemini(ctx context.Context, cfg *config.GeminiConfig, systemPrompt, userContent string) (*LLMResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	model := cfg.Model
	if model == "" {
		model = "gemini-pro"
	}
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", model)

	body := geminiRequest{
		SystemInstruction: geminiContent{Parts: []geminiPart{{Text: systemPrompt}}},
		Contents:          []geminiContent{{Parts: []geminiPart{{Text: userContent}}}},
		GenerationConfig: geminiGenConfig{
			Temperature:      0.1,
			MaxOutputTokens:  512,
			ResponseMimeType: "application/json",
		},
	}

	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshalling Gemini request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("creating Gemini request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", cfg.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling Gemini: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Gemini returned HTTP %d: %s", resp.StatusCode, string(raw))
	}

	var gr geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return nil, fmt.Errorf("decoding Gemini response: %w", err)
	}
	if len(gr.Candidates) == 0 || len(gr.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("Gemini returned empty candidates")
	}

	text := gr.Candidates[0].Content.Parts[0].Text
	return ParseJSONResponse(text)
}

// --- Ollama ---

type ollamaRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Format   string          `json:"format"`
	Options  ollamaOptions   `json:"options"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature"`
	NumPredict  int     `json:"num_predict"`
}

type ollamaResponse struct {
	Message ollamaMessage `json:"message"`
}

func callOllama(ctx context.Context, cfg *config.OllamaConfig, systemPrompt, userContent string) (*LLMResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	url := endpoint + "/api/chat"

	body := ollamaRequest{
		Model: cfg.Model,
		Messages: []ollamaMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userContent},
		},
		Stream:  false,
		Format:  "json",
		Options: ollamaOptions{Temperature: 0.1, NumPredict: 512},
	}

	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshalling Ollama request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("creating Ollama request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling Ollama: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Ollama returned HTTP %d: %s", resp.StatusCode, string(raw))
	}

	var or ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&or); err != nil {
		return nil, fmt.Errorf("decoding Ollama response: %w", err)
	}

	return ParseJSONResponse(or.Message.Content)
}
