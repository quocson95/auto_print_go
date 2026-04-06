#!/usr/bin/env python3
"""Generate all Go source files for auto_print_go project."""
import os

BASE = "/Users/sondq/Documents/oneblock/auto_print_go"

files = {}

# ============================================================
# internal/config/config.go
# ============================================================
files["internal/config/config.go"] = r'''package config

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
'''

# ============================================================
# internal/email/monitor.go
# ============================================================
files["internal/email/monitor.go"] = r'''package email

import (
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"golang.org/x/net/html"

	"github.com/sondq/auto_print/internal/config"
)

type Monitor struct {
	cfg *config.Config
	mu  sync.Mutex

	imapClient        *client.Client
	lastNoopTime      time.Time
	noopInterval      time.Duration
	connectionCreated time.Time
	maxConnectionAge  time.Duration
	processedUIDs     map[uint32]struct{}
}

type EmailResult struct {
	UID     uint32
	Subject string
	Link    string
}

func NewMonitor(cfg *config.Config) *Monitor {
	return &Monitor{
		cfg:              cfg,
		noopInterval:     60 * time.Second,
		maxConnectionAge: time.Hour,
		processedUIDs:    make(map[uint32]struct{}),
	}
}

func (m *Monitor) ensureConnection() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ensureConnectionUnlocked()
}

func (m *Monitor) ensureConnectionUnlocked() error {
	now := time.Now()

	if m.imapClient != nil {
		if now.Sub(m.connectionCreated) > m.maxConnectionAge {
			slog.Info("Connection age exceeds max, reconnecting...")
			m.reconnectUnlocked()
			return nil
		}
	}

	if m.imapClient == nil {
		return m.connectUnlocked()
	}

	if now.Sub(m.lastNoopTime) > m.noopInterval {
		if err := m.imapClient.Noop(); err != nil {
			slog.Warn("NOOP failed, reconnecting...", "error", err)
			m.reconnectUnlocked()
		}
		m.lastNoopTime = now
	}

	return nil
}

func (m *Monitor) connectUnlocked() error {
	addr := fmt.Sprintf("%s:%d", m.cfg.IMAPServer, m.cfg.IMAPPort)
	slog.Info("Connecting to IMAP server", "addr", addr)

	c, err := client.DialTLS(addr, nil)
	if err != nil {
		return fmt.Errorf("dial TLS: %w", err)
	}

	c.Timeout = 5 * time.Minute

	if err := c.Login(m.cfg.EmailAddress, m.cfg.EmailPassword); err != nil {
		c.Logout()
		return fmt.Errorf("login: %w", err)
	}

	if _, err := c.Select("INBOX", false); err != nil {
		c.Logout()
		return fmt.Errorf("select INBOX: %w", err)
	}

	m.imapClient = c
	m.connectionCreated = time.Now()
	m.lastNoopTime = m.connectionCreated
	slog.Info("IMAP connection established")
	return nil
}

func (m *Monitor) reconnectUnlocked() {
	m.disconnectUnlocked()
	if err := m.connectUnlocked(); err != nil {
		slog.Error("Reconnect failed", "error", err)
	}
}

func (m *Monitor) Disconnect() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disconnectUnlocked()
}

func (m *Monitor) disconnectUnlocked() {
	if m.imapClient != nil {
		if err := m.imapClient.Logout(); err != nil {
			slog.Debug("Logout error", "error", err)
		}
		m.imapClient = nil
		slog.Info("IMAP connection closed")
	}
}

func (m *Monitor) MarkAsRead(uid uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureConnectionUnlocked(); err != nil {
		slog.Error("Failed to ensure connection for mark read", "error", err)
		return
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uid)
	flags := []interface{}{imap.SeenFlag}

	if err := m.imapClient.UidStore(seqSet, imap.FormatFlagsOp(imap.AddFlags, false), flags, nil); err != nil {
		slog.Error("Failed to mark email as read", "uid", uid, "error", err)
		m.reconnectUnlocked()
		return
	}
	slog.Info("Marked email as read", "uid", uid)
}

func (m *Monitor) MarkAsNotRead(uid uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureConnectionUnlocked(); err != nil {
		slog.Error("Failed to ensure connection for mark unread", "error", err)
		return
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uid)
	flags := []interface{}{imap.SeenFlag}

	if err := m.imapClient.UidStore(seqSet, imap.FormatFlagsOp(imap.RemoveFlags, false), flags, nil); err != nil {
		slog.Error("Failed to mark email as not read", "uid", uid, "error", err)
		m.reconnectUnlocked()
		return
	}
	slog.Info("Marked email as not read", "uid", uid)
}

func (m *Monitor) extractLink(body string) string {
	keyword := strings.ToLower(m.cfg.ViewBrowserKeyword)

	tokenizer := html.NewTokenizer(strings.NewReader(body))
	var inLink bool
	var href string

	for {
		tt := tokenizer.Next()
		switch tt {
		case html.ErrorToken:
			return m.extractLinkRegex(body)

		case html.StartTagToken:
			t := tokenizer.Token()
			if t.Data == "a" {
				inLink = true
				for _, attr := range t.Attr {
					if attr.Key == "href" {
						href = attr.Val
					}
				}
			}

		case html.TextToken:
			if inLink {
				text := strings.TrimSpace(tokenizer.Token().Data)
				if strings.Contains(strings.ToLower(text), keyword) && href != "" {
					slog.Info("Found link", "keyword", m.cfg.ViewBrowserKeyword, "href", href)
					return href
				}
			}

		case html.EndTagToken:
			t := tokenizer.Token()
			if t.Data == "a" {
				inLink = false
				href = ""
			}
		}
	}
}

func (m *Monitor) extractLinkRegex(body string) string {
	pattern := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(m.cfg.ViewBrowserKeyword) + `.*?(https?://[^\s<>"]+)`)
	match := pattern.FindStringSubmatch(body)
	if len(match) > 1 {
		slog.Info("Found URL via regex", "url", match[1])
		return match[1]
	}
	slog.Warn("No link found in email", "keyword", m.cfg.ViewBrowserKeyword)
	return ""
}

func getEmailBody(msg *mail.Message) string {
	contentType := msg.Header.Get("Content-Type")

	if !strings.Contains(contentType, "multipart") {
		b, err := io.ReadAll(msg.Body)
		if err != nil {
			slog.Error("Failed to read email body", "error", err)
			return ""
		}
		return string(b)
	}

	boundary := extractBoundary(contentType)
	if boundary == "" {
		b, err := io.ReadAll(msg.Body)
		if err != nil {
			return ""
		}
		return string(b)
	}

	return parseMultipart(msg.Body, boundary)
}

func extractBoundary(contentType string) string {
	for _, param := range strings.Split(contentType, ";") {
		param = strings.TrimSpace(param)
		lower := strings.ToLower(param)
		if strings.HasPrefix(lower, "boundary=") {
			b := param[len("boundary="):]
			b = strings.Trim(b, `"`)
			return b
		}
	}
	return ""
}

func parseMultipart(r io.Reader, boundary string) string {
	mr := multipart.NewReader(r, boundary)

	var htmlPart, textPart string
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}

		ct := p.Header.Get("Content-Type")
		lower := strings.ToLower(ct)

		data, err := io.ReadAll(p)
		if err != nil {
			continue
		}

		if strings.Contains(lower, "multipart/") {
			nested := extractBoundary(ct)
			if nested != "" {
				result := parseMultipart(strings.NewReader(string(data)), nested)
				if result != "" {
					return result
				}
			}
		} else if strings.Contains(lower, "text/html") {
			htmlPart = string(data)
		} else if strings.Contains(lower, "text/plain") {
			textPart = string(data)
		}
	}

	if htmlPart != "" {
		return htmlPart
	}
	return textPart
}

func (m *Monitor) CheckNewEmails() ([]EmailResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureConnectionUnlocked(); err != nil {
		return nil, fmt.Errorf("ensure connection: %w", err)
	}

	var results []EmailResult

	for _, sender := range m.cfg.SenderEmails {
		criteria := imap.NewSearchCriteria()
		criteria.Header.Set("From", sender)
		criteria.WithoutFlags = []string{imap.SeenFlag}

		uids, err := m.imapClient.UidSearch(criteria)
		if err != nil {
			slog.Error("Search failed", "sender", sender, "error", err)
			m.reconnectUnlocked()
			return results, fmt.Errorf("search: %w", err)
		}

		if len(uids) == 0 {
			continue
		}

		slog.Info("Found new emails", "sender", sender, "count", len(uids))

		if len(uids) > m.cfg.MaxEmailsToCheck {
			uids = uids[len(uids)-m.cfg.MaxEmailsToCheck:]
		}

		seqSet := new(imap.SeqSet)
		seqSet.AddNum(uids...)

		section := &imap.BodySectionName{Peek: true}
		items := []imap.FetchItem{section.FetchItem(), imap.FetchEnvelope, imap.FetchUid}

		messages := make(chan *imap.Message, len(uids))
		slog.Info("Fetching email bodies...", "count", len(uids))

		// Start reader goroutine to process messages concurrently with UidFetch
		// go-imap closes the 'messages' channel automatically when UidFetch returns
		done := make(chan struct{})
		go func() {
			defer close(done)
			for msg := range messages {
				uid := msg.Uid

				if _, seen := m.processedUIDs[uid]; seen {
					continue
				}

				subject := ""
				if msg.Envelope != nil {
					subject = msg.Envelope.Subject
				}

				subject = strings.TrimPrefix(subject, "Fwd: ")
				subject = strings.TrimPrefix(subject, "Fwd ")

				slog.Info("Processing email", "uid", uid, "subject", subject)

				var body string
				for _, value := range msg.Body {
					b, err := io.ReadAll(value)
					if err != nil {
						slog.Error("Failed to read message body", "uid", uid, "error", err)
						continue
					}
					body = string(b)
				}

				parsed, err := mail.ReadMessage(strings.NewReader(body))
				if err == nil {
					body = getEmailBody(parsed)
				}

				link := m.extractLink(body)
				if link != "" {
					results = append(results, EmailResult{
						UID:     uid,
						Subject: subject,
						Link:    link,
					})
					m.processedUIDs[uid] = struct{}{}
					slog.Info("Email queued for processing", "uid", uid)
				} else {
					slog.Warn("No valid link found in email", "uid", uid)
					m.processedUIDs[uid] = struct{}{}
				}
			}
		}()

		if err := m.imapClient.UidFetch(seqSet, items, messages); err != nil {
			slog.Error("Fetch failed", "error", err)
			m.reconnectUnlocked()
		}

		// Wait for reader goroutine to complete (it finishes when UidFetch closes messages)
		<-done
		slog.Info("Fetch and processing complete for sender", "sender", sender)
	}

	if len(results) == 0 {
		slog.Debug("No new emails found")
	}

	return results, nil
}
'''

