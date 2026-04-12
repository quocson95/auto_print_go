package s3

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
	"path"
	"path/filepath"
	"regexp"
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

func (u *Uploader) UploadStaticFile(ctx context.Context, filePath, s3Key, contentType, cacheControl string) (string, error) {
	slog.Info("Uploading static file to S3", "file", filepath.Base(filePath), "key", s3Key)

	presignResult, err := u.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:       aws.String(u.cfg.S3BucketName),
		Key:          aws.String(s3Key),
		ContentType:  aws.String(contentType),
		ACL:          "public-read",
		CacheControl: aws.String(cacheControl),
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
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("x-amz-acl", "public-read")
	req.Header.Set("Cache-Control", cacheControl)

	httpClient := &http.Client{Timeout: 2 * time.Minute}
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
	Name         string `json:"name,omitempty"`
	Title        string `json:"title,omitempty"`
	LastModified string `json:"lastModified"`
	Size         int64  `json:"size"`
}

type MigrationOptions struct {
	Overrides map[string]string
	DryRun    bool
	RenameKeys bool
}

type MigrationResult struct {
	TotalEntries      int
	UpdatedTitles     int
	RenamedKeys       int
	RemovedDuplicates int
	CopiedObjects     int
}

type copyOperation struct {
	FromKey string
	ToKey   string
}

func MigrateIndexEntries(entries []IndexEntry, overrides map[string]string) ([]IndexEntry, MigrationResult) {
	updated, result, _ := migrateIndexEntriesWithOptions(entries, overrides, false)
	return updated, result
}

func ReadIndexEntriesFile(filePath string) ([]IndexEntry, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var entries []IndexEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}

	return entries, nil
}

func WriteIndexEntriesFile(filePath string, entries []IndexEntry) error {
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}

	return os.WriteFile(filePath, data, 0o644)
}

func (u *Uploader) UpdateIndexJSON(ctx context.Context, newEntries []IndexEntry) error {
	data, err := u.ReadIndexJSON(ctx)
	if err != nil {
		return err
	}

	updated := mergeIndexEntries(data, newEntries)
	if err := u.WriteIndexJSON(ctx, updated); err != nil {
		return err
	}

	slog.Info("Successfully updated index.json", "entries", len(newEntries))
	return nil
}

func (u *Uploader) ReadIndexJSON(ctx context.Context) ([]IndexEntry, error) {
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

	return data, nil
}

func (u *Uploader) WriteIndexJSON(ctx context.Context, entries []IndexEntry) error {
	indexKey := "pdfs/index.json"

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].LastModified > entries[j].LastModified
	})

	jsonBytes, err := json.Marshal(entries)
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

	return nil
}

func (u *Uploader) MigrateIndexJSON(ctx context.Context, opts MigrationOptions) (MigrationResult, error) {
	data, err := u.ReadIndexJSON(ctx)
	if err != nil {
		return MigrationResult{}, err
	}

	updated, result, copies := migrateIndexEntriesWithOptions(data, opts.Overrides, opts.RenameKeys)
	if opts.DryRun {
		return result, nil
	}

	for _, op := range copies {
		if err := u.copyObjectIfNeeded(ctx, op.FromKey, op.ToKey); err != nil {
			return result, fmt.Errorf("copy %s -> %s: %w", op.FromKey, op.ToKey, err)
		}
		result.CopiedObjects++
	}

	if err := u.WriteIndexJSON(ctx, updated); err != nil {
		return result, err
	}

	return result, nil
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

func mergeIndexEntries(existing, newEntries []IndexEntry) []IndexEntry {
	dataMap := make(map[string]IndexEntry, len(existing))
	for _, e := range existing {
		dataMap[e.Key] = e
	}
	for _, e := range newEntries {
		if legacyKey := legacyKeyForEntry(e); legacyKey != "" && legacyKey != e.Key {
			delete(dataMap, legacyKey)
		}
		dataMap[e.Key] = e
		slog.Info("Adding/Updating entry", "key", e.Key)
	}

	updated := make([]IndexEntry, 0, len(dataMap))
	for _, e := range dataMap {
		updated = append(updated, e)
	}
	return updated
}

func migrateIndexEntries(entries []IndexEntry, overrides map[string]string) ([]IndexEntry, MigrationResult, []copyOperation) {
	return migrateIndexEntriesWithOptions(entries, overrides, true)
}

func migrateIndexEntriesWithOptions(entries []IndexEntry, overrides map[string]string, renameKeys bool) ([]IndexEntry, MigrationResult, []copyOperation) {
	result := MigrationResult{TotalEntries: len(entries)}
	dataMap := make(map[string]IndexEntry, len(entries))
	var copies []copyOperation

	for _, entry := range entries {
		if !isManagedPDFKey(entry.Key) {
			if _, exists := dataMap[entry.Key]; exists {
				result.RemovedDuplicates++
			}
			dataMap[entry.Key] = entry
			continue
		}

		originalKey := entry.Key
		originalTitle := strings.TrimSpace(entry.Title)
		originalName := strings.TrimSpace(entry.Name)

		title := resolveOverrideTitle(entry, overrides)
		if title == "" {
			title = deriveTitleFromKey(entry.Key)
		}
		title = strings.TrimSpace(strings.Join(strings.Fields(title), " "))

		if title != "" {
			entry.Title = title
			entry.Name = title
		}

		if renameKeys && entry.Key != "" && title != "" {
			if targetKey := keyForTitle(entry.Key, title); targetKey != "" && targetKey != entry.Key {
				entry.Key = targetKey
				copies = append(copies, copyOperation{FromKey: originalKey, ToKey: targetKey})
				result.RenamedKeys++
			}
		}

		if entry.Title != originalTitle || entry.Name != originalName {
			result.UpdatedTitles++
		}

		if _, exists := dataMap[entry.Key]; exists {
			result.RemovedDuplicates++
		}
		dataMap[entry.Key] = entry
	}

	updated := make([]IndexEntry, 0, len(dataMap))
	for _, entry := range dataMap {
		updated = append(updated, entry)
	}

	sort.Slice(updated, func(i, j int) bool {
		return updated[i].LastModified > updated[j].LastModified
	})

	return updated, result, dedupeCopyOperations(copies)
}

func dedupeCopyOperations(copies []copyOperation) []copyOperation {
	seen := make(map[string]struct{}, len(copies))
	deduped := make([]copyOperation, 0, len(copies))
	for _, op := range copies {
		key := op.FromKey + "->" + op.ToKey
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, op)
	}
	return deduped
}

