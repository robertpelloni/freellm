package engine

import (
	"strings"
)

type ModelSpec struct {
	Params   int    `json:"params"`
	Ctx      int    `json:"ctx"`
	Provider string `json:"provider"`
}

var KnownModels = map[string]ModelSpec{
	"github/Meta-Llama-3.1-405B-Instruct": {Params: 405, Ctx: 128000, Provider: "github"},
	"openrouter/nvidia/nemotron-3-super-120b-a12b:free": {Params: 120, Ctx: 4096, Provider: "openrouter"},
	"nvidia/meta/llama-3.3-70b-instruct": {Params: 70, Ctx: 128000, Provider: "nvidia"},
	"groq/llama-3.3-70b-versatile": {Params: 70, Ctx: 128000, Provider: "groq"},
	"deepinfra/meta-llama/Meta-Llama-3.1-405B-Instruct": {Params: 405, Ctx: 131072, Provider: "deepinfra"},
	"huggingface/meta-llama/Llama-3.1-405B-Instruct": {Params: 405, Ctx: 131072, Provider: "huggingface"},
}

func LookupKnownModel(modelID string) (ModelSpec, bool) {
	// Exact match
	if spec, ok := KnownModels[modelID]; ok {
		return spec, true
	}

	// Tail match
	for id, spec := range KnownModels {
		if strings.HasSuffix(modelID, id) || strings.HasSuffix(id, modelID) {
			return spec, true
		}
	}

	return ModelSpec{}, false
}

var GlobalExclusions = []string{
	"-base", "dummy", "whisper", "orpheus", "flux", "prompt-guard",
	"lyria", "dall", "sdxl", "stable-diffusion", "midjourney",
}

func IsExcluded(modelID string) bool {
	lowerID := strings.ToLower(modelID)
	for _, exc := range GlobalExclusions {
		if strings.Contains(lowerID, exc) {
			return true
		}
	}
	return false
}
