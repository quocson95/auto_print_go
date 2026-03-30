package config

import (
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
	viper.SetDefault("S3_REGION", "ap-southeast-1")
	viper.SetDefault("LOG_LEVEL", "INFO")
	viper.SetDefault("LOG_FILE", "logs/automation.log")

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
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
