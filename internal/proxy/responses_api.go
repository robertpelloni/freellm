package proxy

import (
	"bufio"
	"encoding/json"
	"log"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handleResponsesAPI translates OpenAI Responses API requests to Chat Completions
// and translates the responses back. This allows Codex and other Responses API
// clients to use FreeLLM's routing.
func (g *Gateway) handleResponsesAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, 405, "Method not allowed", "method_not_allowed", "responses")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, 400, "Failed to read request body", "invalid_request", "responses")
		return
	}
	body = SanitizeJSONStringLiterals(body)

	var respReq map[string]interface{}
	if err := json.Unmarshal(body, &respReq); err != nil {
		writeJSONError(w, 400, "Invalid JSON", "invalid_request", "responses")
		return
	}

	messages := convertResponsesInputToMessages(respReq)

	chatReq := map[string]interface{}{
		"model":    respReq["model"],
		"messages": messages,
		"stream":   respReq["stream"],
	}
	if temp, ok := respReq["temperature"]; ok {
		chatReq["temperature"] = temp
	}
	if topP, ok := respReq["top_p"]; ok {
		chatReq["top_p"] = topP
	}
	if maxOut, ok := respReq["max_output_tokens"]; ok {
		chatReq["max_tokens"] = maxOut
	}
	if tools, ok := respReq["tools"].([]interface{}); ok {
		chatTools := convertResponsesToolsToChatTools(tools)
		if len(chatTools) > 0 {
			chatReq["tools"] = chatTools
		}
	}

	isStream := false
	if s, ok := respReq["stream"].(bool); ok {
		isStream = s
	}

	chatBody, _ := json.Marshal(chatReq)

	// Call processJob directly to bypass the request queue (avoids deadlock)
	internalReq, err := http.NewRequest("POST", "http://127.0.0.1:"+strconv.Itoa(g.Port)+"/v1/chat/completions", strings.NewReader(string(chatBody)))
	if err != nil {
		writeJSONError(w, 500, "Failed to create internal request", "internal_error", "responses")
		return
	}
	internalReq.Header.Set("Content-Type", "application/json")
	if auth := r.Header.Get("Authorization"); auth != "" {
		internalReq.Header.Set("Authorization", auth)
	}

	// Create a RequestJob and call processJob directly (bypasses queue, avoids deadlock)
	job := &RequestJob{
		Request: internalReq,
		Response: make(chan *ProxyResponse, 1),
		Ctx: r.Context(),
	}
	log.Printf("[RESPONSES] Calling processJob directly")
	go g.processJob(job)

	// Wait for the response with a timeout
	var resp *http.Response
	select {
	case proxyResp := <-job.Response:
		if proxyResp.Err != nil {
			log.Printf("[RESPONSES] processJob error: %v", proxyResp.Err)
			writeJSONError(w, 502, "Routing failed: "+proxyResp.Err.Error(), "server_error", "")
			return
		}
		log.Printf("[RESPONSES] processJob result: status=%d, body=%s", proxyResp.Status, func() string { s := string(proxyResp.Body); if len(s) > 200 { return s[:200] }; return s }())
		// Build an http.Response from the ProxyResponse
		var body io.ReadCloser
		if proxyResp.Stream != nil {
			body = proxyResp.Stream
		} else {
			body = io.NopCloser(strings.NewReader(string(proxyResp.Body)))
		}
		resp = &http.Response{
			StatusCode: proxyResp.Status,
			Body:       body,
			Header:     proxyResp.Header,
		}
	case <-time.After(30 * time.Second):
		log.Printf("[RESPONSES] processJob timeout")
		writeJSONError(w, 504, "Routing timeout", "server_error", "")
		return
	}

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		writeJSONError(w, resp.StatusCode, string(respBody), "upstream_error", "responses")
		return
	}

	if isStream {
		g.translateStreamToResponses(w, resp, respReq)
	} else {
		g.translateNonStreamToResponses(w, resp, respReq)
	}
}

