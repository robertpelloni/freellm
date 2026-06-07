package engine

import (
	"strconv"
	"regexp"
	"strings"
	"sync"
)

// runtimeDeadMu protects runtime-registered dead models
var runtimeDeadMu sync.RWMutex
var runtimeDeadModels = map[string]bool{}

// ExplicitSizePattern matches parameter sizes that are clearly part of the
// model name (e.g., "-8b", "-70b", "_7b") not version numbers like "3.1".
var ExplicitSizePattern = regexp.MustCompile(`(?:[-_/])([0-9]+)[bB](?:[-_]|$)`)

type ModelSpec struct {
    Params   int    `json:"params"`
    Ctx      int    `json:"ctx"`
    Provider string `json:"provider"`
}

var KnownModels = map[string]ModelSpec{
    "github/Meta-Llama-3.1-405B-Instruct": {Params: 405, Ctx: 128000, Provider: "github"},
    "github/gpt-4o": {Params: 175, Ctx: 128000, Provider: "github"},
    "github/gpt-4o-mini": {Params: 175, Ctx: 128000, Provider: "github"},
    "nvidia_nim/mistralai/mistral-large-3-675b-instruct-2512": {Params: 675, Ctx: 128000, Provider: "nvidia_nim"},
    "sambanova/DeepSeek-V3.2": {Params: 671, Ctx: 131072, Provider: "sambanova"},
    "cerebras/gpt-oss-120b": {Params: 120, Ctx: 131072, Provider: "cerebras"},
    "groq/llama-3.3-70b-versatile": {Params: 70, Ctx: 128000, Provider: "groq"},
    "mistral/mistral-large-latest": {Params: 123, Ctx: 128000, Provider: "mistral"},
    "opencode_zen/deepseek-v4-flash-free": {Params: 671, Ctx: 65536, Provider: "opencode_zen"},
	"openrouter/deepseek/deepseek-v3-0324": {Params: 671, Ctx: 65536, Provider: "openrouter"},
	"openrouter/deepseek/deepseek-r1": {Params: 671, Ctx: 65536, Provider: "openrouter"},
	"openrouter/deepseek/deepseek-v4-flash": {Params: 671, Ctx: 65536, Provider: "openrouter"},
	"openrouter/deepseek/deepseek-chat-v3-0324": {Params: 671, Ctx: 65536, Provider: "openrouter"},
	"openrouter/deepseek/deepseek-v3.1-terminus": {Params: 671, Ctx: 131072, Provider: "openrouter"},
	// SiliconFlow free models
	"siliconflow/deepseek-ai/DeepSeek-V3": {Params: 671, Ctx: 65536, Provider: "siliconflow"},
	"siliconflow/Qwen/Qwen3-235B-A22B": {Params: 235, Ctx: 131072, Provider: "siliconflow"},
	"siliconflow/meta-llama/Llama-4-Scout-17B-16E": {Params: 109, Ctx: 10000000, Provider: "siliconflow"},
	"siliconflow/google/gemma-4-27b-it": {Params: 27, Ctx: 256000, Provider: "siliconflow"},
	// Together AI free models
	"together/meta-llama/Llama-4-Scout-17B-16E-Instruct": {Params: 109, Ctx: 10000000, Provider: "together"},
	"together/Qwen/Qwen3-235B-A22B": {Params: 235, Ctx: 131072, Provider: "together"},
	"together/deepseek-ai/DeepSeek-V3": {Params: 671, Ctx: 65536, Provider: "together"},
	// Novita AI free models
	"novita/deepseek/deepseek-v4-flash": {Params: 671, Ctx: 131072, Provider: "novita"},
	"novita/qwen/qwen3-235b-a22b": {Params: 235, Ctx: 131072, Provider: "novita"},
	"novita/meta-llama/llama-4-scout-17b-16e-instruct": {Params: 109, Ctx: 10000000, Provider: "novita"},
	// Nebius AI free models
	"nebius/Qwen/Qwen3-235B-A22B": {Params: 235, Ctx: 131072, Provider: "nebius"},
	"nebius/deepseek-ai/DeepSeek-V3": {Params: 671, Ctx: 65536, Provider: "nebius"},
	"nebius/meta-llama/Llama-4-Scout-17B-16E-Instruct": {Params: 109, Ctx: 10000000, Provider: "nebius"},
	// DeepSeek Platform direct
	"deepseek/deepseek-chat": {Params: 671, Ctx: 65536, Provider: "deepseek"},
	"deepseek/deepseek-reasoner": {Params: 671, Ctx: 65536, Provider: "deepseek"},
	// AI21 Labs
	"ai21/jamba-1.6-large": {Params: 396, Ctx: 256000, Provider: "ai21"},
}

func LookupKnownModel(modelID string) (ModelSpec, bool) {
    if spec, ok := KnownModels[modelID]; ok { return spec, true }
    for id, spec := range KnownModels {
        if strings.HasSuffix(modelID, id) || strings.HasSuffix(id, modelID) { return spec, true }
    }
    return ModelSpec{}, false
}