# ============================================================
# internal/pdf/generator.go
# ============================================================
files["internal/pdf/generator.go"] = r'''package pdf

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"

	"github.com/sondq/auto_print/internal/config"
)

var shutdownRequested atomic.Bool

func RequestShutdown()          { shutdownRequested.Store(true) }
func ResetShutdown()            { shutdownRequested.Store(false) }
func IsShutdownRequested() bool { return shutdownRequested.Load() }

type Generator struct {
	outputDir     string
	retentionDays int
}

func NewGenerator(cfg *config.Config) *Generator {
	return &Generator{
		outputDir:     cfg.PDFOutputDir,
		retentionDays: cfg.PDFRetentionDays,
	}
}

func (g *Generator) generateFilename(subject string) string {
	subject = strings.TrimPrefix(subject, "Fwd ")
	subject = strings.TrimPrefix(subject, "Fwd: ")

	re := regexp.MustCompile(`[^a-zA-Z0-9 \-_]`)
	clean := re.ReplaceAllString(subject, "")
	clean = strings.TrimSpace(clean)
	if len(clean) > 50 {
		clean = clean[:50]
	}
	return clean + ".pdf"
}

func (g *Generator) CleanupOldPDFs() {
	if g.retentionDays <= 0 {
		return
	}

	cutoff := time.Now().AddDate(0, 0, -g.retentionDays)

	entries, err := os.ReadDir(g.outputDir)
	if err != nil {
		slog.Error("Error cleaning up old PDFs", "error", err)
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pdf") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			path := filepath.Join(g.outputDir, entry.Name())
			slog.Info("Deleting old PDF", "file", entry.Name())
			if err := os.Remove(path); err != nil {
				slog.Error("Failed to delete PDF", "file", path, "error", err)
			}
		}
	}
}

func (g *Generator) GenerateBothPDFs(ctx context.Context, url, subject string) (desktopPath, mobilePath string, err error) {
	return retryWithBackoff(ctx, 10, 2*time.Second, func() (string, string, error) {
		return g.generateBothPDFsOnce(ctx, url, subject)
	})
}

func retryWithBackoff(ctx context.Context, maxRetries int, initialDelay time.Duration, fn func() (string, string, error)) (string, string, error) {
	delay := initialDelay
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if IsShutdownRequested() {
			return "", "", fmt.Errorf("shutdown requested")
		}

		d, m, err := fn()
		if err == nil {
			return d, m, nil
		}

		lastErr = err
		if attempt < maxRetries {
			slog.Warn("PDF generation failed, retrying",
				"attempt", attempt+1,
				"maxRetries", maxRetries+1,
				"error", err,
				"retryIn", delay,
			)
			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			case <-time.After(delay):
			}
			delay = min(delay*2, 60*time.Second)
		}
	}

	return "", "", fmt.Errorf("all retries failed: %w", lastErr)
}

func (g *Generator) generateBothPDFsOnce(ctx context.Context, url, subject string) (string, string, error) {
	baseName := strings.TrimSuffix(g.generateFilename(subject), ".pdf")
	desktopFile := fmt.Sprintf("[PC] %s.pdf", baseName)
	mobileFile := fmt.Sprintf("[Mobile] %s.pdf", baseName)
	desktopPath := filepath.Join(g.outputDir, desktopFile)
	mobilePath := filepath.Join(g.outputDir, mobileFile)

	slog.Info("Generating both PDFs", "url", url)

	path, _ := launcher.LookPath()
	u := launcher.New().Bin(path).Headless(true).MustLaunch()
	browser := rod.New().ControlURL(u).MustConnect()
	defer browser.MustClose()

	page := browser.MustPage("")

	if err := page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width: 1920, Height: 1080,
	}); err != nil {
		return "", "", fmt.Errorf("set viewport: %w", err)
	}

	slog.Info("Loading page", "url", url)
	if err := page.Navigate(url); err != nil {
		return "", "", fmt.Errorf("navigate: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return "", "", fmt.Errorf("wait load: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := page.Context(waitCtx).WaitRequestIdle(2*time.Second, nil, nil, nil)(); err != nil {
		slog.Warn("Network idle timeout, continuing anyway", "error", err)
	}

	g.closePopups(page)

	slog.Info("Scrolling page to load all images...")
	g.scrollToBottom(page)

	slog.Info("Waiting for images to load...")
	time.Sleep(3 * time.Second)

	slog.Info("Compressing images to reduce PDF size...")
	g.compressImages(page)
	time.Sleep(time.Second)

	// Desktop PDF (A4 landscape)
	slog.Info("Saving desktop PDF", "path", desktopPath)
	if err := g.savePDF(page, desktopPath, 29.7, 21.0, 1.0); err != nil {
		return "", "", fmt.Errorf("desktop PDF: %w", err)
	}
	slog.Info("Desktop PDF generated", "path", desktopPath)

	// Switch to mobile viewport
	slog.Info("Resizing viewport to mobile...")
	if err := page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width: 375, Height: 812,
	}); err != nil {
		return "", "", fmt.Errorf("set mobile viewport: %w", err)
	}
	time.Sleep(time.Second)

	slog.Info("Injecting mobile-friendly CSS...")
	g.injectMobileCSS(page)
	time.Sleep(500 * time.Millisecond)

	// Mobile PDF (A5 portrait)
	slog.Info("Saving mobile PDF", "path", mobilePath)
	if err := g.savePDF(page, mobilePath, 14.8, 21.0, 0.5); err != nil {
		return "", "", fmt.Errorf("mobile PDF: %w", err)
	}
	slog.Info("Mobile PDF generated", "path", mobilePath)

	g.CleanupOldPDFs()

	return desktopPath, mobilePath, nil
}

func (g *Generator) savePDF(page *rod.Page, path string, widthCM, heightCM, marginCM float64) error {
	marginInch := marginCM / 2.54

	reader, err := page.PDF(&proto.PagePrintToPDF{
		PrintBackground: true,
		PaperWidth:      toPtr(widthCM / 2.54),
		PaperHeight:     toPtr(heightCM / 2.54),
		MarginTop:       toPtr(marginInch),
		MarginBottom:    toPtr(marginInch),
		MarginLeft:      toPtr(marginInch),
		MarginRight:     toPtr(marginInch),
	})
	if err != nil {
		return fmt.Errorf("generate PDF: %w", err)
	}

	data, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("read PDF: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write PDF: %w", err)
	}

	return nil
}

func toPtr[T any](v T) *T { return &v }

func (g *Generator) closePopups(page *rod.Page) {
	slog.Info("Closing popups and overlays...")

	selectors := []string{
		`button[aria-label*="Close"]`,
		`button[aria-label*="close"]`,
		`[class*="close"]`,
		`[class*="dismiss"]`,
	}

	closed := 0
	for _, sel := range selectors {
		el, err := page.Timeout(500 * time.Millisecond).Element(sel)
		if err == nil && el != nil {
			if err := el.Click(proto.InputMouseButtonLeft, 1); err == nil {
				closed++
				time.Sleep(200 * time.Millisecond)
			}
		}
	}

	page.MustEval(`() => {
		document.querySelectorAll('[class*="overlay"], [class*="backdrop"], [class*="modal-backdrop"]')
			.forEach(el => { if (el.offsetParent !== null) el.style.display = 'none'; });
		document.querySelectorAll('*').forEach(el => {
			if (parseInt(window.getComputedStyle(el).zIndex) > 9999) el.style.display = 'none';
		});
		document.body.style.overflow = 'auto';
		document.documentElement.style.overflow = 'auto';
	}`)

	if closed > 0 {
		slog.Info("Closed popups", "count", closed)
	}
}

func (g *Generator) scrollToBottom(page *rod.Page) {
	viewportHeight := page.MustEval(`window.innerHeight`).Int()
	scrollHeight := page.MustEval(`document.body.scrollHeight`).Int()

	current := 0
	for current < scrollHeight {
		if IsShutdownRequested() {
			slog.Info("Shutdown requested, stopping scroll")
			return
		}
		current += viewportHeight
		page.MustEval(fmt.Sprintf(`window.scrollTo(0, %d)`, current))
		time.Sleep(300 * time.Millisecond)

		newHeight := page.MustEval(`document.body.scrollHeight`).Int()
		if newHeight > scrollHeight {
			scrollHeight = newHeight
		}
	}

	page.MustEval(`window.scrollTo(0, document.body.scrollHeight)`)
	time.Sleep(500 * time.Millisecond)
	page.MustEval(`window.scrollTo(0, 0)`)
	time.Sleep(300 * time.Millisecond)

	slog.Info("Incremental scrolling complete")
}

func (g *Generator) compressImages(page *rod.Page) {
	page.MustEval(`() => {
		const images = document.querySelectorAll('img');
		images.forEach(img => {
			if (img.naturalWidth < 100 || img.naturalHeight < 100) return;
			try {
				const canvas = document.createElement('canvas');
				const ctx = canvas.getContext('2d');
				const maxSize = 800;
				let width = img.naturalWidth;
				let height = img.naturalHeight;
				if (width > maxSize || height > maxSize) {
					if (width > height) {
						height = Math.round(height * maxSize / width);
						width = maxSize;
					} else {
						width = Math.round(width * maxSize / height);
						height = maxSize;
					}
				}
				canvas.width = width;
				canvas.height = height;
				ctx.drawImage(img, 0, 0, width, height);
				img.src = canvas.toDataURL('image/jpeg', 0.6);
			} catch (e) {}
		});
	}`)
}

func (g *Generator) injectMobileCSS(page *rod.Page) {
	page.MustEval(`() => {
		const style = document.createElement('style');
		style.textContent = ` + "`" + `
			img { max-width: 100% !important; height: auto !important; width: auto !important; }
			* { max-width: 100% !important; word-wrap: break-word !important; overflow-wrap: break-word !important; }
			body, html { overflow-x: hidden !important; width: 100% !important; }
			table { table-layout: fixed !important; width: 100% !important; }
			pre, code { white-space: pre-wrap !important; word-break: break-word !important; max-width: 100% !important; }
			iframe { max-width: 100% !important; }
			body { font-size: 14px !important; line-height: 1.5 !important; }
			[style*="position: fixed"], [style*="position:fixed"] { position: relative !important; }
		` + "`" + `;
		document.head.appendChild(style);
	}`)
}
'''