func (g *Gateway) translateNonStreamToResponses(w http.ResponseWriter, resp *http.Response, respReq map[string]interface{}) {
	fullText := ""
	modelName := ""
	if m, ok := respReq["model"].(string); ok {
		modelName = m
	}
	var toolCalls []interface{}
	finishReason := ""
	inputTokens := 0
	outputTokens := 0

	// Read the response body
	bodyBytes, _ := io.ReadAll(resp.Body)
	bodyStr := string(bodyBytes)

	// Check if this is SSE (starts with "data: ") or regular JSON
	if strings.HasPrefix(strings.TrimSpace(bodyStr), "data: ") {
		// Parse SSE format
		log.Printf("[RESPONSES] Parsing SSE response (%d bytes)", len(bodyBytes))
		bufReader := bufio.NewReader(strings.NewReader(bodyStr))
		for {
			line, err := bufReader.ReadString('\n')
			if err != nil && line == "" {
				break
			}
			line = strings.TrimRight(line, "\r\n")
			if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
				continue
			}
			var chunk map[string]interface{}
			if json.Unmarshal([]byte(line[6:]), &chunk) != nil {
				continue
			}
			if m, ok := chunk["model"].(string); ok && m != "" {
				modelName = m
			}
			if choices, ok := chunk["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if fr, ok := choice["finish_reason"].(string); ok && fr != "" && fr != "null" {
						finishReason = fr
					}
					if delta, ok := choice["delta"].(map[string]interface{}); ok {
						if c, ok := delta["content"].(string); ok {
							fullText += c
						}
						if tc, ok := delta["tool_calls"].([]interface{}); ok {
							toolCalls = append(toolCalls, tc...)
						}
					}
				}
			}
			if u, ok := chunk["usage"].(map[string]interface{}); ok {
				if it, ok := u["prompt_tokens"].(float64); ok {
					inputTokens = int(it)
				}
				if ot, ok := u["completion_tokens"].(float64); ok {
					outputTokens = int(ot)
				}
			}
		}
	} else {
		// Parse as regular Chat Completions JSON
		log.Printf("[RESPONSES] Parsing JSON response (%d bytes)", len(bodyBytes))
		var chatResp map[string]interface{}
		if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
			log.Printf("[RESPONSES] JSON parse error: %v", err)
			writeJSONError(w, 502, "Failed to parse upstream response", "server_error", "")
			return
		}
		if m, ok := chatResp["model"].(string); ok && m != "" {
			modelName = m
		}
		if choices, ok := chatResp["choices"].([]interface{}); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]interface{}); ok {
				if fr, ok := choice["finish_reason"].(string); ok && fr != "" && fr != "null" {
					finishReason = fr
				}
				if msg, ok := choice["message"].(map[string]interface{}); ok {
					if c, ok := msg["content"].(string); ok {
						fullText = c
					}
					if tc, ok := msg["tool_calls"].([]interface{}); ok {
						toolCalls = append(toolCalls, tc...)
					}
				}
			}
		}
		if u, ok := chatResp["usage"].(map[string]interface{}); ok {
			if it, ok := u["prompt_tokens"].(float64); ok {
				inputTokens = int(it)
			}
			if ot, ok := u["completion_tokens"].(float64); ok {
				outputTokens = int(ot)
			}
		}
	}
	log.Printf("[RESPONSES] Parsing done: fullText=%d chars, model=%s", len(fullText), modelName)

	// Build the Responses API response object
	respID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	msgContent := []interface{}{}
	if fullText != "" {
		msgContent = append(msgContent, map[string]interface{}{
			"type": "output_text",
			"text": fullText,
		})
	}
	if len(toolCalls) > 0 {
		for _, tc := range toolCalls {
			if tcMap, ok := tc.(map[string]interface{}); ok {
				msgContent = append(msgContent, tcMap)
			}
		}
	}

	output := []interface{}{
		map[string]interface{}{
			"type":    "message",
			"id":      fmt.Sprintf("msg_%d", time.Now().UnixNano()),
			"role":    "assistant",
			"status":  "completed",
			"content": msgContent,
		},
	}

	status := "completed"
	if finishReason == "length" {
		status = "incomplete"
	}

	result := map[string]interface{}{
		"id":       respID,
		"object":   "response",
		"model":    modelName,
		"status":   status,
		"output":   output,
		"metadata": map[string]interface{}{},
		"usage": map[string]interface{}{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
			"total_tokens":  inputTokens + outputTokens,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (g *Gateway) translateStreamToResponses(w http.ResponseWriter, resp *http.Response, respReq map[string]interface{}) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)

	respID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	modelName := ""
	if m, ok := respReq["model"].(string); ok {
		modelName = m
	}

	// Send response.created
	writeSSEEvent(w, "response.created", map[string]interface{}{
		"type": "response.created",
		"response": map[string]interface{}{
			"id":       respID,
			"object":   "response",
			"model":    modelName,
			"status":   "in_progress",
			"output":   []interface{}{},
			"metadata": map[string]interface{}{},
		},
	}, canFlush, flusher)

	// Send output_item.added
	writeSSEEvent(w, "response.output_item.added", map[string]interface{}{
		"type":         "response.output_item.added",
		"output_index": 0,
		"item": map[string]interface{}{
			"type":   "message",
			"id":     fmt.Sprintf("msg_%d", time.Now().UnixNano()),
			"role":   "assistant",
			"status": "in_progress",
		},
	}, canFlush, flusher)

	// Send content_part.added
	writeSSEEvent(w, "response.content_part.added", map[string]interface{}{
		"type":          "response.content_part.added",
		"output_index":  0,
		"content_index": 0,
		"part": map[string]interface{}{
			"type": "output_text",
			"text": "",
		},
	}, canFlush, flusher)

	// Parse the chat completions SSE stream and translate to Responses API events
	fullText := ""
	var leftover string
	readBuf := make([]byte, 8192)

	for {
		n, err := resp.Body.Read(readBuf)
		if n > 0 {
			leftover += string(readBuf[:n])
			for {
				idx := strings.Index(leftover, "\n")
				if idx < 0 {
					break
				}
				line := strings.TrimRight(leftover[:idx], "\r")
				leftover = leftover[idx+1:]

				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				data := strings.TrimPrefix(line, "data: ")
				if data == "[DONE]" {
					continue
				}
				var chatChunk map[string]interface{}
				if json.Unmarshal([]byte(data), &chatChunk) != nil {
					continue
				}
				if choices, ok := chatChunk["choices"].([]interface{}); ok && len(choices) > 0 {
					if choice, ok := choices[0].(map[string]interface{}); ok {
						if delta, ok := choice["delta"].(map[string]interface{}); ok {
					if content, ok := delta["content"].(string); ok && content != "" {
						fullText += content
						writeSSEEvent(w, "response.output_text.delta", map[string]interface{}{
							"type":          "response.output_text.delta",
							"output_index":  0,
							"content_index": 0,
							"delta":         content,
						}, canFlush, flusher)
					}
					// Handle tool call deltas
					if toolCalls, ok := delta["tool_calls"].([]interface{}); ok {
						for _, tc := range toolCalls {
							if tcMap, ok := tc.(map[string]interface{}); ok {
								tcID, _ := tcMap["id"].(string)
								tcIdx, _ := tcMap["index"].(float64)
								fnName := ""
								fnArgs := ""
								if fn, ok := tcMap["function"].(map[string]interface{}); ok {
									fnName, _ = fn["name"].(string)
									fnArgs, _ = fn["arguments"].(string)
								}
								// Emit function_call events for Responses API
								if fnName != "" {
									writeSSEEvent(w, "response.function_call_arguments.delta", map[string]interface{}{
										"type":         "response.function_call_arguments.delta",
										"output_index": int(tcIdx) + 1,
										"call_id":      tcID,
										"delta":        fnArgs,
									}, canFlush, flusher)
								}
								if fnArgs != "" {
									writeSSEEvent(w, "response.function_call_arguments.delta", map[string]interface{}{
										"type":         "response.function_call_arguments.delta",
										"output_index": int(tcIdx) + 1,
										"call_id":      tcID,
										"delta":        fnArgs,
									}, canFlush, flusher)
								}
							}
						}
					}
						}
					}
				}
			}
		}
		if err != nil {
			break
		}
	}

	// Send text.done
	writeSSEEvent(w, "response.output_text.done", map[string]interface{}{
		"type":          "response.output_text.done",
		"output_index":  0,
		"content_index": 0,
		"text":          fullText,
	}, canFlush, flusher)

	// Send content_part.done
	writeSSEEvent(w, "response.content_part.done", map[string]interface{}{
		"type":          "response.content_part.done",
		"output_index":  0,
		"content_index": 0,
		"part": map[string]interface{}{
			"type": "output_text",
			"text": fullText,
		},
	}, canFlush, flusher)

	// Send output_item.done
	writeSSEEvent(w, "response.output_item.done", map[string]interface{}{
		"type":         "response.output_item.done",
		"output_index": 0,
		"item": map[string]interface{}{
			"type":   "message",
			"id":     fmt.Sprintf("msg_%d", time.Now().UnixNano()),
			"role":   "assistant",
			"status": "completed",
			"content": []interface{}{
				map[string]interface{}{
					"type": "output_text",
					"text": fullText,
				},
			},
		},
	}, canFlush, flusher)

	// Send response.completed
	writeSSEEvent(w, "response.completed", map[string]interface{}{
		"type": "response.completed",
		"response": map[string]interface{}{
			"id":       respID,
			"object":   "response",
			"model":    modelName,
			"status":   "completed",
			"output": []interface{}{
				map[string]interface{}{
					"type":   "message",
					"id":     fmt.Sprintf("msg_%d", time.Now().UnixNano()),
					"role":   "assistant",
					"status": "completed",
					"content": []interface{}{
						map[string]interface{}{
							"type": "output_text",
							"text": fullText,
						},
					},
				},
			},
			"metadata": map[string]interface{}{},
		},
	}, canFlush, flusher)

	fmt.Fprintf(w, "data: [DONE]\n\n")
	if canFlush {
		flusher.Flush()
	}
}

