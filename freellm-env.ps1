# FreeLLM Proxy Environment Configuration
# Source this file: . ./freellm-env.ps1

# === Proxy endpoints ===
$env:OPENAI_BASE_URL = "http://localhost:4000/v1"
$env:OPENAI_API_KEY = "sk-freellm"
$env:ANTHROPIC_BASE_URL = "http://localhost:4000"
$env:ANTHROPIC_API_KEY = "sk-freellm-proxy"
$env:GEMINI_API_KEY = ""

# === Claude Code model aliases (all route through FreeLLM) ===
$env:ANTHROPIC_MODEL = "claude-sonnet-4-20250514"
$env:ANTHROPIC_SMALL_FAST_MODEL = "claude-haiku-4-20250514"
$env:ANTHROPIC_DEFAULT_SONNET_MODEL = "claude-sonnet-4-20250514"
$env:ANTHROPIC_DEFAULT_OPUS_MODEL = "claude-opus-4-20250514"
$env:ANTHROPIC_DEFAULT_HAIKU_MODEL = "claude-haiku-4-20250514"

# === Provider API keys (fill in your keys) ===
$env:GEMINI_API_KEY = $env:GEMINI_API_KEY
$env:OPENROUTER_API_KEY = $env:OPENROUTER_API_KEY
$env:GROQ_API_KEY = $env:GROQ_API_KEY
$env:CEREBRAS_API_KEY = $env:CEREBRAS_API_KEY
$env:NVIDIA_NIM_API_KEY = $env:NVIDIA_NIM_API_KEY
$env:GITHUB_TOKEN = $env:GITHUB_TOKEN
$env:DEEPINFRA_API_KEY = $env:DEEPINFRA_API_KEY
$env:MISTRAL_API_KEY = $env:MISTRAL_API_KEY
$env:SAMBANOVA_API_KEY = $env:SAMBANOVA_API_KEY
$env:FIREWORKS_API_KEY = $env:FIREWORKS_API_KEY
$env:HYPERBOLIC_API_KEY = $env:HYPERBOLIC_API_KEY
$env:COHERE_API_KEY = $env:COHERE_API_KEY

# === New providers (from awesome-free-models) ===
$env:SILICONFLOW_API_KEY = $env:SILICONFLOW_API_KEY
$env:TOGETHER_API_KEY = $env:TOGETHER_API_KEY
$env:NOVITA_API_KEY = $env:NOVITA_API_KEY
$env:NEBIUS_API_KEY = $env:NEBIUS_API_KEY
$env:DEEPSEEK_API_KEY = $env:DEEPSEEK_API_KEY
$env:AI21_API_KEY = $env:AI21_API_KEY
# === Additional providers (from awesome-free-models v2) ===
$env:REPLICATE_API_TOKEN = $env:REPLICATE_API_TOKEN
$env:DASHSCOPE_API_KEY = $env:DASHSCOPE_API_KEY
$env:MINIMAX_API_KEY = $env:MINIMAX_API_KEY
$env:MOONSHOT_API_KEY = $env:MOONSHOT_API_KEY
$env:STEPFUN_API_KEY = $env:STEPFUN_API_KEY
$env:ZHIPU_API_KEY = $env:ZHIPU_API_KEY
$env:INTERNLM_API_KEY = $env:INTERNLM_API_KEY
$env:ARCEE_API_KEY = $env:ARCEE_API_KEY
$env:PERPLEXITY_API_KEY = $env:PERPLEXITY_API_KEY
$env:XAI_API_KEY = $env:XAI_API_KEY
$env:HUNYUAN_API_KEY = $env:HUNYUAN_API_KEY

Write-Host "FreeLLM proxy environment configured."
Write-Host "  OpenAI:      $env:OPENAI_BASE_URL"
Write-Host "  Anthropic:   $env:ANTHROPIC_BASE_URL"
Write-Host "  Claude Code: models -> FreeLLM routing"