# ============================================================
# internal/s3/uploader.go
# ============================================================
files["internal/s3/uploader.go"] = r'''package s3

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sondq/auto_print/internal/config"
)

type Uploader struct {
	cfg     *config.Config
	client  *s3.Client
	presign *s3.PresignClient
}

func NewUploader(cfg *config.Config) (*Uploader, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.S3Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AWSAccessKeyID, cfg.AWSSecretAccessKey, ""),
		),
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	s3Opts := []func(*s3.Options){}
	if cfg.S3EndpointURL != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.S3EndpointURL)
			o.UsePathStyle = true
		})
	}

	client := s3.NewFromConfig(awsCfg, s3Opts...)
	presign := s3.NewPresignClient(client)

	return &Uploader{
		cfg:     cfg,
		client:  client,
		presign: presign,
	}, nil
}

func (u *Uploader) IsConfigured() bool {
	return u.cfg.S3BucketName != "" && u.cfg.AWSAccessKeyID != "" && u.cfg.AWSSecretAccessKey != ""
}

func (u *Uploader) UploadPDF(ctx context.Context, pdfPath string) (string, error) {
	filename := filepath.Base(pdfPath)
	s3Key := "pdfs/" + filename

	slog.Info("Uploading PDF to S3", "file", filename, "key", s3Key)

	presignResult, err := u.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:       aws.String(u.cfg.S3BucketName),
		Key:          aws.String(s3Key),
		ContentType:  aws.String("application/pdf"),
		ACL:          "public-read",
		CacheControl: aws.String("max-age=31536000"),
	}, s3.WithPresignExpires(time.Hour))
	if err != nil {
		return "", fmt.Errorf("presign: %w", err)
	}

	data, err := os.ReadFile(pdfPath)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, presignResult.URL, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/pdf")
	req.Header.Set("x-amz-acl", "public-read")
	req.Header.Set("Cache-Control", "max-age=31536000")

	httpClient := &http.Client{Timeout: 5 * time.Minute}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("upload failed: HTTP %d: %s", resp.StatusCode, string(body))
	}

	slog.Info("Upload successful", "status", resp.StatusCode)

	publicURL := u.buildPublicURL(s3Key)
	slog.Info("PDF uploaded successfully", "url", publicURL)
	return publicURL, nil
}

func (u *Uploader) UploadThumbnail(ctx context.Context, filePath, s3Key string) (string, error) {
	slog.Info("Uploading thumbnail to S3", "file", filepath.Base(filePath), "key", s3Key)

	presignResult, err := u.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:       aws.String(u.cfg.S3BucketName),
		Key:          aws.String(s3Key),
		ContentType:  aws.String("image/webp"),
		ACL:          "public-read",
		CacheControl: aws.String("max-age=31536000"),
	}, s3.WithPresignExpires(time.Hour))
	if err != nil {
		return "", fmt.Errorf("presign: %w", err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, presignResult.URL, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "image/webp")
	req.Header.Set("x-amz-acl", "public-read")
	req.Header.Set("Cache-Control", "max-age=31536000")

	httpClient := &http.Client{Timeout: 5 * time.Minute}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("upload failed: HTTP %d: %s", resp.StatusCode, string(body))
	}

	return u.buildPublicURL(s3Key), nil
}

type IndexEntry struct {
	Key          string `json:"key"`
	LastModified string `json:"lastModified"`
	Size         int64  `json:"size"`
}

func (u *Uploader) UpdateIndexJSON(ctx context.Context, newEntries []IndexEntry) error {
	indexKey := "pdfs/index.json"

	var data []IndexEntry
	result, err := u.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(u.cfg.S3BucketName),
		Key:    aws.String(indexKey),
	})
	if err != nil {
		if !strings.Contains(err.Error(), "NoSuchKey") {
			slog.Warn("Could not fetch index.json, starting fresh", "error", err)
		}
		data = []IndexEntry{}
	} else {
		defer result.Body.Close()
		content, readErr := io.ReadAll(result.Body)
		if readErr == nil {
			if len(content) >= 2 && content[0] == 0x1f && content[1] == 0x8b {
				gr, gerr := gzip.NewReader(bytes.NewReader(content))
				if gerr == nil {
					content, _ = io.ReadAll(gr)
					gr.Close()
				}
			}
			if jsonErr := json.Unmarshal(content, &data); jsonErr != nil {
				slog.Warn("Could not decode index.json, starting fresh", "error", jsonErr)
				data = []IndexEntry{}
			}
		}
	}

	dataMap := make(map[string]IndexEntry, len(data))
	for _, e := range data {
		dataMap[e.Key] = e
	}
	for _, e := range newEntries {
		dataMap[e.Key] = e
		slog.Info("Adding/Updating entry", "key", e.Key)
	}

	updated := make([]IndexEntry, 0, len(dataMap))
	for _, e := range dataMap {
		updated = append(updated, e)
	}
	sort.Slice(updated, func(i, j int) bool {
		return updated[i].LastModified > updated[j].LastModified
	})

	jsonBytes, err := json.Marshal(updated)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(jsonBytes); err != nil {
		return fmt.Errorf("gzip write: %w", err)
	}
	if err := gw.Close(); err != nil {
		return fmt.Errorf("gzip close: %w", err)
	}

	presignResult, err := u.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:          aws.String(u.cfg.S3BucketName),
		Key:             aws.String(indexKey),
		ContentType:     aws.String("application/json"),
		ContentEncoding: aws.String("gzip"),
		ACL:             "public-read",
		CacheControl:    aws.String("max-age=0, no-cache, no-store, must-revalidate"),
	}, s3.WithPresignExpires(5*time.Minute))
	if err != nil {
		return fmt.Errorf("presign: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, presignResult.URL, &buf)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("x-amz-acl", "public-read")
	req.Header.Set("Cache-Control", "max-age=0, no-cache, no-store, must-revalidate")

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed: HTTP %d: %s", resp.StatusCode, string(body))
	}

	slog.Info("Successfully updated index.json", "entries", len(newEntries))
	return nil
}

func (u *Uploader) buildPublicURL(s3Key string) string {
	encoded := url.PathEscape(s3Key)
	encoded = strings.ReplaceAll(encoded, "%2F", "/")

	if u.cfg.S3EndpointURL != "" {
		endpoint := strings.TrimRight(u.cfg.S3EndpointURL, "/")
		return fmt.Sprintf("%s/%s/%s", endpoint, u.cfg.S3BucketName, encoded)
	}
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", u.cfg.S3BucketName, u.cfg.S3Region, encoded)
}
'''

