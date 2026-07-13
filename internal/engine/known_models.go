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
	// Replicate models
	"replicate/meta/llama-4-scout-17b-16e-instruct": {Params: 109, Ctx: 10000000, Provider: "replicate"},
	"replicate/deepseek-ai/deepseek-v4": {Params: 685, Ctx: 128000, Provider: "replicate"},
	"replicate/qwen/qwen3-235b-a22b": {Params: 235, Ctx: 128000, Provider: "replicate"},
	// DashScope (Qwen/Alibaba) models
	"dashscope/qwen3.6-max": {Params: 235, Ctx: 1000000, Provider: "dashscope"},
	"dashscope/qwen3.6-plus": {Params: 235, Ctx: 1000000, Provider: "dashscope"},
	"dashscope/qwen3-235b-a22b": {Params: 235, Ctx: 128000, Provider: "dashscope"},
	"dashscope/qwen-plus": {Params: 235, Ctx: 128000, Provider: "dashscope"},
	"dashscope/qwen-turbo": {Params: 72, Ctx: 128000, Provider: "dashscope"},
	"dashscope/deepseek-v4": {Params: 685, Ctx: 128000, Provider: "dashscope"},
	"dashscope/deepseek-v4-flash": {Params: 685, Ctx: 1000000, Provider: "dashscope"},
	// MiniMax models
	"minimax/MiniMax-M3": {Params: 456, Ctx: 1000000, Provider: "minimax"},
	"minimax/minimax-m3": {Params: 456, Ctx: 1000000, Provider: "minimax"},
	// Moonshot (Kimi) models
	"moonshot/moonshot-v1-128k": {Params: 200, Ctx: 128000, Provider: "moonshot"},
	"moonshot/moonshot-v1-32k": {Params: 200, Ctx: 32000, Provider: "moonshot"},
	"moonshot/kimi-k2.6": {Params: 1000, Ctx: 128000, Provider: "moonshot"},
	// StepFun models
	"stepfun/step-3.7-flash": {Params: 200, Ctx: 128000, Provider: "stepfun"},
	"stepfun/step-3.7-flash-15b": {Params: 15, Ctx: 128000, Provider: "stepfun"},
	// Zhipu AI (GLM) models
	"zhipu/glm-5.2": {Params: 744, Ctx: 1000000, Provider: "zhipu"},
	"zhipu/glm-5.1": {Params: 744, Ctx: 128000, Provider: "zhipu"},
	"zhipu/glm-4-plus": {Params: 130, Ctx: 128000, Provider: "zhipu"},
	"zhipu/glm-4-flash": {Params: 130, Ctx: 128000, Provider: "zhipu"},
	"openrouter/z-ai/glm-5.2": {Params: 744, Ctx: 1000000, Provider: "openrouter"},
	"opencode_zen/glm-5.2": {Params: 744, Ctx: 1000000, Provider: "opencode_zen"},
	// InternLM (Shanghai AI Lab) models
	"internlm/internlm3-latest": {Params: 200, Ctx: 128000, Provider: "internlm"},
	"internlm/internlm3-20b": {Params: 20, Ctx: 128000, Provider: "internlm"},
	// Arcee AI models
	"arcee/trinity": {Params: 400, Ctx: 128000, Provider: "arcee"},
	"arcee/arcee-flash": {Params: 70, Ctx: 128000, Provider: "arcee"},
	// Perplexity models
	"perplexity/sonar": {Params: 8, Ctx: 128000, Provider: "perplexity"},
	"perplexity/sonar-pro": {Params: 70, Ctx: 200000, Provider: "perplexity"},
	"perplexity/sonar-reasoning": {Params: 70, Ctx: 128000, Provider: "perplexity"},
	"perplexity/r1-1776": {Params: 685, Ctx: 128000, Provider: "perplexity"},
	// xAI (Grok) models
	"xai/grok-3-mini": {Params: 314, Ctx: 131072, Provider: "xai"},
	"xai/grok-3-fast": {Params: 314, Ctx: 131072, Provider: "xai"},
	"xai/grok-4.20": {Params: 500, Ctx: 128000, Provider: "xai"},
	// Tencent Hunyuan models
	"hunyuan/hunyuan-lite": {Params: 70, Ctx: 256000, Provider: "hunyuan"},
	"hunyuan/hunyuan-standard": {Params: 70, Ctx: 128000, Provider: "hunyuan"},
	"hunyuan/hunyuan-pro": {Params: 200, Ctx: 128000, Provider: "hunyuan"},
	"hunyuan/hunyuan-turbos": {Params: 70, Ctx: 128000, Provider: "hunyuan"},
	// June 2026 Verified Models
	"google/gemini-2.5-flash": {Params: 200, Ctx: 1000000, Provider: "gemini"},
	"google/gemini-2.5-flash-lite": {Params: 20, Ctx: 1000000, Provider: "gemini"},
	"google/gemma-3-27b": {Params: 27, Ctx: 128000, Provider: "gemini"},
	"xai/grok-3": {Params: 314, Ctx: 131072, Provider: "xai"},
	"mistral/codestral-latest": {Params: 22, Ctx: 32000, Provider: "mistral"},
	"cloudflare/llama-4-scout-17b": {Params: 17, Ctx: 128000, Provider: "cloudflare"},
	"cloudflare/qwen-qwq-32b": {Params: 32, Ctx: 128000, Provider: "cloudflare"},
	"llm7/deepseek-r1": {Params: 671, Ctx: 128000, Provider: "llm7"},
	"cerebras/qwen3-235b": {Params: 235, Ctx: 131072, Provider: "cerebras"},
	"groq/llama-4-scout": {Params: 17, Ctx: 128000, Provider: "groq"},
	"groq/kimi-k2": {Params: 100, Ctx: 128000, Provider: "groq"},
	// Kluster AI
	"kluster/DeepSeek-R1": {Params: 671, Ctx: 128000, Provider: "kluster"},
	"kluster/Qwen3-235B-A22B": {Params: 235, Ctx: 128000, Provider: "kluster"},
	// Lepton AI
	"lepton/llama-3.3-70b": {Params: 70, Ctx: 128000, Provider: "lepton"},
	// Pollinations AI
	"pollinations/openai/gpt-4o": {Params: 175, Ctx: 128000, Provider: "pollinations"},
	"pollinations/deepseek-r1": {Params: 671, Ctx: 128000, Provider: "pollinations"},
}

