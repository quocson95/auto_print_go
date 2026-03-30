package thumbnail

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