// DeadModels - models known to be 404/dead. Lowercase keys for case-insensitive matching.
var DeadModels = map[string]bool{
	// Old/deprecated models that genuinely no longer exist
	"groq/llama-3.1-70b-versatile": true,
	"groq/mixtral-8x7b-32768": true,
	"01-ai/yi-large": true,
	"adept/fuyu-8b": true,
	"google/gemma-2b": true,
	"google/recurrentgemma-2b": true,
	// Non-serverless models requiring dedicated endpoints (Together AI)
	// Both lowercase and original-case keys needed for IsDeadModel lookup
	"qwen/qwen3-coder-480b-a35b-instruct": true,
	"Qwen/Qwen3-Coder-480B-A35B-Instruct": true,
	"qwen/qwen3-coder-480b-a35b-instruct-fp8": true,
	"Qwen/Qwen3-Coder-480B-A35B-Instruct-FP8": true,
	"deepseek-ai/deepseek-r1-0528-q8_0": true,
	"deepseek-ai/deepseek-v3-0324-fp8": true,
	// Non-serverless models on Together AI (via Hyperbolic/OpenRouter)
	"deepseek-ai/deepseek-r1": true,
	"deepseek/deepseek-r1": true,
	"deepseek-r1": true,
	"DeepSeek-R1": true,
}

// RegisterDeadModel adds a model to the runtime dead-model registry.
// This is thread-safe and persists for the lifetime of the process.
func RegisterDeadModel(modelID string) {
	lower := strings.ToLower(modelID)
	runtimeDeadMu.Lock()
	runtimeDeadModels[lower] = true
	runtimeDeadMu.Unlock()
	// Also add to the static map for consistency
	DeadModels[modelID] = true
}

func IsDeadModel(modelID string) bool {
	lower := strings.ToLower(modelID)
	// Check runtime registry first
	runtimeDeadMu.RLock()
	if runtimeDeadModels[lower] {
		runtimeDeadMu.RUnlock()
		return true
	}
	runtimeDeadMu.RUnlock()
	// Check static registry
	for dead := range DeadModels {
		deadLower := strings.ToLower(dead)
		if lower == deadLower || strings.HasSuffix(lower, "/"+deadLower) {
			return true
		}
	}
	return false
}


var DeadProviders = map[string]bool{
            "nebius": true,
	}

func IsDeadProvider(provider string) bool {
    lower := strings.ToLower(provider)
    if DeadProviders[lower] { return true }
    for dp := range DeadProviders {
        if strings.Contains(lower, dp) { return true }
    }
    return false
}

var GlobalExclusions = []string{
	// Non-chat model types
	"-base", "dummy", "whisper", "orpheus", "flux", "prompt-guard",
	"lyria", "dall", "sdxl", "stable-diffusion", "midjourney", "canopylabs",
	"tts", "asr", "image-gen", "embed", "nomic-embed", "nomic-embed-text",
	"text-embedding", "ocr", "voxtral", "moderation", "nemotron-parse",
	"nemoretriever", "bge-m3", "deplot", "kosmos-2", "nvclip",
	"nemotron-4-340b-reward", "reward", "ai-synthetic-video", "phi-3-vision",
	"labs-", "content-safety", "nemotron-3", "nemotron-3.5",
	// Meta-router models
	"openrouter/free", "openrouter/auto", "openrouter/default",
	"/free", ":free", "/auto",
	// Utility/non-chat
	"gliner", "sarvam", "laguna", "pii",
}

func IsExcluded(modelID string) bool {
	lower := strings.ToLower(modelID)
	for _, exc := range GlobalExclusions {
		if strings.Contains(lower, exc) {
			return true
		}
	}
	// Filter small models only if the parameter size is explicitly in the name.
	// Matches "-8b", "-70b", "_7b" (with separator), NOT "3.1" version numbers.
	sizeMatch := ExplicitSizePattern.FindStringSubmatch(lower)
	if len(sizeMatch) > 1 {
		params, err := strconv.Atoi(sizeMatch[1])
		if err == nil && params < 150 {
			return true
		}
	}
	return false
}

// StreamTimeoutProviders need longer streaming timeouts.
var StreamTimeoutProviders = map[string]bool{
    "nvidia_nim": true, "nvidia": true, "openrouter": true,
    "sambanova": true,
}

// IsSmallModel returns true if the model name contains a parameter size
// indicator (e.g., "8b", "70b") and it is less than 150B parameters.
// Models without a size indicator in their name are not filtered.
func IsSmallModel(modelID string) bool {
	re := regexp.MustCompile(`(?i)(\d+)\s*[bB]`)
	matches := re.FindStringSubmatch(modelID)
	if len(matches) < 2 {
		return false // no size indicator in name
	}
	params, err := strconv.Atoi(matches[1])
	if err != nil {
		return false
	}
	return params < 150
}

func NeedsStreamTimeout(provider string) bool {
    return StreamTimeoutProviders[strings.ToLower(provider)]
}
