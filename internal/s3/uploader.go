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
