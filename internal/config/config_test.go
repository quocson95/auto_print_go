package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestLoadEnablesGeminiWhenAPIKeyPresent(t *testing.T) {
	resetViper(t)
	chdirTemp(t)

	t.Setenv("GEMINI_API_KEY", "secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !cfg.GeminiEnabled {
		t.Fatalf("expected Gemini to be enabled when API key is present")
	}
	if cfg.GeminiModel != "gemini-2.5-flash" {
		t.Fatalf("unexpected default model: %q", cfg.GeminiModel)
	}
	if cfg.GeminiTimeoutSeconds != 30 {
		t.Fatalf("unexpected timeout default: %d", cfg.GeminiTimeoutSeconds)
	}
	if cfg.GeminiMaxInputChars != 12000 {
		t.Fatalf("unexpected max input chars default: %d", cfg.GeminiMaxInputChars)
	}
}

func TestValidateRequiresGeminiAPIKeyWhenEnabled(t *testing.T) {
	outputDir := filepath.Join(t.TempDir(), "output")
	cfg := &Config{
		EmailAddress:         "user@example.com",
		EmailPassword:        "secret",
		TelegramBotToken:     "bot-token",
		TelegramChatID:       "chat-id",
		PDFOutputDir:         outputDir,
		GeminiEnabled:        true,
		GeminiModel:          "gemini-2.5-flash",
		GeminiTimeoutSeconds: 30,
		GeminiMaxInputChars:  12000,
	}

	err := cfg.Validate()
	if err == nil || err.Error() != "missing required configuration: GEMINI_API_KEY" {
		t.Fatalf("Validate() error = %v, want missing GEMINI_API_KEY", err)
	}
}

func resetViper(t *testing.T) {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
}

func chdirTemp(t *testing.T) {
	t.Helper()

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}

	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir(%q) error = %v", tempDir, err)
	}

	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})
}
