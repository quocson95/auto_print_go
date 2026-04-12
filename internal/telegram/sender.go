package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sondq/auto_print/internal/config"
)

const telegramMaxMessageLen = 4096

type Sender struct {
	botToken   string
	chatID     string
	errChatID  string
	httpClient *http.Client
}

func NewSender(cfg *config.Config) *Sender {
	return &Sender{
		botToken:  cfg.TelegramBotToken,
		chatID:    cfg.TelegramChatID,
		errChatID: cfg.TelegramChatIDErr,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

func (s *Sender) apiURL(method string) string {
	return fmt.Sprintf("https://api.telegram.org/bot%s/%s", s.botToken, method)
}

func (s *Sender) SendCombinedMessage(ctx context.Context, subject, pcURL, mobileURL, thumbnailURL string, summary []string) (bool, error) {
	return retryTelegram(ctx, 10, 2*time.Second, func() (bool, error) {
		return s.sendCombinedOnce(ctx, subject, pcURL, mobileURL, thumbnailURL, summary)
	})
}

func (s *Sender) sendCombinedOnce(ctx context.Context, subject, pcURL, mobileURL, thumbnailURL string, summary []string) (bool, error) {
	message := buildCombinedMessage(subject, pcURL, mobileURL, summary, time.Now())

	if thumbnailURL != "" {
		ok, err := s.sendPhoto(ctx, s.chatID, thumbnailURL, message)
		if err != nil {
			slog.Warn("Failed to send thumbnail, falling back to text", "error", err, "url", thumbnailURL)
		} else if ok {
			slog.Info("Combined message sent successfully to Telegram")
			return true, nil
		}
	}

	ok, err := s.sendMessage(ctx, s.chatID, message, false)
	if err != nil {
		return false, err
	}
	if ok {
		slog.Info("Combined message sent successfully to Telegram")
	}
	return ok, nil
}

func (s *Sender) SendPDF(ctx context.Context, pdfPath, subject, pdfType string) (bool, error) {
	return retryTelegram(ctx, 10, 2*time.Second, func() (bool, error) {
		return s.sendPDFOnce(ctx, pdfPath, subject, pdfType)
	})
}

func (s *Sender) sendPDFOnce(ctx context.Context, pdfPath, subject, pdfType string) (bool, error) {
	slog.Info("Sending PDF directly", "file", filepath.Base(pdfPath), "type", pdfType)

	typeIcon := "\U0001f4bb"
	typeLabel := "PC"
	if pdfType == "mobile" {
		typeIcon = "\U0001f4f1"
		typeLabel = "Mobile"
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	caption := fmt.Sprintf("*Bài viết:* %s\n\n%s *Phiên bản %s*\n*Thời gian:* %s", escapeMarkdown(subject), typeIcon, typeLabel, timestamp)

	f, err := os.Open(pdfPath)
	if err != nil {
		return false, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	if err := w.WriteField("chat_id", s.chatID); err != nil {
		return false, err
	}
	if err := w.WriteField("caption", caption); err != nil {
		return false, err
	}
	if err := w.WriteField("parse_mode", "Markdown"); err != nil {
		return false, err
	}

	part, err := w.CreateFormFile("document", filepath.Base(pdfPath))
	if err != nil {
		return false, err
	}
	if _, err := io.Copy(part, f); err != nil {
		return false, err
	}
	if err := w.Close(); err != nil {
		return false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL("sendDocument"), &buf)
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("send document: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("telegram error: %s", body)
	}

	slog.Info("PDF sent successfully to Telegram")
	return true, nil
}

func (s *Sender) SendErrorNotification(ctx context.Context, errMsg string) {
	message := fmt.Sprintf("\u26a0\ufe0f *Email Automation Error*\n\n%s", errMsg)
	ok, err := retryTelegram(ctx, 2, time.Second, func() (bool, error) {
		return s.sendMessage(ctx, s.errChatID, message, true)
	})
	if err != nil || !ok {
		slog.Error("Failed to send error notification", "error", err)
	} else {
		slog.Info("Error notification sent to Telegram")
	}
}

func (s *Sender) sendMessage(ctx context.Context, chatID, text string, disablePreview bool) (bool, error) {
	payload := map[string]interface{}{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "Markdown",
	}
	if disablePreview {
		payload["disable_web_page_preview"] = true
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL("sendMessage"), bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("telegram error: %s", respBody)
	}

	return true, nil
}

func (s *Sender) sendPhoto(ctx context.Context, chatID, photoURL, caption string) (bool, error) {
	payload := map[string]interface{}{
		"chat_id":    chatID,
		"photo":      photoURL,
		"caption":    caption,
		"parse_mode": "Markdown",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL("sendPhoto"), bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("send photo: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("telegram error: %s", respBody)
	}

	return true, nil
}

func retryTelegram(ctx context.Context, maxRetries int, initialDelay time.Duration, fn func() (bool, error)) (bool, error) {
	delay := initialDelay
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		ok, err := fn()
		if err == nil {
			return ok, nil
		}

		lastErr = err
		if attempt < maxRetries {
			slog.Warn("Telegram API error, retrying",
				"attempt", attempt+1,
				"maxRetries", maxRetries+1,
				"error", err,
				"retryIn", delay,
			)
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(delay):
			}
			delay *= 2
		}
	}

	return false, fmt.Errorf("all retries failed: %w", lastErr)
}

func buildCombinedMessage(subject, pcURL, mobileURL string, summary []string, now time.Time) string {
	loc := time.FixedZone("GMT+7", 7*3600)
	timestamp := now.In(loc).Format("2006-01-02 15:04:05")
	safeSubject := truncateRunes(escapeMarkdown(subject), 200)

	base := fmt.Sprintf("\U0001f4f0 *%s*\n\U0001f550 %s", safeSubject, timestamp)
	links := fmt.Sprintf(
		"\n\n\U0001f4f1 [Tải PDF cho Mobile](%s)\n\U0001f4bb [Tải PDF cho PC](%s)\n\n\U0001f4da [Tất cả tài liệu](https://library.oneblock.vn)",
		mobileURL,
		pcURL,
	)

	bullets := fitSummaryBullets(base, links, sanitizeSummary(summary), telegramMaxMessageLen)
	return base + buildSummarySection(bullets) + links
}

func sanitizeSummary(summary []string) []string {
	cleaned := make([]string, 0, len(summary))
	for _, bullet := range summary {
		bullet = strings.TrimSpace(bullet)
		if bullet == "" {
			continue
		}
		cleaned = append(cleaned, escapeMarkdown(bullet))
	}
	return cleaned
}

func fitSummaryBullets(base, links string, bullets []string, limit int) []string {
	if len([]rune(base+links)) >= limit {
		return nil
	}

	var kept []string
	for _, bullet := range bullets {
		candidate := append(append([]string(nil), kept...), bullet)
		if len([]rune(base+buildSummarySection(candidate)+links)) <= limit {
			kept = candidate
			continue
		}

		if len(kept) > 0 {
			break
		}

		overhead := len([]rune(base + buildSummarySection([]string{""}) + links))
		available := limit - overhead
		if available <= 1 {
			return nil
		}

		kept = []string{truncateRunes(bullet, available)}
		break
	}

	return kept
}

func buildSummarySection(summary []string) string {
	if len(summary) == 0 {
		return ""
	}

	lines := make([]string, 0, len(summary)+1)
	lines = append(lines, "\n\n*Tóm tắt*")
	for _, bullet := range summary {
		lines = append(lines, "- "+bullet)
	}

	return strings.Join(lines, "\n")
}

func escapeMarkdown(input string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`_`, `\_`,
		`*`, `\*`,
		"[", `\[`,
		"`", "\\`",
	)
	return replacer.Replace(input)
}

func truncateRunes(input string, limit int) string {
	if limit <= 0 {
		return ""
	}

	runes := []rune(input)
	if len(runes) <= limit {
		return input
	}
	if limit == 1 {
		return "…"
	}

	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}
