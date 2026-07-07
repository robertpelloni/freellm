package transform

import (
    "encoding/json"
    "fmt"
    "strings"
    "time"
)

// ---------------------------------------------------------------------------
// OpenAI‑compatible response structs (a minimal subset sufficient for the UI)
// ---------------------------------------------------------------------------

type ChatMessage struct {
    Role    string `json:"role,omitempty"`
    Content string `json:"content,omitempty"`
}

type Choice struct {
    Index        int          `json:"index"`
    Message       *ChatMessage `json:"message,omitempty"`
    Delta         *ChatMessage `json:"delta,omitempty"`
    FinishReason string       `json:"finish_reason,omitempty"`
    Logprobs     interface{}  `json:"logprobs,omitempty"`
}

type Usage struct {
    PromptTokens     int `json:"prompt_tokens,omitempty"`
    CompletionTokens int `json:"completion_tokens,omitempty"`
    TotalTokens       int `json:"total_tokens,omitempty"`
}

type ChatCompletionResponse struct {
    ID      string   `json:"id"`
    Object  string   `json:"object"`
    Created int64    `json:"created"`
    Model   string   `json:"model"`
    Choices []Choice `json:"choices"`
    Usage   *Usage   `json:"usage,omitempty"`
}

// ---------------------------------------------------------------------------
// Helper: low‑level JSON unmarshal into a generic map (used by the provider parsers)
// ---------------------------------------------------------------------------
func toMap(data []byte) (map[string]interface{}, error) {
    var m map[string]interface{}
    if err := json.Unmarshal(data, &m); err != nil {
        return nil, err
    }
    return m, nil
}

// ---------------------------------------------------------------------------
// Provider‑specific mapping functions – three concrete examples
// ---------------------------------------------------------------------------

// OpenAI – the response already matches the struct, just unmarshal.
func FromOpenAI(body []byte) (*ChatCompletionResponse, error) {
    var resp ChatCompletionResponse
    if err := json.Unmarshal(body, &resp); err != nil {
        return nil, err
    }
    return &resp, nil
}

// Anthropic – transforms the Anthropic format into the OpenAI struct.
// Anthropic returns {"id":"...","model":"...","content":[{"text":"..."}],"type":"..."}
func FromAnthropic(body []byte) (*ChatCompletionResponse, error) {
    var src struct {
        ID      string `json:"id"`
        Model   string `json:"model"`
        Content []struct {
            Text string `json:"text"`
        } `json:"content"`
        StopReason string `json:"stop_reason"`
    }
    if err := json.Unmarshal(body, &src); err != nil {
        return nil, err
    }
    text := ""
    if len(src.Content) > 0 {
        text = src.Content[0].Text
    }
    finishReason := "stop"
    switch src.StopReason {
    case "", "end_turn", "stop_sequence":
        finishReason = "stop"
    case "max_tokens":
        finishReason = "length"
    case "tool_use":
        finishReason = "tool_calls"
    default:
        finishReason = src.StopReason
    }

    resp := &ChatCompletionResponse{
        ID:      src.ID,
        Object:  "chat.completion",
        Created: time.Now().Unix(),
        Model:   src.Model,
        Choices: []Choice{{
            Index: 0,
            Message: &ChatMessage{Role: "assistant", Content: text},
            FinishReason: finishReason,
        }},
    }
    return resp, nil
}

// Mistral – Mistral returns a very similar shape to OpenAI, but the
// top‑level key for the model is "model" and the content lives under
// choices[0].message.content.  We reuse the generic map to keep the code tiny.
func FromMistral(body []byte) (*ChatCompletionResponse, error) {
    // Re‑use the generic map because Mistral's JSON is already OpenAI‑compatible.
    // We just make sure the fields exist and fill defaults when missing.
    m, err := toMap(body)
    if err != nil {
        return nil, err
    }
    // Marshal back into the concrete struct – any missing fields stay zero‑value.
    data, _ := json.Marshal(m)
    return FromOpenAI(data)
}

// ---------------------------------------------------------------------------
// Dispatcher – selects the right parser based on the provider name.
// ---------------------------------------------------------------------------
func Parse(provider string, body []byte) (*ChatCompletionResponse, error) {
    // Normalise provider name (the Gateway stores values like "anthropic",
    // "openai", "mistral", etc.)
    p := strings.ToLower(provider)
    switch p {
    case "openai":
        return FromOpenAI(body)
    case "anthropic":
        return FromAnthropic(body)
    case "mistral", "mistralai", "mistral-small", "mistral-medium":
        return FromMistral(body)
    default:
        // Fallback – try direct unmarshal; if it fails we bubble the error up.
        return FromOpenAI(body)
    }
}

// ---------------------------------------------------------------------------
// Convenience: marshal the final struct back to JSON for the HTTP response.
// ---------------------------------------------------------------------------
func Marshal(resp *ChatCompletionResponse) ([]byte, error) {
    if resp == nil {
        return nil, fmt.Errorf("nil response")
    }
    return json.Marshal(resp)
}
