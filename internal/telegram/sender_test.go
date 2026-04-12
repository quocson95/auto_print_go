package telegram

import (
	"strings"
	"testing"
	"time"
)

func TestBuildCombinedMessageIncludesSummaryAndEscapesMarkdown(t *testing.T) {
	t.Parallel()

	msg := buildCombinedMessage(
		"Deal_[Hot]*",
		"https://pc.example.com",
		"https://mobile.example.com",
		[]string{"Bullet with _markdown_ markers", "Second *bullet*"},
		time.Date(2026, 4, 12, 10, 30, 0, 0, time.UTC),
	)

	if !strings.Contains(msg, "*Tóm tắt*") {
		t.Fatalf("expected summary heading in message: %q", msg)
	}
	if !strings.Contains(msg, "Deal\\_\\[Hot]\\*") {
		t.Fatalf("expected escaped subject, got %q", msg)
	}
	if !strings.Contains(msg, "Bullet with \\_markdown\\_ markers") {
		t.Fatalf("expected escaped summary bullet, got %q", msg)
	}
}

func TestBuildCombinedMessageDropsOverflowingSummaryFirst(t *testing.T) {
	t.Parallel()

	longBullet := strings.Repeat("x", telegramMaxMessageLen)
	msg := buildCombinedMessage(
		"Subject",
		"https://pc.example.com",
		"https://mobile.example.com",
		[]string{longBullet, "second bullet"},
		time.Date(2026, 4, 12, 10, 30, 0, 0, time.UTC),
	)

	if len([]rune(msg)) > telegramMaxMessageLen {
		t.Fatalf("message length = %d, want <= %d", len([]rune(msg)), telegramMaxMessageLen)
	}
	if strings.Contains(msg, "second bullet") {
		t.Fatalf("expected overflowing bullets to be removed, got %q", msg)
	}
	if !strings.Contains(msg, "[Tải PDF cho Mobile](https://mobile.example.com)") {
		t.Fatalf("expected links to remain, got %q", msg)
	}
}
