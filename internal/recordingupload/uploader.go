package recordingupload

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/recordstore"
)

// Uploader uploads finalized recording segments to an S3-compatible service.
type Uploader struct {
	client *minio.Client
	conf   conf.RecordingS3
}

// New creates an uploader from the S3 configuration.
func New(c conf.RecordingS3) (*Uploader, error) {
	return newWithTransport(c, nil)
}

func newWithTransport(c conf.RecordingS3, transport http.RoundTripper) (*Uploader, error) {
	endpoint, err := url.Parse(c.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid S3 endpoint: %w", err)
	}

	client, err := minio.New(endpoint.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(c.AccessKey, c.SecretKey, ""),
		Secure:       endpoint.Scheme == "https",
		Region:       c.Region,
		BucketLookup: minio.BucketLookupPath,
		Transport:    transport,
		MaxRetries:   1,
	})
	if err != nil {
		return nil, fmt.Errorf("create S3 client: %w", err)
	}

	return &Uploader{
		client: client,
		conf:   c,
	}, nil
}

func recordingPathFormat(pathConf *conf.Path) string {
	return recordstore.PathAddExtension(
		pathConf.RecordPath,
		pathConf.RecordFormat,
	)
}

func (u *Uploader) objectKey(pathConf *conf.Path, pathName string, segmentPath string) (string, recordstore.Path, error) {
	var segment recordstore.Path
	if !segment.Decode(recordingPathFormat(pathConf), segmentPath) {
		return "", segment, fmt.Errorf("cannot parse recording segment path")
	}

	var objectKey string
	if u.conf.ObjectKey != "" {
		objectFormat := recordstore.PathAddExtension(u.conf.ObjectKey, pathConf.RecordFormat)
		objectKey = segment.Encode(objectFormat)
	} else {
		basePath := recordstore.CommonPath(pathConf.RecordPath)
		if basePath == "" {
			basePath = "."
			if filepath.IsAbs(segmentPath) {
				basePath = string(filepath.Separator)
			}
		}
		relativePath, err := filepath.Rel(basePath, segmentPath)
		if err != nil {
			return "", segment, fmt.Errorf("resolve recording object key: %w", err)
		}
		objectKey = filepath.ToSlash(relativePath)
	}

	objectKey = strings.TrimPrefix(objectKey, "/")
	if objectKey == "" || objectKey == "." || path.IsAbs(objectKey) || objectKey == ".." || strings.HasPrefix(objectKey, "../") {
		return "", segment, fmt.Errorf("recording object key is empty or escapes its prefix")
	}

	prefix := strings.Trim(u.conf.Prefix, "/")
	if prefix != "" {
		objectKey = path.Join(prefix, objectKey)
	}

	return objectKey, segment, nil
}

func (u *Uploader) metadata(pathName string, pathConf *conf.Path, segment recordstore.Path, duration time.Duration) map[string]string {
	metadata := make(map[string]string, len(u.conf.Metadata)+4)
	for key, value := range u.conf.Metadata {
		metadata[key] = segment.Encode(value)
	}

	metadata["mediamtx-path"] = pathName
	metadata["mediamtx-recording-start"] = segment.Start.UTC().Format(time.RFC3339Nano)
	metadata["mediamtx-recording-duration"] = fmt.Sprintf("%.3f", duration.Seconds())
	metadata["mediamtx-recording-format"] = string(pathConf.RecordFormat)
	return metadata
}

// Upload sends a finalized segment and its recording metadata to S3.
func (u *Uploader) Upload(
	ctx context.Context,
	pathConf *conf.Path,
	pathName string,
	segmentPath string,
	duration time.Duration,
) (string, error) {
	objectKey, segment, err := u.objectKey(pathConf, pathName, segmentPath)
	if err != nil {
		return "", err
	}

	contentType := mime.TypeByExtension(filepath.Ext(segmentPath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	_, err = u.client.FPutObject(ctx, u.conf.Bucket, objectKey, segmentPath, minio.PutObjectOptions{
		ContentType:  contentType,
		UserMetadata: u.metadata(pathName, pathConf, segment, duration),
	})
	if err != nil {
		return objectKey, fmt.Errorf("upload recording to S3: %w", err)
	}

	return objectKey, nil
}

// UploadWithRetry retries a failed segment upload twice with a short backoff.
func (u *Uploader) UploadWithRetry(
	ctx context.Context,
	pathConf *conf.Path,
	pathName string,
	segmentPath string,
	duration time.Duration,
) (string, error) {
	var objectKey string
	var err error
	for attempt := range 3 {
		objectKey, err = u.Upload(ctx, pathConf, pathName, segmentPath, duration)
		if err == nil {
			return objectKey, nil
		}

		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return objectKey, ctx.Err()
			}
		}
	}

	return objectKey, err
}
