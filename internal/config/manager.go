package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ModelList        []ModelEntry           `yaml:"model_list"`
	RouterSettings   map[string]interface{} `yaml:"router_settings"`
	LiteLLMSettings  map[string]interface{} `yaml:"litellm_settings"`
	Port             int                    `yaml:"port"`
}

type ModelEntry struct {
	ModelName      string                 `yaml:"model_name"`
	LiteLLMParams  map[string]interface{} `yaml:"litellm_params"`
	ModelInfo      map[string]interface{} `yaml:"model_info"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func SaveConfig(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}
