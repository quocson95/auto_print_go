package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	aipkg "github.com/sondq/auto_print/internal/ai"
	"github.com/sondq/auto_print/internal/config"
	emailpkg "github.com/sondq/auto_print/internal/email"
	"github.com/sondq/auto_print/internal/pdf"
	s3pkg "github.com/sondq/auto_print/internal/s3"
	"github.com/sondq/auto_print/internal/telegram"
	"github.com/sondq/auto_print/internal/thumbnail"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	setupLogging(cfg)

	slog.Info(strings.Repeat("=", 60))
	slog.Info("Email to PDF Automation Starting")
	slog.Info(strings.Repeat("=", 60))

	if err := cfg.Validate(); err != nil {
		slog.Error("Configuration error", "error", err)
		os.Exit(1)
	}
	slog.Info("Configuration validated successfully")

	emailMonitor := emailpkg.NewMonitor(cfg)
	pdfGenerator := pdf.NewGenerator(cfg)
	telegramSender := telegram.NewSender(cfg)
	summarizer := aipkg.NewSummarizer(cfg)

	s3Uploader, err := s3pkg.NewUploader(cfg)
	if err != nil {
		slog.Error("Failed to create S3 uploader", "error", err)
		os.Exit(1)
	}

	thumbGenerator := thumbnail.NewGenerator(cfg, s3Uploader)

	slog.Info("Monitoring emails", "senders", strings.Join(cfg.SenderEmails, ", "))
	slog.Info("Check interval", "seconds", cfg.CheckIntervalSeconds)
	slog.Info("PDF Mode: Dual-format (Desktop + Mobile)")
	if summarizer.Enabled() {
		slog.Info("Gemini summarization enabled", "model", cfg.GeminiModel)
	} else {
		slog.Info("Gemini summarization disabled")
	}
	slog.Info("Starting main loop...")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		slog.Info("Shutdown signal received", "signal", sig)
		cancel()
		pdf.RequestShutdown()
	}()

	cycleCount := 0
	for {
		select {
		case <-ctx.Done():
			goto shutdown
		default:
		}

		cycleCount++
		slog.Debug("Check cycle", "cycle", cycleCount)

		start := time.Now()
		processEmails(ctx, emailMonitor, pdfGenerator, telegramSender, summarizer, s3Uploader, thumbGenerator, cfg)
		elapsed := time.Since(start)

		remaining := time.Duration(cfg.CheckIntervalSeconds)*time.Second - elapsed
		if remaining > 0 {
			select {
			case <-ctx.Done():
				goto shutdown
			case <-time.After(remaining):
			}
		} else {
			slog.Debug("Processing took longer than interval", "elapsed", elapsed)
		}
	}

shutdown:
	emailMonitor.Disconnect()
	slog.Info("Application shutting down gracefully")
	slog.Info(strings.Repeat("=", 60))
}

