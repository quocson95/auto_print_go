package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSummarizeUsesGeminiResponseAndNormalizesBullets(t *testing.T) {
	t.Parallel()

	var captured generateContentRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1beta/models/test-model:generateContent" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("X-goog-api-key"); got != "secret" {
			t.Fatalf("unexpected API key header: %q", got)
		}

		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}

		_, _ = w.Write([]byte(`{
			"candidates": [{
				"content": {
					"parts": [{
						"text": "- Y chinh thu nhat\n2. Y chinh thu hai\n• Y chinh thu ba"
					}]
				}
			}]
		}`))
	}))
	defer server.Close()

	summarizer := &Summarizer{
		enabled:       true,
		apiKey:        "secret",
		model:         "test-model",
		maxInputChars: 80,
		httpClient:    server.Client(),
		baseURL:       server.URL,
		retryCount:    0,
	}

	bullets, err := summarizer.Summarize(context.Background(), "Dong 1\n\nDong 2")
	if err != nil {
		t.Fatalf("Summarize() error = %v", err)
	}

	if len(bullets) != 3 {
		t.Fatalf("expected 3 bullets, got %d", len(bullets))
	}
	if bullets[0] != "Y chinh thu nhat" || bullets[1] != "Y chinh thu hai" || bullets[2] != "Y chinh thu ba" {
		t.Fatalf("unexpected bullets: %#v", bullets)
	}

	gotPrompt := captured.Contents[0].Parts[0].Text
	if !strings.Contains(gotPrompt, "Dong 1\nDong 2") {
		t.Fatalf("expected sanitized page text in prompt, got %q", gotPrompt)
	}
}

func TestSummarizeRetriesAfterServerError(t *testing.T) {
	t.Parallel()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"message":"temporary"}}`))
			return
		}

		_, _ = w.Write([]byte(`{
			"candidates": [{
				"content": {
					"parts": [{
						"text": "- Ban tom tat thanh cong"
					}]
				}
			}]
		}`))
	}))
	defer server.Close()

	summarizer := &Summarizer{
		enabled:       true,
		apiKey:        "secret",
		model:         "test-model",
		maxInputChars: 80,
		httpClient:    server.Client(),
		baseURL:       server.URL,
		retryCount:    1,
	}

	bullets, err := summarizer.Summarize(context.Background(), "Noi dung can tom tat")
	if err != nil {
		t.Fatalf("Summarize() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	if len(bullets) != 1 || bullets[0] != "Ban tom tat thanh cong" {
		t.Fatalf("unexpected bullets: %#v", bullets)
	}
}

func TestSanitizeInputTrimsWhitespaceAndCapsLength(t *testing.T) {
	t.Parallel()

	got := sanitizeInput("  Dong 1   \n\nDong     2\nDong     2\nDong 3  ", 12)
	if got != "Dong 1\nDong" {
		t.Fatalf("sanitizeInput() = %q", got)
	}
}