func convertResponsesInputToMessages(req map[string]interface{}) []interface{} {
	var messages []interface{}

	// Add system instructions as system message
	if instructions, ok := req["instructions"].(string); ok && instructions != "" {
		messages = append(messages, map[string]interface{}{
			"role":    "system",
			"content": instructions,
		})
	}

	// Handle input - can be a string or an array of input items
	switch input := req["input"].(type) {
	case string:
		messages = append(messages, map[string]interface{}{
			"role":    "user",
			"content": input,
		})
	case []interface{}:
		for _, item := range input {
			if itemMap, ok := item.(map[string]interface{}); ok {
				itemType, _ := itemMap["type"].(string)
				role, _ := itemMap["role"].(string)

				switch {
				case itemType == "function_call":
					// Convert Responses API function_call to Chat Completions tool_calls
					callID, _ := itemMap["call_id"].(string)
					fnName, _ := itemMap["name"].(string)
					fnArgs, _ := itemMap["arguments"].(string)
					// Add as assistant message with tool_calls
					messages = append(messages, map[string]interface{}{
						"role": "assistant",
						"tool_calls": []interface{}{map[string]interface{}{
							"id":   callID,
							"type": "function",
							"function": map[string]interface{}{
								"name":      fnName,
								"arguments": fnArgs,
							},
						}},
					})

				case itemType == "function_call_output":
					// Convert to Chat Completions tool result message
					callID, _ := itemMap["call_id"].(string)
					output, _ := itemMap["output"].(string)
					messages = append(messages, map[string]interface{}{
						"role":       "tool",
						"tool_call_id": callID,
						"content":    output,
					})

				default:
					// Regular message (user/assistant/system)
					if role == "" {
						role = "user"
					}
					content := extractContentFromInputItem(itemMap)
					msg := map[string]interface{}{
						"role":    role,
						"content": content,
					}
					// Preserve tool_calls from assistant messages
					if tc, ok := itemMap["tool_calls"].([]interface{}); ok {
						msg["tool_calls"] = tc
					}
					messages = append(messages, msg)
				}
			}
		}
	}

	return messages
}