func processEmails(
	ctx context.Context,
	emailMonitor *emailpkg.Monitor,
	pdfGenerator *pdf.Generator,
	telegramSender *telegram.Sender,
	summarizer *aipkg.Summarizer,
	s3Uploader *s3pkg.Uploader,
	thumbGenerator *thumbnail.Generator,
	cfg *config.Config,
) {
	emails, err := emailMonitor.CheckNewEmails()
	if err != nil {
		slog.Error("Error checking emails", "error", err)
		return
	}

	if len(emails) == 0 {
		slog.Debug("No new emails to process")
		return
	}

	var pendingUIDs []uint32

	for _, e := range emails {
		if ctxDone(ctx) {
			slog.Info("Stop signal received, cancelling email processing")
			markPendingAsUnread(emailMonitor, pendingUIDs)
			return
		}

		pendingUIDs = append(pendingUIDs, e.UID)
		slog.Info("Processing email", "uid", e.UID, "subject", e.Subject)

		slog.Info("Generating both PDFs (desktop + mobile)...")
		artifacts, err := pdfGenerator.GenerateArtifacts(ctx, e.Link, e.Subject)
		if err != nil {
			errMsg := fmt.Sprintf("Error processing email %d: %v", e.UID, err)
			slog.Error(errMsg)
			telegramSender.SendErrorNotification(ctx, errMsg)
			continue
		}
		desktopPath := artifacts.DesktopPath
		mobilePath := artifacts.MobilePath

		var summary []string
		if summarizer.Enabled() && strings.TrimSpace(artifacts.PageText) != "" {
			slog.Info("Generating Gemini summary...", "uid", e.UID)
			summary, err = summarizer.Summarize(ctx, artifacts.PageText)
			if err != nil {
				slog.Warn("Failed to generate Gemini summary", "uid", e.UID, "error", err)
			} else if len(summary) > 0 {
				slog.Info("Gemini summary generated", "uid", e.UID, "bullets", len(summary))
			}
		}

		if ctxDone(ctx) {
			markPendingAsUnread(emailMonitor, pendingUIDs)
			return
		}

		slog.Info("Uploading PDFs to S3...")
		pcURL, err := s3Uploader.UploadPDF(ctx, desktopPath)
		if err != nil {
			slog.Warn("Failed to upload desktop PDF to S3", "error", err)
		}

		if ctxDone(ctx) {
			markPendingAsUnread(emailMonitor, pendingUIDs)
			return
		}

		mobileURL, err := s3Uploader.UploadPDF(ctx, mobilePath)
		if err != nil {
			slog.Warn("Failed to upload mobile PDF to S3", "error", err)
		}

		if ctxDone(ctx) {
			markPendingAsUnread(emailMonitor, pendingUIDs)
			return
		}

		slog.Info("Generating and uploading thumbnail...")
		thumbnailURL, err := thumbGenerator.ProcessPDF(ctx, desktopPath, "")
		if err != nil {
			slog.Warn("Failed to generate thumbnail", "error", err)
		} else {
			slog.Info("Thumbnail created", "url", thumbnailURL)
		}

		if ctxDone(ctx) {
			markPendingAsUnread(emailMonitor, pendingUIDs)
			return
		}

		updateIndexJSON(ctx, s3Uploader, desktopPath, mobilePath, pcURL, mobileURL, e.Subject)

		if pcURL != "" && mobileURL != "" {
			slog.Info("Sending combined message to Telegram...")
			ok, err := telegramSender.SendCombinedMessage(ctx, e.Subject, pcURL, mobileURL, thumbnailURL, summary)
			if ok && err == nil {
				slog.Info("Successfully processed email - both versions sent", "uid", e.UID)
				emailMonitor.MarkAsRead(e.UID)
				pendingUIDs = removePending(pendingUIDs, e.UID)
			} else {
				slog.Error("Failed to send Telegram message", "uid", e.UID, "error", err)
				telegramSender.SendErrorNotification(ctx, fmt.Sprintf("Failed to send message for: %s", e.Subject))
			}
		} else {
			slog.Warn("S3 upload failed, sending files directly...")
			okDesktop, _ := telegramSender.SendPDF(ctx, desktopPath, e.Subject, "pc")
			okMobile, _ := telegramSender.SendPDF(ctx, mobilePath, e.Subject, "mobile")

			if okDesktop && okMobile {
				slog.Info("Successfully processed email - both versions sent directly", "uid", e.UID)
				emailMonitor.MarkAsRead(e.UID)
				pendingUIDs = removePending(pendingUIDs, e.UID)
			} else {
				var failed []string
				if !okDesktop {
					failed = append(failed, "Desktop")
				}
				if !okMobile {
					failed = append(failed, "Mobile")
				}
				slog.Error("Failed to send PDF", "uid", e.UID, "failed", strings.Join(failed, ", "))
				telegramSender.SendErrorNotification(ctx, fmt.Sprintf("Failed to send %s PDF for: %s", strings.Join(failed, ", "), e.Subject))
			}
		}
	}

	markPendingAsUnread(emailMonitor, pendingUIDs)
}

func ctxDone(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

func updateIndexJSON(ctx context.Context, uploader *s3pkg.Uploader, desktopPath, mobilePath, pcURL, mobileURL, title string) {
	var entries []s3pkg.IndexEntry
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
	title = normalizeIndexTitle(title)

	if pcURL != "" {
		info, err := os.Stat(desktopPath)
		if err == nil {
			entries = append(entries, s3pkg.IndexEntry{
				Key:          "pdfs/" + filepath.Base(desktopPath),
				Name:         title,
				Title:        title,
				LastModified: timestamp,
				Size:         info.Size(),
			})
		}
	}

	if mobileURL != "" {
		info, err := os.Stat(mobilePath)
		if err == nil {
			entries = append(entries, s3pkg.IndexEntry{
				Key:          "pdfs/" + filepath.Base(mobilePath),
				Name:         title,
				Title:        title,
				LastModified: timestamp,
				Size:         info.Size(),
			})
		}
	}

	if len(entries) > 0 {
		if err := uploader.UpdateIndexJSON(ctx, entries); err != nil {
			slog.Error("Failed to update index.json", "error", err)
		}
	}
}

func normalizeIndexTitle(title string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(title), " "))
}

func markPendingAsUnread(monitor *emailpkg.Monitor, uids []uint32) {
	if len(uids) == 0 {
		return
	}
	slog.Warn("Marking failed/interrupted emails as not read", "count", len(uids), "uids", uids)
	for _, uid := range uids {
		monitor.MarkAsNotRead(uid)
	}
}

func removePending(uids []uint32, uid uint32) []uint32 {
	for i, u := range uids {
		if u == uid {
			return append(uids[:i], uids[i+1:]...)
		}
	}
	return uids
}

func setupLogging(cfg *config.Config) {
	logDir := filepath.Dir(cfg.LogFile)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create log dir: %v\n", err)
	}

	logFile, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open log file: %v\n", err)
		logFile = nil
	}

	var w io.Writer
	if logFile != nil {
		w = io.MultiWriter(os.Stdout, logFile)
	} else {
		w = os.Stdout
	}

	handler := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: cfg.SlogLevel(),
	})
	slog.SetDefault(slog.New(handler))
}