func resolveOverrideTitle(entry IndexEntry, overrides map[string]string) string {
	if len(overrides) == 0 {
		return ""
	}

	if title := strings.TrimSpace(overrides[entry.Key]); title != "" {
		return title
	}

	stem := deriveTitleFromKey(entry.Key)
	if title := strings.TrimSpace(overrides[stem]); title != "" {
		return title
	}

	return ""
}

func deriveTitleFromKey(key string) string {
	base := strings.TrimSuffix(filepath.Base(key), ".pdf")
	base = strings.TrimPrefix(base, "[PC] ")
	base = strings.TrimPrefix(base, "[Mobile] ")
	return strings.TrimSpace(base)
}

func isManagedPDFKey(key string) bool {
	lower := strings.ToLower(key)
	if !strings.HasSuffix(lower, ".pdf") {
		return false
	}

	base := filepath.Base(key)
	return strings.HasPrefix(base, "[PC] ") || strings.HasPrefix(base, "[Mobile] ")
}

func keyForTitle(existingKey, title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}

	dir := filepath.Dir(existingKey)
	base := filepath.Base(existingKey)
	prefix := ""
	switch {
	case strings.HasPrefix(base, "[PC] "):
		prefix = "[PC] "
	case strings.HasPrefix(base, "[Mobile] "):
		prefix = "[Mobile] "
	}

	filename := normalizedFilename(title)
	if filename == "" {
		return ""
	}

	if dir == "." || dir == "" {
		return prefix + filename
	}
	return path.Join(dir, prefix+filename)
}

func normalizedFilename(subject string) string {
	subject = strings.TrimPrefix(subject, "Fwd ")
	subject = strings.TrimPrefix(subject, "Fwd: ")
	subject = strings.Join(strings.Fields(subject), " ")

	re := regexp.MustCompile(`[^\p{L}\p{N} \-_]`)
	clean := strings.TrimSpace(re.ReplaceAllString(subject, ""))
	if clean == "" {
		return ""
	}

	runes := []rune(clean)
	if len(runes) > 50 {
		clean = string(runes[:50])
	}

	return clean + ".pdf"
}

func legacyKeyForEntry(entry IndexEntry) string {
	title := strings.TrimSpace(entry.Title)
	if title == "" {
		title = strings.TrimSpace(entry.Name)
	}
	if title == "" {
		return ""
	}

	base := filepath.Base(entry.Key)
	prefix := ""
	switch {
	case strings.HasPrefix(base, "[PC] "):
		prefix = "[PC] "
	case strings.HasPrefix(base, "[Mobile] "):
		prefix = "[Mobile] "
	default:
		return ""
	}

	legacyBase := legacyFilename(title)
	if legacyBase == "" {
		return ""
	}

	return "pdfs/" + prefix + legacyBase
}

func legacyFilename(subject string) string {
	subject = strings.TrimPrefix(subject, "Fwd ")
	subject = strings.TrimPrefix(subject, "Fwd: ")

	re := regexp.MustCompile(`[^a-zA-Z0-9 \-_]`)
	clean := strings.TrimSpace(re.ReplaceAllString(subject, ""))
	if len(clean) > 50 {
		clean = clean[:50]
	}
	if clean == "" {
		return ""
	}

	return clean + ".pdf"
}

func (u *Uploader) copyObjectIfNeeded(ctx context.Context, fromKey, toKey string) error {
	if fromKey == "" || toKey == "" || fromKey == toKey {
		return nil
	}

	_, err := u.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(u.cfg.S3BucketName),
		Key:    aws.String(toKey),
	})
	if err == nil {
		return nil
	}

	copySource := url.PathEscape(path.Join(u.cfg.S3BucketName, fromKey))
	copySource = strings.ReplaceAll(copySource, "%2F", "/")

	_, err = u.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:       aws.String(u.cfg.S3BucketName),
		Key:          aws.String(toKey),
		CopySource:   aws.String(copySource),
		ACL:          "public-read",
		CacheControl: aws.String("max-age=31536000"),
		ContentType:  aws.String("application/pdf"),
	})
	if err != nil {
		return err
	}

	return nil
}