# ============================================================
# internal/telegram/sender.go
# ============================================================
files["internal/telegram/sender.go"] = r'''package telegram

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
	"time"

	"github.com/sondq/auto_print/internal/config"
)

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

func (s *Sender) SendCombinedMessage(ctx context.Context, subject, pcURL, mobileURL, thumbnailURL string) (bool, error) {
	return retryTelegram(ctx, 10, 2*time.Second, func() (bool, error) {
		return s.sendCombinedOnce(ctx, subject, pcURL, mobileURL, thumbnailURL)
	})
}

func (s *Sender) sendCombinedOnce(ctx context.Context, subject, pcURL, mobileURL, thumbnailURL string) (bool, error) {
	loc := time.FixedZone("GMT+7", 7*3600)
	timestamp := time.Now().In(loc).Format("2006-01-02 15:04:05")

	message := fmt.Sprintf(
		"\U0001f4f0 *%s*\n\U0001f550 %s\n\n\U0001f4f1 [Tải PDF cho Mobile](%s)\n\U0001f4bb [Tải PDF cho PC](%s)\n\n\U0001f4da [Tất cả tài liệu](https://library.oneblock.vn)",
		subject, timestamp, mobileURL, pcURL,
	)

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
	caption := fmt.Sprintf("*Bài viết:* %s\n\n%s *Phiên bản %s*\n*Thời gian:* %s", subject, typeIcon, typeLabel, timestamp)

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
'''

