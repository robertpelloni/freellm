package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"github.com/robertpelloni/freellm/internal/db"

)

// anthropicReq represents an Anthropic Messages API request.
type anthropicReq struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Messages  []anthropicMsg  `json:"messages"`
	System    json.RawMessage `json:"system,omitempty"` // string or array
	Stream    bool            `json:"stream,omitempty"`
	Tools     json.RawMessage `json:"tools,omitempty"`
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	TopP      *float64        `json:"top_p,omitempty"`
	Stop      json.RawMessage `json:"stop,omitempty"` // string or array
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

type anthropicMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string or array of content blocks
}

// anthropicResp represents a non-streaming Anthropic Messages API response.
type anthropicResp struct {
	ID           string               `json:"id"`
	Type         string               `json:"type"`
	Role         string               `json:"role"`
	Content      []anthropicContent   `json:"content"`
	Model        string               `json:"model"`
	StopReason   string               `json:"stop_reason"`
	StopSequence string               `json:"stop_sequence,omitempty"`
	Usage        anthropicUsage       `json:"usage"`
}

type anthropicContent struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input interface{} `json:"input,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// --- Anthropic SSE event types for streaming ---

type anthropicSSEMessageStart struct {
	Type         string        `json:"type"`
	Message      anthropicResp `json:"message"`
}

type anthropicSSEContentBlockStart struct {
	Type         string            `json:"type"`
	Index        int               `json:"index"`
	ContentBlock anthropicContent  `json:"content_block"`
}

type anthropicSSEContentBlockDelta struct {
	Type  string      `json:"type"`
	Index int         `json:"index"`
	Delta interface{} `json:"delta"`
}

type anthropicSSETextDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicSSEToolUseDelta struct {
	Type  string `json:"type"`
	Input string `json:"partial_json"`
}

type anthropicSSEMessageDelta struct {
	Type  string                  `json:"type"`
	Delta anthropicMessageDelta   `json:"delta"`
	Usage anthropicMessageDeltaUsage `json:"usage"`
}

type anthropicMessageDelta struct {
	StopReason   string `json:"stop_reason,omitempty"`
	StopSequence string `json:"stop_sequence,omitempty"`
}

type anthropicMessageDeltaUsage struct {
	OutputTokens int `json:"output_tokens"`
}

// msgCounter generates unique message IDs for Anthropic responses.
var msgCounter uint64

// handleAnthropicMessages translates an Anthropic /v1/messages request
// into an OpenAI /v1/chat/completions request, feeds it through the
// existing gateway pipeline, and translates the response back.
func (g *Gateway) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAnthropicError(w, 400, "invalid_request_error", fmt.Sprintf("read body: %v", err))
		return
	}

	// Parse Anthropic request
	var aReq anthropicReq
	if err := json.Unmarshal(body, &aReq); err != nil {
		writeAnthropicError(w, 400, "invalid_request_error", fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	log.Printf("[ANTHROPIC] Request model=%s stream=%v messages=%d tools=%d", aReq.Model, aReq.Stream, len(aReq.Messages), len(aReq.Tools))
	if aReq.Model == "" || len(aReq.Messages) == 0 {
		writeAnthropicError(w, 400, "invalid_request_error", "model and messages are required")
		return
	}

	// Translate Anthropic request -> OpenAI request
	oaiBody, err := translateAnthropicToOpenAI(&aReq)
	if err != nil {
		writeAnthropicError(w, 400, "invalid_request_error", fmt.Sprintf("translation error: %v", err))
		return
	}

	// Create a new HTTP request with the translated body, targeting our own /v1/chat/completions
	oaiReq, err := http.NewRequestWithContext(r.Context(), "POST", "http://localhost:4000/v1/chat/completions", bytes.NewReader(oaiBody))
	if err != nil {
		writeAnthropicError(w, 500, "api_error", fmt.Sprintf("create request: %v", err))
		return
	}

	// Copy relevant headers
	oaiReq.Header.Set("Content-Type", "application/json")
	oaiReq.Header.Set("Authorization", r.Header.Get("Authorization"))
	oaiReq.Header.Set("X-Api-Key", r.Header.Get("X-Api-Key"))
	if r.Header.Get("X-FreeLLM-Priority") != "" {
		oaiReq.Header.Set("X-FreeLLM-Priority", r.Header.Get("X-FreeLLM-Priority"))
	}

	// Feed through the gateway pipeline
	job := &RequestJob{
		Request:  oaiReq,
		Response: make(chan *ProxyResponse, 1),
		Ctx:      r.Context(),
	}

	queue := g.Queue
	if r.Header.Get("X-FreeLLM-Priority") == "high" {
		queue = g.HighPriQueue
	}

	select {
	case queue <- job:
	case <-r.Context().Done():
		return
	}

	resp := <-job.Response
	if resp.Err != nil {
		writeAnthropicError(w, 502, "api_error", resp.Err.Error())
		return
	}

	// Translate response back
	if resp.Stream != nil {
		defer resp.Stream.Close()
		if aReq.Stream {
			// Stream: translate OpenAI SSE -> Anthropic SSE
			g.streamAnthropicSSE(w, resp.Stream, aReq.Model, resp.ModelID)
		} else {
			// Non-stream but upstream gave us a stream - collect and translate
			g.collectAndTranslateStream(w, resp.Stream, aReq.Model, resp.ModelID)
		}
	} else {
		if aReq.Stream {
			// Client wants stream but got non-stream response - wrap as single SSE
			g.wrapNonStreamAsAnthropicSSE(w, resp.Body, aReq.Model, resp.ModelID)
		} else {
			// Non-stream: translate OpenAI response -> Anthropic response
			translateOpenAIToAnthropicResponse(w, resp, aReq.Model)
		}
	}
}

// translateAnthropicToOpenAI converts an Anthropic Messages API request
// into an OpenAI Chat Completions API request body.
func translateAnthropicToOpenAI(aReq *anthropicReq) ([]byte, error) {
	// Build OpenAI messages
	var oaiMessages []map[string]interface{}

	// Handle system prompt
	if aReq.System != nil {
		systemText := extractSystemText(aReq.System)
		if systemText != "" {
			oaiMessages = append(oaiMessages, map[string]interface{}{
				"role":    "system",
				"content": systemText,
			})
		}
	}

	// Convert messages - properly handle tool_use and tool_result content blocks
	for _, msg := range aReq.Messages {
		// Try to parse content as array of blocks
		var blocks []map[string]interface{}
		hasToolUse := false
		hasToolResult := false
		if err := json.Unmarshal(msg.Content, &blocks); err == nil {
			for _, b := range blocks {
				if b["type"] == "tool_use" {
					hasToolUse = true
				}
				if b["type"] == "tool_result" {
					hasToolResult = true
				}
			}
		}

		if hasToolUse && msg.Role == "assistant" {
			// Anthropic assistant with tool_use -> OpenAI assistant with tool_calls
			var textParts []string
			var toolCalls []map[string]interface{}
			for _, b := range blocks {
				switch b["type"] {
				case "text":
					if t, ok := b["text"].(string); ok && t != "" {
						textParts = append(textParts, t)
					}
				case "tool_use":
					tc := map[string]interface{}{
						"type": "function",
						"function": map[string]interface{}{
							"name": b["name"],
						},
					}
					if id, ok := b["id"].(string); ok {
						tc["id"] = id
					}
					// Convert input map to JSON string for "arguments"
					if input, ok := b["input"]; ok {
						if argBytes, err := json.Marshal(input); err == nil {
							tc["function"].(map[string]interface{})["arguments"] = string(argBytes)
						}
					}
					toolCalls = append(toolCalls, tc)
				}
			}
			oaiMsg := map[string]interface{}{
				"role": "assistant",
			}
			if len(textParts) > 0 {
				oaiMsg["content"] = strings.Join(textParts, "\n")
			} else {
				oaiMsg["content"] = nil
			}
			if len(toolCalls) > 0 {
				oaiMsg["tool_calls"] = toolCalls
			}
			oaiMessages = append(oaiMessages, oaiMsg)
		} else if hasToolResult && msg.Role == "user" {
			// Anthropic user with tool_result -> OpenAI "tool" role messages
			var textParts []string
			for _, b := range blocks {
				switch b["type"] {
				case "text":
					if t, ok := b["text"].(string); ok && t != "" {
						textParts = append(textParts, t)
					}
				case "tool_result":
					toolMsg := map[string]interface{}{
						"role": "tool",
					}
					if tid, ok := b["tool_use_id"].(string); ok {
						toolMsg["tool_call_id"] = tid
					}
					// Extract content from tool_result
					if rc, ok := b["content"]; ok {
						switch v := rc.(type) {
						case string:
							toolMsg["content"] = v
						case []interface{}:
							var rParts []string
							for _, item := range v {
								if m, ok := item.(map[string]interface{}); ok {
									if t, ok := m["text"].(string); ok {
										rParts = append(rParts, t)
									}
								}
							}
							toolMsg["content"] = strings.Join(rParts, "\n")
						default:
							if jb, err := json.Marshal(v); err == nil {
								toolMsg["content"] = string(jb)
							}
						}
					}
					oaiMessages = append(oaiMessages, toolMsg)
				}
			}
			// If there were also text parts in this user message, add as user msg
			if len(textParts) > 0 {
				oaiMessages = append(oaiMessages, map[string]interface{}{
					"role":    "user",
					"content": strings.Join(textParts, "\n"),
				})
			}
		} else {
			// Simple message - no tool content
			content := extractMessageContent(msg.Content)
			oaiMessages = append(oaiMessages, map[string]interface{}{
				"role":    msg.Role,
				"content": content,
			})
		}
	}

	// Build OpenAI request
	oaiReq := map[string]interface{}{
		"model":      aReq.Model,
		"messages":   oaiMessages,
		"max_tokens": aReq.MaxTokens,
	}
	if aReq.Stream {
		oaiReq["stream"] = true
	}
	if aReq.Temperature != nil {
		oaiReq["temperature"] = *aReq.Temperature
	}
	if aReq.TopP != nil {
		oaiReq["top_p"] = *aReq.TopP
	}

	// Translate Anthropic tools -> OpenAI tools
	if aReq.Tools != nil {
		var aTools []map[string]interface{}
		if err := json.Unmarshal(aReq.Tools, &aTools); err == nil {
			var oaiTools []map[string]interface{}
			for _, t := range aTools {
				oaiTool := map[string]interface{}{
					"type": "function",
					"function": map[string]interface{}{
						"name":        t["name"],
						"description": t["description"],
					},
				}
				if inputSchema, ok := t["input_schema"]; ok {
					oaiTool["function"].(map[string]interface{})["parameters"] = inputSchema
				}
				oaiTools = append(oaiTools, oaiTool)
			}
			if len(oaiTools) > 0 {
				oaiReq["tools"] = oaiTools
			}
		}
	}

	// Translate tool_choice
	if aReq.ToolChoice != nil {
		var tc interface{}
		if err := json.Unmarshal(aReq.ToolChoice, &tc); err == nil {
			switch v := tc.(type) {
			case string:
				if v == "auto" || v == "any" {
					oaiReq["tool_choice"] = "auto"
				}
			case map[string]interface{}:
				if v["type"] == "tool" {
					if name, ok := v["name"].(string); ok {
						oaiReq["tool_choice"] = map[string]interface{}{
							"type": "function",
							"function": map[string]interface{}{
								"name": name,
							},
						}
					}
				}
			}
		}
	}

	// Handle stop sequences
	if aReq.Stop != nil {
		var stop interface{}
		if err := json.Unmarshal(aReq.Stop, &stop); err == nil {
			oaiReq["stop"] = stop
		}
	}

	// Fix: many providers (Mistral, etc.) require the last message to be
	// user or tool role. Anthropic allows the last message to be assistant
	// (meaning "continue from here"). We need to append a continuation prompt.
	if len(oaiMessages) > 0 {
		lastMsg := oaiMessages[len(oaiMessages)-1]
		if role, ok := lastMsg["role"].(string); ok && role == "assistant" {
			// If assistant is last, add a user continuation message
			oaiMessages = append(oaiMessages, map[string]interface{}{
				"role":    "user",
				"content": "Continue your response.",
			})
			oaiReq["messages"] = oaiMessages
		}
	}

	return json.Marshal(oaiReq)
}

// sanitizeToolCallID converts Anthropic tool call IDs to a format
// compatible with Mistral and other providers that require alphanumeric
// IDs of exactly 9 characters (a-z, A-Z, 0-9 only).
func sanitizeToolCallID(id string) string {
	// Remove non-alphanumeric characters
	var cleaned strings.Builder
	for _, c := range id {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			cleaned.WriteRune(c)
		}
	}
	result := cleaned.String()
	// Pad or truncate to exactly 9 characters
	if len(result) < 9 {
		result = result + "000000000"[:9-len(result)]
	}
	if len(result) > 9 {
		result = result[:9]
	}
	return result
}

func extractSystemText(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}

	// Try as string first
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	// Try as array of content blocks
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var texts []string
		for _, b := range blocks {
			if b.Type == "text" {
				texts = append(texts, b.Text)
			}
		}
		return strings.Join(texts, "\n")
	}

	return string(raw)
}

// extractMessageContent extracts text content from an Anthropic message content field.
// Can be a string or an array of content blocks.
func extractMessageContent(raw json.RawMessage) interface{} {
	if raw == nil {
		return ""
	}

	// Try as string first
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	// Try as array of content blocks
	var blocks []map[string]interface{}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		var toolCalls []map[string]interface{}

		for _, b := range blocks {
			switch b["type"] {
			case "text":
				if text, ok := b["text"].(string); ok {
					parts = append(parts, text)
				}
			case "tool_use":
				tc := map[string]interface{}{
					"type": "function",
					"function": map[string]interface{}{
						"name":      b["name"],
						"arguments": b["input"],
					},
				}
				if id, ok := b["id"].(string); ok {
					tc["id"] = id
				}
				toolCalls = append(toolCalls, tc)
			case "tool_result":
				// Convert to OpenAI tool message format
				// This will be handled by the role being "user"
				content, _ := json.Marshal(b["content"])
				parts = append(parts, string(content))
			}
		}

		if len(toolCalls) > 0 {
			// Return as assistant message with tool_calls
			return map[string]interface{}{
				"content":    strings.Join(parts, "\n"),
				"tool_calls": toolCalls,
			}
		}

		return strings.Join(parts, "\n")
	}

	return string(raw)
}

// translateOpenAIToAnthropicResponse converts an OpenAI Chat Completion
// response to an Anthropic Messages response.
func translateOpenAIToAnthropicResponse(w http.ResponseWriter, resp *ProxyResponse, reqModel string) {
	var oaiResp struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Index        int        `json:"index"`
			Message      oaiMessage `json:"message"`
			FinishReason string    `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(resp.Body, &oaiResp); err != nil {
		writeAnthropicError(w, 502, "api_error", fmt.Sprintf("parse upstream response: %v", err))
		return
	}

	// Build Anthropic response content blocks
	var contentBlocks []anthropicContent
	if len(oaiResp.Choices) > 0 {
		msg := oaiResp.Choices[0].Message

		// Add text content if present
// Extract text from Content (can be string or array in json.RawMessage)
	text := ""
	if len(msg.Content) > 0 {
		var s string
		if json.Unmarshal(msg.Content, &s) == nil && s != "" {
			text = s
		} else {
			var blocks []map[string]interface{}
			if json.Unmarshal(msg.Content, &blocks) == nil {
				for _, b := range blocks {
					if b["type"] == "text" {
						if t, ok := b["text"].(string); ok && t != "" {
							text = t
							break
						}
					}
				}
			}
		}
	}
	if text == "" {
		text = msg.ReasoningContent
	}
	if text == "" {
		text = msg.Reasoning
	}
	if text == "" && len(msg.ReasoningDetails) > 0 {
		var details []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(msg.ReasoningDetails, &details) == nil {
			for _, d := range details {
				if d.Text != "" {
					text = d.Text
					break
				}
			}
		}
	}
		if text != "" {
			contentBlocks = append(contentBlocks, anthropicContent{
				Type: "text",
				Text: text,
			})
		}

		// Add tool_use content blocks from tool_calls
		if len(msg.ToolCalls) > 0 {
			var toolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function"`
			}
			if json.Unmarshal(msg.ToolCalls, &toolCalls) == nil {
				for _, tc := range toolCalls {
					// Unmarshal arguments string into object for Anthropic input field
					var inputObj interface{}
					if err := json.Unmarshal(tc.Function.Arguments, &inputObj); err != nil {
						inputObj = map[string]interface{}{}
					}

					toolID := tc.ID
					if toolID == "" {
						toolID = fmt.Sprintf("toolu_%016x", atomic.AddUint64(&msgCounter, 1))
					}
					contentBlocks = append(contentBlocks, anthropicContent{
						Type:  "tool_use",
						ID:    toolID,
						Name:  tc.Function.Name,
						Input: inputObj,
					})
				}
			}
		}
	}

	// Ensure at least one content block exists
	if len(contentBlocks) == 0 {
		contentBlocks = append(contentBlocks, anthropicContent{
			Type: "text",
			Text: "",
		})
	}

	aResp := anthropicResp{
		ID:         fmt.Sprintf("msg_%016x", atomic.AddUint64(&msgCounter, 1)),
		Type:       "message",
		Role:       "assistant",
		Content:    contentBlocks,
		Model:      reqModel,
		StopReason: translateFinishReason(oaiResp.Choices),
		Usage: anthropicUsage{
			InputTokens:  oaiResp.Usage.PromptTokens,
			OutputTokens: oaiResp.Usage.CompletionTokens,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.Status)
	json.NewEncoder(w).Encode(aResp)
}