func extractContentFromInputItem(item map[string]interface{}) interface{} {
	// If content is a string, return directly
	if content, ok := item["content"].(string); ok {
		return content
	}

	// If content is an array of content parts (Responses API format)
	if contentParts, ok := item["content"].([]interface{}); ok {
		var texts []string
		for _, part := range contentParts {
			if partMap, ok := part.(map[string]interface{}); ok {
				if text, ok := partMap["text"].(string); ok {
					texts = append(texts, text)
				}
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n")
		}
	}

	// If input field exists (for function_call_output type)
	if input, ok := item["input"].(string); ok {
		return input
	}

	// Fallback
	if content, ok := item["content"]; ok {
		return content
	}
	return ""
}

func convertResponsesToolsToChatTools(tools []interface{}) []interface{} {
	var chatTools []interface{}
	for _, tool := range tools {
		if toolMap, ok := tool.(map[string]interface{}); ok {
			toolType, _ := toolMap["type"].(string)
			switch toolType {
			case "function":
				if fn, ok := toolMap["function"].(map[string]interface{}); ok {
					chatTools = append(chatTools, map[string]interface{}{
						"type":     "function",
						"function": fn,
					})
				}
			default:
				// web_search, file_search, computer_use etc. - Responses API specific
				chatTools = append(chatTools, toolMap)
			}
		}
	}
	return chatTools
}

func convertChatToResponsesAPI(chatResp map[string]interface{}, origReq map[string]interface{}) map[string]interface{} {
	respID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	modelName, _ := origReq["model"].(string)

	content := ""
	role := "assistant"
	var toolCalls []interface{}
	finishReason := ""

	if choices, ok := chatResp["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if fr, ok := choice["finish_reason"].(string); ok {
				finishReason = fr
			}
			if msg, ok := choice["message"].(map[string]interface{}); ok {
				if c, ok := msg["content"].(string); ok {
					content = c
				}
				if r, ok := msg["role"].(string); ok {
					role = r
				}
				if tc, ok := msg["tool_calls"].([]interface{}); ok {
					toolCalls = tc
				}
			}
		}
	}

	usage := map[string]interface{}{}
	if u, ok := chatResp["usage"].(map[string]interface{}); ok {
		usage = u
	}

	// Build output items
	var output []interface{}

	// Add the message output (text content)
	if content != "" {
		msgContent := []interface{}{
			map[string]interface{}{
				"type": "output_text",
				"text": content,
			},
		}
		output = append(output, map[string]interface{}{
			"type":    "message",
			"id":      fmt.Sprintf("msg_%d", time.Now().UnixNano()),
			"role":    role,
			"status":  "completed",
			"content": msgContent,
		})
	}

	// Add function_call output items for each tool call
	for i, tc := range toolCalls {
		if tcMap, ok := tc.(map[string]interface{}); ok {
			callID, _ := tcMap["id"].(string)
			fnName := ""
			fnArgs := ""
			if fn, ok := tcMap["function"].(map[string]interface{}); ok {
				fnName, _ = fn["name"].(string)
				fnArgs, _ = fn["arguments"].(string)
			}
			output = append(output, map[string]interface{}{
				"type":        "function_call",
				"id":          fmt.Sprintf("fc_%d_%d", time.Now().UnixNano(), i),
				"call_id":     callID,
				"name":        fnName,
				"arguments":   fnArgs,
				"status":      "completed",
			})
		}
	}

	// If no content and no tool calls, add empty message
	if len(output) == 0 {
		output = append(output, map[string]interface{}{
			"type":    "message",
			"id":      fmt.Sprintf("msg_%d", time.Now().UnixNano()),
			"role":    role,
			"status":  "completed",
			"content": []interface{}{},
		})
	}

	status := "completed"
	if finishReason == "tool_calls" || len(toolCalls) > 0 {
		status = "completed" // Responses API uses completed even with tool calls
	}

	return map[string]interface{}{
		"id":       respID,
		"object":   "response",
		"model":    modelName,
		"status":   status,
		"output":   output,
		"usage":    usage,
		"metadata": map[string]interface{}{},
	}
}

func writeSSEEvent(w http.ResponseWriter, event string, data interface{}, canFlush bool, flusher http.Flusher) {
	jsonData, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, string(jsonData))
	if canFlush {
		flusher.Flush()
	}
}