# ============================================================
# internal/thumbnail/generator.go
# ============================================================
files["internal/thumbnail/generator.go"] = r'''package thumbnail

import (
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gen2brain/go-fitz"
	"golang.org/x/image/draw"

	"github.com/sondq/auto_print/internal/config"
	s3pkg "github.com/sondq/auto_print/internal/s3"
)

type Generator struct {
	cfg      *config.Config
	uploader *s3pkg.Uploader
}

func NewGenerator(cfg *config.Config, uploader *s3pkg.Uploader) *Generator {
	return &Generator{
		cfg:      cfg,
		uploader: uploader,
	}
}

func ExtractCleanTitle(filename string) string {
	re1 := regexp.MustCompile(`(?i)\[PC\]`)
	re2 := regexp.MustCompile(`(?i)\[Mobile\]`)
	re3 := regexp.MustCompile(`(?i)\.pdf$`)
	re4 := regexp.MustCompile(`(\d+)-(\d+)`)
	re5 := regexp.MustCompile(`\s+`)

	clean := re1.ReplaceAllString(filename, "")
	clean = re2.ReplaceAllString(clean, "")
	clean = re3.ReplaceAllString(clean, "")
	clean = re4.ReplaceAllString(clean, "${1}${2}")
	clean = re5.ReplaceAllString(clean, "-")
	clean = strings.Trim(clean, "-")
	return clean
}

func (g *Generator) ProcessPDF(ctx context.Context, pdfPath, title string) (string, error) {
	if title == "" {
		title = ExtractCleanTitle(filepath.Base(pdfPath))
	}

	thumbnailFilename := title + ".jpg"
	thumbnailPath := filepath.Join(g.cfg.PDFOutputDir, thumbnailFilename)

	if err := g.generateThumbnail(pdfPath, thumbnailPath, 800); err != nil {
		return "", fmt.Errorf("generate thumbnail: %w", err)
	}

	s3Key := "pdfs/thumbnails/" + title + ".webp"
	thumbURL, err := g.uploader.UploadThumbnail(ctx, thumbnailPath, s3Key)
	if err != nil {
		return "", fmt.Errorf("upload thumbnail: %w", err)
	}

	if err := os.Remove(thumbnailPath); err != nil {
		slog.Warn("Failed to cleanup thumbnail", "path", thumbnailPath, "error", err)
	}

	return thumbURL, nil
}

func (g *Generator) generateThumbnail(pdfPath, outputPath string, maxWidth int) error {
	doc, err := fitz.New(pdfPath)
	if err != nil {
		return fmt.Errorf("open PDF: %w", err)
	}
	defer doc.Close()

	img, err := doc.Image(0)
	if err != nil {
		return fmt.Errorf("render page: %w", err)
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	var output image.Image = img
	if width > maxWidth {
		newHeight := height * maxWidth / width
		scaled := image.NewRGBA(image.Rect(0, 0, maxWidth, newHeight))
		draw.ApproxBiLinear.Scale(scaled, scaled.Bounds(), img, img.Bounds(), draw.Over, nil)
		output = scaled
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer f.Close()

	if err := jpeg.Encode(f, output, &jpeg.Options{Quality: 80}); err != nil {
		return fmt.Errorf("encode: %w", err)
	}

	slog.Info("Generated thumbnail", "output", outputPath)
	return nil
}
'''

