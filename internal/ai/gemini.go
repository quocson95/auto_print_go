package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/sondq/auto_print/internal/config"
)

const defaultGeminiBaseURL = "https://generativelanguage.googleapis.com"

type Summarizer struct {
	enabled       bool
	apiKey        string
	model         string
	maxInputChars int
	httpClient    *http.Client
	baseURL       string
	retryCount    int
	initialDelay  time.Duration
}

type generateContentRequest struct {
	Contents []geminiContent `json:"contents"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text,omitempty"`
}

type generateContentResponse struct {
	Candidates []struct {
		Content geminiContent `json:"content"`
	} `json:"candidates"`
}

type geminiAPIError struct {
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func NewSummarizer(cfg *config.Config) *Summarizer {
	timeout := time.Duration(cfg.GeminiTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return &Summarizer{
		enabled:       cfg.GeminiEnabled && cfg.GeminiAPIKey != "",
		apiKey:        cfg.GeminiAPIKey,
		model:         cfg.GeminiModel,
		maxInputChars: cfg.GeminiMaxInputChars,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		baseURL:      defaultGeminiBaseURL,
		retryCount:   2,
		initialDelay: 2 * time.Second,
	}
}

func (s *Summarizer) Enabled() bool {
	return s != nil && s.enabled
}

func (s *Summarizer) Summarize(ctx context.Context, pageText string) ([]string, error) {
	if !s.Enabled() {
		return nil, nil
	}

	input := sanitizeInput(pageText, s.maxInputChars)
	if input == "" {
		return nil, fmt.Errorf("no page text available for summary")
	}

	prompt := buildPrompt(input)

	responseText, err := retryWithBackoff(ctx, s.retryCount, s.initialDelay, func() (string, error) {
		return s.generateSummaryOnce(ctx, prompt)
	})
	if err != nil {
		return nil, err
	}

	bullets := parseSummaryBullets(responseText)
	if len(bullets) == 0 {
		return nil, fmt.Errorf("gemini returned an empty summary")
	}

	return bullets, nil
}

func (s *Summarizer) generateSummaryOnce(ctx context.Context, prompt string) (string, error) {
	reqBody := generateContentRequest{
		Contents: []geminiContent{
			{
				Parts: []geminiPart{
					{Text: prompt},
				},
			},
		},
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal Gemini request: %w", err)
	}

	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", strings.TrimRight(s.baseURL, "/"), s.model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("build Gemini request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-goog-api-key", s.apiKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("call Gemini API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read Gemini response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr geminiAPIError
		if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error != nil && apiErr.Error.Message != "" {
			return "", fmt.Errorf("gemini error: %s", apiErr.Error.Message)
		}
		return "", fmt.Errorf("gemini error: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed generateContentResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decode Gemini response: %w", err)
	}

	text := extractResponseText(parsed)
	if text == "" {
		return "", fmt.Errorf("Gemini response did not contain summary text")
	}

	return text, nil
}

func buildPrompt(pageText string) string {
	return fmt.Sprintf(
		`Tom tat noi dung trang web sau bang tieng Viet de gui len Telegram.

Yeu cau:
- Tra ve 3 den 5 gạch đầu dòng ngan gon.
- Chi tap trung vao noi dung chinh cua trang.
- Khong them mo dau, khong them tieu de, khong them ghi chu.
- Moi dong la mot y rieng.

Noi dung trang:
%s`,
		pageText,
	)
}

func sanitizeInput(input string, maxChars int) string {
	lines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	cleaned := make([]string, 0, len(lines))
	last := ""

	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" || line == last {
			continue
		}
		cleaned = append(cleaned, line)
		last = line
	}

	joined := strings.TrimSpace(strings.Join(cleaned, "\n"))
	if maxChars <= 0 {
		return joined
	}

	runes := []rune(joined)
	if len(runes) <= maxChars {
		return joined
	}

	return strings.TrimSpace(string(runes[:maxChars]))
}

func extractResponseText(resp generateContentResponse) string {
	if len(resp.Candidates) == 0 {
		return ""
	}

	var parts []string
	for _, part := range resp.Candidates[0].Content.Parts {
		if strings.TrimSpace(part.Text) != "" {
			parts = append(parts, part.Text)
		}
	}

	return strings.TrimSpace(strings.Join(parts, "\n"))
}

var numberedBulletPattern = regexp.MustCompile(`^\d+[\.\)]\s*`)

func parseSummaryBullets(input string) []string {
	lines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	bullets := make([]string, 0, 5)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		line = strings.TrimPrefix(line, "-")
		line = strings.TrimPrefix(line, "*")
		line = strings.TrimPrefix(line, "•")
		line = strings.TrimSpace(numberedBulletPattern.ReplaceAllString(line, ""))
		if line == "" {
			continue
		}

		bullets = append(bullets, line)
		if len(bullets) == 5 {
			return bullets
		}
	}

	if len(bullets) == 0 {
		single := strings.TrimSpace(input)
		if single != "" {
			bullets = append(bullets, single)
		}
	}

	return bullets
}

func retryWithBackoff[T any](ctx context.Context, maxRetries int, initialDelay time.Duration, fn func() (T, error)) (T, error) {
	var zero T
	delay := initialDelay
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		result, err := fn()
		if err == nil {
			return result, nil
		}

		lastErr = err
		if attempt < maxRetries {
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			case <-time.After(delay):
			}
			delay = min(delay*2, 10*time.Second)
		}
	}

	return zero, fmt.Errorf("all retries failed: %w", lastErr)
}