type oaiMessage struct {
	Role              string          `json:"role"`
	Content          json.RawMessage `json:"content"`
	ReasoningContent  string          `json:"reasoning_content"`
	Reasoning         string          `json:"reasoning"`
	ReasoningDetails  json.RawMessage `json:"reasoning_details"`
	ToolCalls         json.RawMessage `json:"tool_calls,omitempty"`
}

func extractOAIMessageText(choices []struct {
Index int `json:"index"`
Message oaiMessage `json:"message"`
FinishReason string `json:"finish_reason"`
}) string {
if len(choices) == 0 {
return ""
}
msg := choices[0].Message

// Content can be a string or an array of content blocks
if len(msg.Content) > 0 {
// Try as string first
var s string
if json.Unmarshal(msg.Content, &s) == nil && s != "" {
return s
}
// Try as array of content blocks
var blocks []map[string]interface{}
if json.Unmarshal(msg.Content, &blocks) == nil {
for _, b := range blocks {
if b["type"] == "text" {
if t, ok := b["text"].(string); ok && t != "" {
return t
}
}
}
}
}

// Fall back to reasoning_content
if msg.ReasoningContent != "" {
return msg.ReasoningContent
}
// Fall back to reasoning
if msg.Reasoning != "" {
return msg.Reasoning
}
// Fall back to reasoning_details text
if len(msg.ReasoningDetails) > 0 {
var details []struct {
Text string `json:"text"`
}
if json.Unmarshal(msg.ReasoningDetails, &details) == nil {
for _, d := range details {
if d.Text != "" {
return d.Text
}
}
}
}
return ""
}

