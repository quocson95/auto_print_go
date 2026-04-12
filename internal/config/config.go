package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	EmailProvider string `mapstructure:"EMAIL_PROVIDER"`
	EmailAddress  string `mapstructure:"EMAIL_ADDRESS"`
	EmailPassword string `mapstructure:"EMAIL_PASSWORD"`
	IMAPServer    string `mapstructure:"IMAP_SERVER"`
	IMAPPort      int    `mapstructure:"IMAP_PORT"`

	SenderEmails       []string
	ViewBrowserKeyword string `mapstructure:"VIEW_BROWSER_KEYWORD"`

	TelegramBotToken  string `mapstructure:"TELEGRAM_BOT_TOKEN"`
	TelegramChatID    string `mapstructure:"TELEGRAM_CHAT_ID"`
	TelegramChatIDErr string `mapstructure:"TELEGRAM_CHAT_ID_ERROR"`

	GeminiEnabled        bool   `mapstructure:"GEMINI_ENABLED"`
	GeminiAPIKey         string `mapstructure:"GEMINI_API_KEY"`
	GeminiModel          string `mapstructure:"GEMINI_MODEL"`
	GeminiTimeoutSeconds int    `mapstructure:"GEMINI_TIMEOUT_SECONDS"`
	GeminiMaxInputChars  int    `mapstructure:"GEMINI_MAX_INPUT_CHARS"`

	CheckIntervalSeconds int `mapstructure:"CHECK_INTERVAL_SECONDS"`
	MaxEmailsToCheck     int `mapstructure:"MAX_EMAILS_TO_CHECK"`

	PDFOutputDir     string `mapstructure:"PDF_OUTPUT_DIR"`
	PDFRetentionDays int    `mapstructure:"PDF_RETENTION_DAYS"`

	S3BucketName       string `mapstructure:"S3_BUCKET_NAME"`
	S3Region           string `mapstructure:"S3_REGION"`
	AWSAccessKeyID     string `mapstructure:"AWS_ACCESS_KEY_ID"`
	AWSSecretAccessKey string `mapstructure:"AWS_SECRET_ACCESS_KEY"`
	S3EndpointURL      string `mapstructure:"S3_ENDPOINT_URL"`

	LogLevel string `mapstructure:"LOG_LEVEL"`
	LogFile  string `mapstructure:"LOG_FILE"`
}

func Load() (*Config, error) {
	viper.SetConfigFile(".env")
	viper.SetConfigType("env")
	viper.AutomaticEnv()

	viper.SetDefault("EMAIL_PROVIDER", "gmail")
	viper.SetDefault("IMAP_SERVER", "imap.gmail.com")
	viper.SetDefault("IMAP_PORT", 993)
	viper.SetDefault("VIEW_BROWSER_KEYWORD", "View in Browser")
	viper.SetDefault("SENDER_EMAILS", "dtoan.bui@gmail.com")
	viper.SetDefault("CHECK_INTERVAL_SECONDS", 60)
	viper.SetDefault("MAX_EMAILS_TO_CHECK", 10)
	viper.SetDefault("PDF_OUTPUT_DIR", "output")
	viper.SetDefault("PDF_RETENTION_DAYS", 7)
	viper.SetDefault("GEMINI_MODEL", "gemini-2.5-flash")
	viper.SetDefault("GEMINI_TIMEOUT_SECONDS", 30)
	viper.SetDefault("GEMINI_MAX_INPUT_CHARS", 12000)
	viper.SetDefault("S3_REGION", "ap-southeast-1")
	viper.SetDefault("LOG_LEVEL", "INFO")
	viper.SetDefault("LOG_FILE", "logs/automation.log")

	for _, key := range []string{
		"EMAIL_PROVIDER",
		"EMAIL_ADDRESS",
		"EMAIL_PASSWORD",
		"IMAP_SERVER",
		"IMAP_PORT",
		"SENDER_EMAILS",
		"VIEW_BROWSER_KEYWORD",
		"TELEGRAM_BOT_TOKEN",
		"TELEGRAM_CHAT_ID",
		"TELEGRAM_CHAT_ID_ERROR",
		"GEMINI_ENABLED",
		"GEMINI_API_KEY",
		"GEMINI_MODEL",
		"GEMINI_TIMEOUT_SECONDS",
		"GEMINI_MAX_INPUT_CHARS",
		"CHECK_INTERVAL_SECONDS",
		"MAX_EMAILS_TO_CHECK",
		"PDF_OUTPUT_DIR",
		"PDF_RETENTION_DAYS",
		"S3_BUCKET_NAME",
		"S3_REGION",
		"AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY",
		"S3_ENDPOINT_URL",
		"LOG_LEVEL",
		"LOG_FILE",
	} {
		if err := viper.BindEnv(key); err != nil {
			return nil, fmt.Errorf("binding env %s: %w", key, err)
		}
	}

	if err := viper.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) && !os.IsNotExist(err) {
			return nil, fmt.Errorf("reading config: %w", err)
		}
		slog.Warn("No .env file found, using environment variables only")
	}

	cfg := &Config{}
	if err := viper.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	raw := viper.GetString("SENDER_EMAILS")
	for _, e := range strings.Split(raw, ",") {
		e = strings.TrimSpace(e)
		if e != "" {
			cfg.SenderEmails = append(cfg.SenderEmails, e)
		}
	}

	if cfg.TelegramChatIDErr == "" {
		cfg.TelegramChatIDErr = cfg.TelegramChatID
	}

	if !viper.IsSet("GEMINI_ENABLED") {
		cfg.GeminiEnabled = cfg.GeminiAPIKey != ""
	}

	return cfg, nil
}

func (c *Config) Validate() error {
	required := map[string]string{
		"EMAIL_ADDRESS":      c.EmailAddress,
		"EMAIL_PASSWORD":     c.EmailPassword,
		"TELEGRAM_BOT_TOKEN": c.TelegramBotToken,
		"TELEGRAM_CHAT_ID":   c.TelegramChatID,
	}

	var missing []string
	for k, v := range required {
		if v == "" {
			missing = append(missing, k)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}

	if c.GeminiEnabled {
		if c.GeminiAPIKey == "" {
			return fmt.Errorf("missing required configuration: GEMINI_API_KEY")
		}
		if c.GeminiTimeoutSeconds <= 0 {
			return fmt.Errorf("GEMINI_TIMEOUT_SECONDS must be greater than 0")
		}
		if c.GeminiMaxInputChars <= 0 {
			return fmt.Errorf("GEMINI_MAX_INPUT_CHARS must be greater than 0")
		}
		if strings.TrimSpace(c.GeminiModel) == "" {
			return fmt.Errorf("GEMINI_MODEL must not be empty when Gemini is enabled")
		}
	}

	if err := os.MkdirAll(c.PDFOutputDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}

	return nil
}

func (c *Config) SlogLevel() slog.Level {
	switch strings.ToUpper(c.LogLevel) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
