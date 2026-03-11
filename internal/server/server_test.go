package server

import (
	"testing"
)

func TestGetProviderForModel(t *testing.T) {
	tests := []struct {
		model    string
		expected string
	}{
		{"claude-opus-4-6", "anthropic"},
		{"claude-haiku-4-5-20251001", "anthropic"},
		{"gpt-4o", "openai"},
		{"gpt-4.5-preview", "openai"},
		{"o1", "openai"},
		{"o3-mini", "openai"},
		{"gemini-2.5-flash", "gemini"},
		{"gemini-3.0-pro", "gemini"},
		{"lux", "lux"},
		{"unknown-model", ""},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			actual := getProviderForModel(tt.model)
			if actual != tt.expected {
				t.Errorf("getProviderForModel(%q) = %q, expected %q", tt.model, actual, tt.expected)
			}
		})
	}
}