func LookupKnownModel(modelID string) (ModelSpec, bool) {
	if spec, ok := KnownModels[modelID]; ok { return spec, true }
	for id, spec := range KnownModels {
		if strings.HasSuffix(modelID, id) || strings.HasSuffix(id, modelID) { return spec, true }
	}

	// Dynamic fallback estimator for Go
	lower := strings.ToLower(modelID)
	// DeepSeek
	if strings.Contains(lower, "deepseek") {
		if strings.Contains(lower, "reasoner") || strings.Contains(lower, "v3") || strings.Contains(lower, "v4") || strings.Contains(lower, "chat") {
			return ModelSpec{Params: 671, Ctx: 64000}, true
		}
		if strings.Contains(lower, "r1-distill-qwen-32") || strings.Contains(lower, "distill-qwen-32") {
			return ModelSpec{Params: 32, Ctx: 32000}, true
		}
		if strings.Contains(lower, "r1-distill-llama-70") || strings.Contains(lower, "distill-llama-70") {
			return ModelSpec{Params: 70, Ctx: 64000}, true
		}
	}
	// Gemini
	if strings.Contains(lower, "gemini") {
		if strings.Contains(lower, "pro") {
			return ModelSpec{Params: 200, Ctx: 2000000, Provider: "gemini"}, true
		}
		if strings.Contains(lower, "flash") || strings.Contains(lower, "lite") {
			return ModelSpec{Params: 20, Ctx: 1000000, Provider: "gemini"}, true
		}
	}
	// Claude
	if strings.Contains(lower, "claude") {
		if strings.Contains(lower, "opus") {
			return ModelSpec{Params: 300, Ctx: 200000, Provider: "anthropic"}, true
		}
		if strings.Contains(lower, "sonnet") {
			return ModelSpec{Params: 175, Ctx: 200000, Provider: "anthropic"}, true
		}
		if strings.Contains(lower, "haiku") {
			return ModelSpec{Params: 15, Ctx: 200000, Provider: "anthropic"}, true
		}
	}
	// GPT / o-series
	if strings.Contains(lower, "gpt") || strings.Contains(lower, "o1") || strings.Contains(lower, "o3") || strings.Contains(lower, "o4") || strings.Contains(lower, "o-") {
		if strings.Contains(lower, "mini") {
			return ModelSpec{Params: 8, Ctx: 128000, Provider: "openai"}, true
		}
		if strings.Contains(lower, "gpt-4o") || strings.Contains(lower, "gpt-4-turbo") {
			return ModelSpec{Params: 175, Ctx: 128000, Provider: "openai"}, true
		}
		if strings.Contains(lower, "o1") {
			return ModelSpec{Params: 200, Ctx: 200000, Provider: "openai"}, true
		}
	}
	// Qwen Max/Plus/Turbo
	if strings.Contains(lower, "qwen") {
		if strings.Contains(lower, "max") {
			return ModelSpec{Params: 300, Ctx: 32000}, true
		}
		if strings.Contains(lower, "plus") {
			return ModelSpec{Params: 72, Ctx: 32000}, true
		}
		if strings.Contains(lower, "turbo") {
			return ModelSpec{Params: 14, Ctx: 32000}, true
		}
	}
	// GLM (Zhipu AI)
	if strings.Contains(lower, "glm") {
		if strings.Contains(lower, "5.2") {
			return ModelSpec{Params: 744, Ctx: 1000000}, true
		}
		if strings.Contains(lower, "5.1") || strings.Contains(lower, "5") {
			return ModelSpec{Params: 744, Ctx: 128000}, true
		}
		if strings.Contains(lower, "4") {
			return ModelSpec{Params: 130, Ctx: 128000}, true
		}
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
	"deepseek-ai/DeepSeek-R1-0528": true,
	"deepseek-ai/DeepSeek-V3-0324": true,
	// Speculative/non-existent June 2026 models (404ing)
	"openai/gpt-5": true,
	"openai/gpt-5.1": true,
	"openai/gpt-5.2": true,
	"openai/gpt-5.5": true,
	"openai/gpt-5-mini": true,
	"xai/grok-4.20": true,
	"xai/grok-4.3": true,
	"anthropic/claude-opus-4-6": true,
	"anthropic/claude-opus-4-7": true,
	"anthropic/claude-opus-4-8": true,
	"anthropic/claude-sonnet-4-6": true,
	"anthropic/claude-haiku-4-5": true,
	"sonar-reasoning": true,
	"aion-labs/aion-1.0": true,
	"r1-1776": true,
	"mistralai/mistral-small-4-119b-2603": true,
	"openai/gpt-oss-120b": true,
	"anthropic/claude-opus-4-5": true,
	"anthropic/claude-sonnet-4-5": true,
	"google/gemini-3.1-pro-preview": true,
	"deepseek-ai/deepseek-v4": true,
	"meta/llama-4-scout-17b-16e-instruct": true,
	"llama-4-scout": true,
	"llama-4-scout-17b": true,
	"qwen3.5-397b-a17b": false, // Keep this one as it works on nvidia
	"qwen3.5-plus-2026-02-15": true,
	"microsoft/phi-4-multimodal-instruct": true, // Nvidia endpoint is returning 400 DEGRADED
	"minimaxai/minimax-m3": true,                // Nvidia endpoint is returning 500 Internal Server Error
	"mistralai/mistral-medium-3.5-128b": true,   // Failing frequently on Nvidia
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
		// Match exact, suffix, or contains for these specific problematic strings
		if lower == deadLower || 
		   strings.HasSuffix(lower, "/"+deadLower) || 
		   (len(deadLower) > 10 && strings.Contains(lower, deadLower)) {
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
		if err == nil && params < 7 {
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
// indicator (e.g., "8b", "70b") and it is less than 7B parameters.
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
	return params < 7
}

func NeedsStreamTimeout(provider string) bool {
    return StreamTimeoutProviders[strings.ToLower(provider)]
}