# ============================================================
# cmd/main.go
# ============================================================
files["cmd/main.go"] = r'''package main

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

	s3Uploader, err := s3pkg.NewUploader(cfg)
	if err != nil {
		slog.Error("Failed to create S3 uploader", "error", err)
		os.Exit(1)
	}

	thumbGenerator := thumbnail.NewGenerator(cfg, s3Uploader)

	slog.Info("Monitoring emails", "senders", strings.Join(cfg.SenderEmails, ", "))
	slog.Info("Check interval", "seconds", cfg.CheckIntervalSeconds)
	slog.Info("PDF Mode: Dual-format (Desktop + Mobile)")
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
		processEmails(ctx, emailMonitor, pdfGenerator, telegramSender, s3Uploader, thumbGenerator, cfg)
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
		desktopPath, mobilePath, err := pdfGenerator.GenerateBothPDFs(ctx, e.Link, e.Subject)
		if err != nil {
			errMsg := fmt.Sprintf("Error processing email %d: %v", e.UID, err)
			slog.Error(errMsg)
			telegramSender.SendErrorNotification(ctx, errMsg)
			continue
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

		updateIndexJSON(ctx, s3Uploader, desktopPath, mobilePath, pcURL, mobileURL)

		if pcURL != "" && mobileURL != "" {
			slog.Info("Sending combined message to Telegram...")
			ok, err := telegramSender.SendCombinedMessage(ctx, e.Subject, pcURL, mobileURL, thumbnailURL)
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

func updateIndexJSON(ctx context.Context, uploader *s3pkg.Uploader, desktopPath, mobilePath, pcURL, mobileURL string) {
	var entries []s3pkg.IndexEntry
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")

	if pcURL != "" {
		info, err := os.Stat(desktopPath)
		if err == nil {
			entries = append(entries, s3pkg.IndexEntry{
				Key:          "pdfs/" + filepath.Base(desktopPath),
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
'''

# Write all files
for relpath, content in files.items():
    fullpath = os.path.join(BASE, relpath)
    os.makedirs(os.path.dirname(fullpath), exist_ok=True)
    with open(fullpath, 'w') as f:
        f.write(content.lstrip('\n'))
    print(f"  wrote {relpath}")

print("\nAll Go source files written successfully.")
