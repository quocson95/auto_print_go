package pdf

import (
	"context"
	"fmt"
	"io"
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

type Artifacts struct {
	DesktopPath string
	MobilePath  string
	PageText    string
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
	subject = strings.Join(strings.Fields(subject), " ")

	re := regexp.MustCompile(`[^\p{L}\p{N} \-_]`)
	clean := re.ReplaceAllString(subject, "")
	clean = strings.TrimSpace(clean)
	runes := []rune(clean)
	if len(runes) > 50 {
		clean = string(runes[:50])
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
	artifacts, err := g.GenerateArtifacts(ctx, url, subject)
	if err != nil {
		return "", "", err
	}
	return artifacts.DesktopPath, artifacts.MobilePath, nil
}

func (g *Generator) GenerateArtifacts(ctx context.Context, url, subject string) (Artifacts, error) {
	return retryWithBackoff(ctx, 10, 2*time.Second, func() (Artifacts, error) {
		return g.generateArtifactsOnce(ctx, url, subject)
	})
}

func retryWithBackoff[T any](ctx context.Context, maxRetries int, initialDelay time.Duration, fn func() (T, error)) (T, error) {
	var zero T
	delay := initialDelay
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if IsShutdownRequested() {
			return zero, fmt.Errorf("shutdown requested")
		}

		result, err := fn()
		if err == nil {
			return result, nil
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
				return zero, ctx.Err()
			case <-time.After(delay):
			}
			delay = min(delay*2, 60*time.Second)
		}
	}

	return zero, fmt.Errorf("all retries failed: %w", lastErr)
}

func (g *Generator) generateArtifactsOnce(ctx context.Context, url, subject string) (Artifacts, error) {
	baseName := strings.TrimSuffix(g.generateFilename(subject), ".pdf")
	desktopFile := fmt.Sprintf("[PC] %s.pdf", baseName)
	mobileFile := fmt.Sprintf("[Mobile] %s.pdf", baseName)
	desktopPath := filepath.Join(g.outputDir, desktopFile)
	mobilePath := filepath.Join(g.outputDir, mobileFile)

	slog.Info("Generating both PDFs", "url", url)

	slog.Info("Launching Chromium browser process...")
	l := launcher.New().
		Headless(true).
		Set("no-sandbox").
		Set("disable-dev-shm-usage").
		Set("disable-gpu").
		Set("disable-software-rasterizer").
		Set("disable-setuid-sandbox")

	u, err := l.Launch()
	if err != nil {
		return Artifacts{}, fmt.Errorf("launch chromium: %w", err)
	}

	slog.Info("Chromium launched, connecting to DevTools protocol...")
	browser := rod.New().ControlURL(u).MustConnect()
	defer browser.MustClose()

	slog.Info("Opening new browser page...")
	page := browser.MustPage("")

	if err := page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width: 1920, Height: 1080,
	}); err != nil {
		return Artifacts{}, fmt.Errorf("set viewport: %w", err)
	}

	slog.Info("Loading page", "url", url)
	if err := page.Navigate(url); err != nil {
		return Artifacts{}, fmt.Errorf("navigate: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return Artifacts{}, fmt.Errorf("wait load: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	wait := page.Context(waitCtx).WaitRequestIdle(2*time.Second, nil, nil, nil)
	wait()

	g.closePopups(page)

	slog.Info("Scrolling page to load all images...")
	g.scrollToBottom(page)

	slog.Info("Waiting for images to load...")
	time.Sleep(3 * time.Second)

	pageText, err := g.extractPageText(page)
	if err != nil {
		slog.Warn("Failed to extract visible page text", "error", err)
	}

	slog.Info("Compressing images to reduce PDF size...")
	g.compressImages(page)
	time.Sleep(time.Second)

	// Desktop PDF (A4 landscape)
	slog.Info("Saving desktop PDF", "path", desktopPath)
	if err := g.savePDF(page, desktopPath, 29.7, 21.0, 1.0); err != nil {
		return Artifacts{}, fmt.Errorf("desktop PDF: %w", err)
	}
	slog.Info("Desktop PDF generated", "path", desktopPath)

	// Switch to mobile viewport
	slog.Info("Resizing viewport to mobile...")
	if err := page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width: 375, Height: 812,
	}); err != nil {
		return Artifacts{}, fmt.Errorf("set mobile viewport: %w", err)
	}
	time.Sleep(time.Second)

	slog.Info("Injecting mobile-friendly CSS...")
	g.injectMobileCSS(page)
	time.Sleep(500 * time.Millisecond)

	// Mobile PDF (A5 portrait)
	slog.Info("Saving mobile PDF", "path", mobilePath)
	if err := g.savePDF(page, mobilePath, 14.8, 21.0, 0.5); err != nil {
		return Artifacts{}, fmt.Errorf("mobile PDF: %w", err)
	}
	slog.Info("Mobile PDF generated", "path", mobilePath)

	g.CleanupOldPDFs()

	return Artifacts{
		DesktopPath: desktopPath,
		MobilePath:  mobilePath,
		PageText:    pageText,
	}, nil
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

	data, err := io.ReadAll(reader)
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
	viewportHeight := page.MustEval(`() => window.innerHeight`).Int()
	scrollHeight := page.MustEval(`() => document.body.scrollHeight`).Int()

	current := 0
	for current < scrollHeight {
		if IsShutdownRequested() {
			slog.Info("Shutdown requested, stopping scroll")
			return
		}
		current += viewportHeight
		page.MustEval(fmt.Sprintf(`() => window.scrollTo(0, %d)`, current))
		time.Sleep(300 * time.Millisecond)

		newHeight := page.MustEval(`() => document.body.scrollHeight`).Int()
		if newHeight > scrollHeight {
			scrollHeight = newHeight
		}
	}

	page.MustEval(`() => window.scrollTo(0, document.body.scrollHeight)`)
	time.Sleep(500 * time.Millisecond)
	page.MustEval(`() => window.scrollTo(0, 0)`)
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

func (g *Generator) extractPageText(page *rod.Page) (string, error) {
	result, err := page.Eval(`() => {
		const root = document.body || document.documentElement;
		if (!root) return '';

		const ignoredTags = new Set(['SCRIPT', 'STYLE', 'NOSCRIPT', 'SVG']);
		const lines = [];
		const seen = new Set();
		const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
			acceptNode(node) {
				const parent = node.parentElement;
				if (!parent || ignoredTags.has(parent.tagName)) return NodeFilter.FILTER_REJECT;

				const text = node.textContent.replace(/\s+/g, ' ').trim();
				if (!text) return NodeFilter.FILTER_REJECT;

				const style = window.getComputedStyle(parent);
				if (style.display === 'none' || style.visibility === 'hidden') return NodeFilter.FILTER_REJECT;

				const rect = parent.getBoundingClientRect();
				if (rect.width === 0 || rect.height === 0) return NodeFilter.FILTER_REJECT;

				return NodeFilter.FILTER_ACCEPT;
			}
		});

		while (walker.nextNode()) {
			const text = walker.currentNode.textContent.replace(/\s+/g, ' ').trim();
			if (!text || seen.has(text)) continue;
			seen.add(text);
			lines.push(text);
		}

		return lines.join('\n');
	}`)
	if err != nil {
		return "", fmt.Errorf("evaluate page text: %w", err)
	}

	return normalizePageText(result.Value.Str()), nil
}

func normalizePageText(input string) string {
	lines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	cleaned := make([]string, 0, len(lines))

	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		cleaned = append(cleaned, line)
	}

	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}
