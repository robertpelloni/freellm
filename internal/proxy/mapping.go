package proxy

import (
	"encoding/json"
)

func TransformRequestBody(provider string, openaiBody []byte) ([]byte, error) {
	switch provider {
	case "anthropic":
		return transformToAnthropic(openaiBody)
	case "gemini":
		return transformToGemini(openaiBody)
	default:
		return openaiBody, nil
	}
}

func transformToAnthropic(body []byte) ([]byte, error) {
	var input struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Stream    bool `json:"stream"`
		MaxTokens int  `json:"max_tokens"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, err
	}

	if input.MaxTokens == 0 {
		input.MaxTokens = 4096
	}

	output := map[string]interface{}{
		"model":      input.Model,
		"messages":   input.Messages,
		"stream":     input.Stream,
		"max_tokens": input.MaxTokens,
	}
	return json.Marshal(output)
}

func transformToGemini(body []byte) ([]byte, error) {
	var input struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, err
	}

	type GeminiPart struct {
		Text string `json:"text"`
	}
	type GeminiContent struct {
		Role  string       `json:"role"`
		Parts []GeminiPart `json:"parts"`
	}

	contents := make([]GeminiContent, 0)
	for _, m := range input.Messages {
		role := m.Role
		if role == "assistant" { role = "model" }
		contents = append(contents, GeminiContent{
			Role:  role,
			Parts: []GeminiPart{{Text: m.Content}},
		})
	}

	output := map[string]interface{}{
		"contents": contents,
	}
	return json.Marshal(output)
}