func translateFinishReason(choices []struct {
	Index        int        `json:"index"`
	Message      oaiMessage `json:"message"`
	FinishReason string     `json:"finish_reason"`
}) string {
	if len(choices) == 0 {
		return "end_turn"
	}
	reason := choices[0].FinishReason
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		return "end_turn"
	}
}

// streamAnthropicSSE translates an OpenAI SSE stream into an Anthropic SSE stream.
func (g *Gateway) streamAnthropicSSE(w http.ResponseWriter, body io.ReadCloser, reqModel string, modelID string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Del("Content-Length")

	flusher, canFlush := w.(http.Flusher)

	msgID := fmt.Sprintf("msg_%016x", atomic.AddUint64(&msgCounter, 1))

	// Send message_start event
	startEvent := anthropicSSEMessageStart{
		Type: "message_start",
		Message: anthropicResp{
			ID:   msgID,
			Type: "message",
			Role: "assistant",
			Content: []anthropicContent{},
			Model: reqModel,
			StopReason: "",
			Usage: anthropicUsage{
				InputTokens:  0,
				OutputTokens: 0,
			},
		},
	}
	writeSSE(w, "message_start", startEvent, flusher, canFlush)

	// Send content_block_start (text block, index 0)
	blockStart := anthropicSSEContentBlockStart{
		Type:  "content_block_start",
		Index: 0,
		ContentBlock: anthropicContent{
			Type: "text",
			Text: "",
		},
	}
	writeSSE(w, "content_block_start", blockStart, flusher, canFlush)

	// Read OpenAI SSE and translate content deltas
	bufReader := newBufferedLineReader(body)
	inputTokens := 0
	outputTokens := 0
	stopReason := "end_turn"
	sentBlockStop := false

	for {
		line, err := bufReader.ReadLine()
		if err != nil && line == "" {
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := line[6:]
		if data == "[DONE]" {
			break
		}

		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		// Extract usage if present
		if usage, ok := chunk["usage"].(map[string]interface{}); ok {
			if pt, ok := usage["prompt_tokens"].(float64); ok {
				inputTokens = int(pt)
			}
			if ct, ok := usage["completion_tokens"].(float64); ok {
				outputTokens = int(ct)
			}
		}

		// Extract delta content
		choices, ok := chunk["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			continue
		}

		choice, ok := choices[0].(map[string]interface{})
		if !ok {
			continue
		}

		// Check finish_reason
		if fr, ok := choice["finish_reason"].(string); ok && fr != "" && fr != "null" {
			switch fr {
			case "stop":
				stopReason = "end_turn"
			case "length":
				stopReason = "max_tokens"
			case "tool_calls":
				stopReason = "tool_use"
			default:
				stopReason = "end_turn"
			}
		}

		delta, ok := choice["delta"].(map[string]interface{})
		if !ok {
			continue
		}

		// Migrate reasoning/reasoning_content to content (cerebras reasoning models)
		if rc, ok := delta["reasoning_content"].(string); ok && rc != "" {
			if existing, ok := delta["content"].(string); !ok || existing == "" {
				delta["content"] = rc
			}
		}
		if r, ok := delta["reasoning"].(string); ok && r != "" {
			if existing, ok := delta["content"].(string); !ok || existing == "" {
				delta["content"] = r
			}
		}
		// Handle content delta
		if content, ok := delta["content"].(string); ok && content != "" {
			textDelta := anthropicSSEContentBlockDelta{
				Type:  "content_block_delta",
				Index: 0,
				Delta: anthropicSSETextDelta{
					Type: "text_delta",
					Text: content,
				},
			}
			writeSSE(w, "content_block_delta", textDelta, flusher, canFlush)
			outputTokens++
		}

		// Handle tool call deltas (simplified)
		if toolCalls, ok := delta["tool_calls"].([]interface{}); ok {
			for _, tc := range toolCalls {
				if tcMap, ok := tc.(map[string]interface{}); ok {
					if args, ok := tcMap["function"].(map[string]interface{}); ok {
						if partial, ok := args["arguments"].(string); ok {
							idx := 1 // tool calls start at index 1
							if idxFloat, ok := tcMap["index"].(float64); ok {
								idx = int(idxFloat) + 1
							}
							toolDelta := anthropicSSEContentBlockDelta{
								Type:  "content_block_delta",
								Index: idx,
								Delta: anthropicSSEToolUseDelta{
									Type:  "input_json_delta",
									Input: partial,
								},
							}
							writeSSE(w, "content_block_delta", toolDelta, flusher, canFlush)
						}
					}
				}
			}
		}
	}

	// Send content_block_stop
	if !sentBlockStop {
		blockStop := map[string]interface{}{
			"type":  "content_block_stop",
			"index": 0,
		}
		writeSSE(w, "content_block_stop", blockStop, flusher, canFlush)
		sentBlockStop = true
	}

	// Send message_delta with stop_reason
	msgDelta := anthropicSSEMessageDelta{
		Type: "message_delta",
		Delta: anthropicMessageDelta{
			StopReason:   stopReason,
			StopSequence: "",
		},
		Usage: anthropicMessageDeltaUsage{
			OutputTokens: outputTokens,
		},
	}
	writeSSE(w, "message_delta", msgDelta, flusher, canFlush)

	// Send message_stop
	writeSSE(w, "message_stop", map[string]string{"type": "message_stop"}, flusher, canFlush)

	if g.DB != nil && (inputTokens > 0 || outputTokens > 0) { db.LogUsage(g.DB, modelID, inputTokens, outputTokens) }
}

// collectAndTranslateStream collects an OpenAI SSE stream and translates
// it to a non-streaming Anthropic response.
func (g *Gateway) collectAndTranslateStream(w http.ResponseWriter, body io.ReadCloser, reqModel string, modelID string) {
	bufReader := newBufferedLineReader(body)
	var fullText strings.Builder
	inputTokens := 0
	outputTokens := 0
	stopReason := "end_turn"

	for {
		line, err := bufReader.ReadLine()
		if err != nil && line == "" {
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := line[6:]
		if data == "[DONE]" {
			break
		}

		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if usage, ok := chunk["usage"].(map[string]interface{}); ok {
			if pt, ok := usage["prompt_tokens"].(float64); ok {
				inputTokens = int(pt)
			}
			if ct, ok := usage["completion_tokens"].(float64); ok {
				outputTokens = int(ct)
			}
		}

		choices, ok := chunk["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			continue
		}
		choice, ok := choices[0].(map[string]interface{})
		if !ok {
			continue
		}

		if fr, ok := choice["finish_reason"].(string); ok && fr != "" && fr != "null" {
			switch fr {
			case "stop":
				stopReason = "end_turn"
			case "length":
				stopReason = "max_tokens"
			case "tool_calls":
				stopReason = "tool_use"
			}
		}

		delta, ok := choice["delta"].(map[string]interface{})
		if !ok {
			continue
		}
		// Migrate reasoning/reasoning_content to content (cerebras reasoning models)
		if rc, ok := delta["reasoning_content"].(string); ok && rc != "" {
			if existing, ok := delta["content"].(string); !ok || existing == "" {
				delta["content"] = rc
			}
		}
		if r, ok := delta["reasoning"].(string); ok && r != "" {
			if existing, ok := delta["content"].(string); !ok || existing == "" {
				delta["content"] = r
			}
		}
		if content, ok := delta["content"].(string); ok {
			fullText.WriteString(content)
			outputTokens++
		}
	}

	aResp := anthropicResp{
		ID:   fmt.Sprintf("msg_%016x", atomic.AddUint64(&msgCounter, 1)),
		Type: "message",
		Role: "assistant",
		Content: []anthropicContent{
			{Type: "text", Text: fullText.String()},
		},
		Model:      reqModel,
		StopReason: stopReason,
		Usage: anthropicUsage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(aResp)

	if g.DB != nil && (inputTokens > 0 || outputTokens > 0) {
		db.LogUsage(g.DB, modelID, inputTokens, outputTokens)
	}
}

// wrapNonStreamAsAnthropicSSE wraps a non-streaming OpenAI response as
// a single-shot Anthropic SSE stream.
func (g *Gateway) wrapNonStreamAsAnthropicSSE(w http.ResponseWriter, body []byte, reqModel string, modelID string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Del("Content-Length")

	flusher, canFlush := w.(http.Flusher)

	msgID := fmt.Sprintf("msg_%016x", atomic.AddUint64(&msgCounter, 1))

	// Parse the OpenAI response
	var oaiResp struct {
		Choices []struct {
			Message      oaiMessage `json:"message"`
			FinishReason string     `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	json.Unmarshal(body, &oaiResp)

	text := ""
	stopReason := "end_turn"
	inputTokens := oaiResp.Usage.PromptTokens
	outputTokens := oaiResp.Usage.CompletionTokens

	if len(oaiResp.Choices) > 0 {
  var cs string
  if json.Unmarshal(oaiResp.Choices[0].Message.Content, &cs) == nil {
  	text = cs
  }
		switch oaiResp.Choices[0].FinishReason {
		case "stop":
			stopReason = "end_turn"
		case "length":
			stopReason = "max_tokens"
		case "tool_calls":
			stopReason = "tool_use"
		}
	}

	// message_start
	startEvent := anthropicSSEMessageStart{
		Type: "message_start",
		Message: anthropicResp{
			ID:      msgID,
			Type:    "message",
			Role:    "assistant",
			Content: []anthropicContent{},
			Model:   reqModel,
			Usage: anthropicUsage{InputTokens: inputTokens, OutputTokens: 0},
		},
	}
	writeSSE(w, "message_start", startEvent, flusher, canFlush)

	// content_block_start
	writeSSE(w, "content_block_start", anthropicSSEContentBlockStart{
		Type:  "content_block_start",
		Index: 0,
		ContentBlock: anthropicContent{Type: "text", Text: ""},
	}, flusher, canFlush)

	// content_block_delta (all text at once)
	if text != "" {
		writeSSE(w, "content_block_delta", anthropicSSEContentBlockDelta{
			Type:  "content_block_delta",
			Index: 0,
			Delta: anthropicSSETextDelta{Type: "text_delta", Text: text},
		}, flusher, canFlush)
	}

	// content_block_stop
	writeSSE(w, "content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": 0}, flusher, canFlush)

	// message_delta
	writeSSE(w, "message_delta", anthropicSSEMessageDelta{
		Type:  "message_delta",
		Delta: anthropicMessageDelta{StopReason: stopReason},
		Usage: anthropicMessageDeltaUsage{OutputTokens: outputTokens},
	}, flusher, canFlush)

	// message_stop
	writeSSE(w, "message_stop", map[string]string{"type": "message_stop"}, flusher, canFlush)

	if g.DB != nil && (inputTokens > 0 || outputTokens > 0) { db.LogUsage(g.DB, modelID, inputTokens, outputTokens) }
}

// writeSSE writes a single SSE event to the response writer.
func writeSSE(w http.ResponseWriter, eventType string, data interface{}, flusher http.Flusher, canFlush bool) {
	dataJSON, err := json.Marshal(data)
	if err != nil {
		log.Printf("[anthropic-sse] marshal error: %v", err)
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, dataJSON)
	if canFlush {
		flusher.Flush()
	}
}

// writeAnthropicError writes an Anthropic-format error response.
func writeAnthropicError(w http.ResponseWriter, status int, errType string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"type": "error",
		"error": map[string]interface{}{
			"type":    errType,
			"message": message,
		},
	})
}

// bufferedLineReader is a simple line reader for SSE streams.
type bufferedLineReader struct {
	reader io.Reader
	buf    []byte
}

func newBufferedLineReader(r io.Reader) *bufferedLineReader {
	return &bufferedLineReader{reader: r, buf: make([]byte, 0, 4096)}
}

func (b *bufferedLineReader) ReadLine() (string, error) {
	for {
		// Check if we have a complete line in the buffer
		for i := 0; i < len(b.buf); i++ {
			if b.buf[i] == '\n' {
				line := string(b.buf[:i])
				b.buf = b.buf[i+1:]
				return line, nil
			}
		}

		// Read more data
		tmp := make([]byte, 1024)
		n, err := b.reader.Read(tmp)
		if n > 0 {
			b.buf = append(b.buf, tmp[:n]...)
		}
		if err != nil && n == 0 {
			// Return remaining buffer
			if len(b.buf) > 0 {
				line := string(b.buf)
				b.buf = nil
				return line, err
			}
			return "", err
		}
	}
}
