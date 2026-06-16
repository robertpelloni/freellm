package transform

import (
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// Test data (realistic JSON snippets from each provider)
// ---------------------------------------------------------------------------

const sampleOpenAI = `{
  "id": "chatcmpl-abc123",
  "object": "chat.completion",
  "created": 1715000000,
  "model": "gpt-4",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "Hello!"},
    "finish_reason": "stop"
  }],
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 5,
    "total_tokens": 15
  }
}`

const sampleAnthropic = `{
  "id": "msg_12345",
  "model": "claude-3-5-sonnet-20241022",
  "type": "message",
  "content": [{"type": "text", "text": "Hello!"}],
  "stop_reason": "end_turn"
}`

const sampleMistral = `{
  "id": "mistral-abc123",
  "object": "chat.completion",
  "created": 1715000000,
  "model": "mistral-large-latest",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "Hello!"},
    "finish_reason": "stop"
  }],
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 5,
    "total_tokens": 15
  }
}`

// ---------------------------------------------------------------------------
// Helper: assert equal for two JSON strings (ignores whitespace order)
// ---------------------------------------------------------------------------
func jsonEqual(t *testing.T, got, want string) {
	t.Helper()
	var g, w interface{}
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("got JSON is invalid: %v\ngot: %s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want JSON is invalid: %v\nwant: %s", err, want)
	}
	if !deepEqual(g, w) {
		t.Errorf("JSON mismatch:\ngot:  %s\nwant: %s", got, want)
	}
}

func deepEqual(a, b interface{}) bool {
	// simple deep-equal for map[string]interface{} and []interface{}
	ma, okA := a.(map[string]interface{})
	mb, okB := b.(map[string]interface{})
	if okA && okB {
		if len(ma) != len(mb) {
			return false
		}
		for k, va := range ma {
			if vb, ok := mb[k]; !ok || !deepEqual(va, vb) {
				return false
			}
		}
		return true
	}
	la, okA := a.([]interface{})
	lb, okB := b.([]interface{})
	if okA && okB {
		if len(la) != len(lb) {
			return false
		}
		for i := range la {
			if !deepEqual(la[i], lb[i]) {
				return false
			}
		}
		return true
	}
	return a == b
}

// ---------------------------------------------------------------------------
// Provider-specific tests
// ---------------------------------------------------------------------------

func TestFromOpenAI(t *testing.T) {
	resp, err := FromOpenAI([]byte(sampleOpenAI))
	if err != nil {
		t.Fatalf("FromOpenAI failed: %v", err)
	}
	if resp.ID != "chatcmpl-abc123" {
		t.Errorf("wrong ID: got %q, want %q", resp.ID, "chatcmpl-abc123")
	}
	if len(resp.Choices) != 1 {
		t.Errorf("wrong number of choices: got %d, want 1", len(resp.Choices))
	}
	if resp.Choices[0].Message == nil || resp.Choices[0].Message.Content != "Hello!" {
		t.Errorf("wrong message content: got %q", resp.Choices[0].Message.Content)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("wrong finish reason: got %q", resp.Choices[0].FinishReason)
	}
}

func TestFromAnthropic(t *testing.T) {
	resp, err := FromAnthropic([]byte(sampleAnthropic))
	if err != nil {
		t.Fatalf("FromAnthropic failed: %v", err)
	}
	if resp.ID != "msg_12345" {
		t.Errorf("wrong ID: got %q, want %q", resp.ID, "msg_12345")
	}
	if len(resp.Choices) != 1 {
		t.Errorf("wrong number of choices: got %d, want 1", len(resp.Choices))
	}
	if resp.Choices[0].Message == nil || resp.Choices[0].Message.Content != "Hello!" {
		t.Errorf("wrong message content: got %q", resp.Choices[0].Message.Content)
	}
	// Anthropic stop_reason "end_turn" → mapped to "stop" by our code
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("wrong finish reason: got %q", resp.Choices[0].FinishReason)
	}
}

func TestFromMistral(t *testing.T) {
	resp, err := FromMistral([]byte(sampleMistral))
	if err != nil {
		t.Fatalf("FromMistral failed: %v", err)
	}
	if resp.ID != "mistral-abc123" {
		t.Errorf("wrong ID: got %q, want %q", resp.ID, "mistral-abc123")
	}
	if len(resp.Choices) != 1 {
		t.Errorf("wrong number of choices: got %d, want 1", len(resp.Choices))
	}
	if resp.Choices[0].Message == nil || resp.Choices[0].Message.Content != "Hello!" {
		t.Errorf("wrong message content: got %q", resp.Choices[0].Message.Content)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("wrong finish reason: got %q", resp.Choices[0].FinishReason)
	}
}

// ---------------------------------------------------------------------------
// Integration test for the dispatcher
// ---------------------------------------------------------------------------

func TestParse(t *testing.T) {
	cases := []struct {
		provider string
		input    string
		wantID   string
	}{
		{"openai", sampleOpenAI, "chatcmpl-abc123"},
		{"anthropic", sampleAnthropic, "msg_12345"},
		{"mistralai", sampleMistral, "mistral-abc123"},
		{"mistral", sampleMistral, "mistral-abc123"},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			resp, err := Parse(tc.provider, []byte(tc.input))
			if err != nil {
				t.Fatalf("Parse(%s) failed: %v", tc.provider, err)
			}
			if resp.ID != tc.wantID {
				t.Errorf("wrong ID for %s: got %q, want %q", tc.provider, resp.ID, tc.wantID)
			}
			// marshal back to JSON to confirm it’s valid
			_, err = Marshal(resp)
			if err != nil {
				t.Fatalf("Marshal failed: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test that marshalled JSON is valid and contains the expected fields
// ---------------------------------------------------------------------------

func TestMarshal(t *testing.T) {
	resp := &ChatCompletionResponse{
		ID:     "test-001",
		Object: "chat.completion",
		Created: 1715000000,
		Model:  "gpt-4",
		Choices: []Choice{{
			Index:        0,
			Message:       &ChatMessage{Role: "assistant", Content: "Hi!"},
			FinishReason: "stop",
		}},
		Usage: &Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}
	got, err := Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	// unmarshal to check it’s valid JSON
	var m map[string]interface{}
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("Marshal produced invalid JSON: %v", err)
	}
	if m["id"] != "test-001" {
		t.Errorf("wrong id in marshalled JSON: got %v", m["id"])
	}
	if m["model"] != "gpt-4" {
		t.Errorf("wrong model in marshalled JSON: got %v", m["model"])
	}
}
