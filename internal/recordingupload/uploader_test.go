package recordingupload

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/recordstore"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestUploadFinalizedSegment(t *testing.T) {
	var putPath string
	var uploaded []byte
	var metadata http.Header
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response := func(statusCode int, headers http.Header, body string) *http.Response {
			return &http.Response{
				StatusCode: statusCode,
				Header:     headers,
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    r,
			}
		}

		if r.Method == http.MethodGet && r.URL.Query().Has("location") {
			return response(http.StatusOK, http.Header{"Content-Type": []string{"application/xml"}},
				`<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`), nil
		}

		if r.Method == http.MethodPut {
			putPath = r.URL.Path
			metadata = r.Header.Clone()
			var err error
			uploaded, err = io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			return response(http.StatusOK, http.Header{"ETag": []string{`"etag"`}}, ""), nil
		}

		return response(http.StatusNotFound, make(http.Header), "unexpected request"), nil
	})

	dir := t.TempDir()
	recordPath := filepath.Join(dir, "%path/%Y-%m-%d_%H-%M-%S-%f_%z")
	pathConf := &conf.Path{
		RecordPath:   recordPath,
		RecordFormat: conf.RecordFormatFMP4,
	}
	start := time.Date(2026, 11, 27, 12, 34, 56, 123000000, time.UTC)
	segmentPath := (recordstore.Path{Path: "camera1", Start: start}).EncodeWithLocation(
		recordstore.PathAddExtension(strings.ReplaceAll(recordPath, "%path", "camera1"), pathConf.RecordFormat),
		time.UTC)
	require.NoError(t, os.MkdirAll(filepath.Dir(segmentPath), 0o755))
	require.NoError(t, os.WriteFile(segmentPath, []byte("finalized segment"), 0o600))

	uploader, err := newWithTransport(conf.RecordingS3{
		Enabled:   true,
		Endpoint:  "http://s3.example.test",
		Bucket:    "recordings",
		Region:    "us-east-1",
		AccessKey: "access",
		SecretKey: "secret",
		Prefix:    "archive",
		Metadata:  map[string]string{"project": "%path"},
	}, transport)
	require.NoError(t, err)

	objectKey, err := uploader.Upload(context.Background(), pathConf, "camera1", segmentPath, 2*time.Second)
	require.NoError(t, err)
	require.Equal(t, "archive/camera1/2026-11-27_12-34-56-123000_Z.mp4", objectKey)
	require.Equal(t, "/recordings/"+objectKey, putPath)
	require.Contains(t, string(uploaded), "finalized segment")
	require.Equal(t, "camera1", metadata.Get("X-Amz-Meta-Mediamtx-Path"))
	require.Equal(t, start.Format(time.RFC3339Nano), metadata.Get("X-Amz-Meta-Mediamtx-Recording-Start"))
	require.Equal(t, "2.000", metadata.Get("X-Amz-Meta-Mediamtx-Recording-Duration"))
	require.Equal(t, "fmp4", metadata.Get("X-Amz-Meta-Mediamtx-Recording-Format"))
	require.Equal(t, "camera1", metadata.Get("X-Amz-Meta-Project"))
}

func TestCustomObjectKeyTemplate(t *testing.T) {
	dir := t.TempDir()
	recordPath := filepath.Join(dir, "%path/%Y-%m-%d_%H-%M-%S-%f")
	pathConf := &conf.Path{RecordPath: recordPath, RecordFormat: conf.RecordFormatMPEGTS}
	start := time.Date(2026, 11, 27, 1, 2, 3, 0, time.UTC)
	segmentPath := (recordstore.Path{Path: "camera1", Start: start}).EncodeWithLocation(
		recordstore.PathAddExtension(strings.ReplaceAll(recordPath, "%path", "camera1"), pathConf.RecordFormat),
		time.UTC)
	uploader := &Uploader{conf: conf.RecordingS3{ObjectKey: "streams/%path/%Y/%m/%d"}}

	key, _, err := uploader.objectKey(pathConf, "camera1", segmentPath)
	require.NoError(t, err)
	require.Equal(t, "streams/camera1/2026/11/27.ts", key)
}

func TestUploadWithRetry(t *testing.T) {
	var putCount int
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		statusCode := http.StatusOK
		body := ""
		if r.Method == http.MethodGet && r.URL.Query().Has("location") {
			body = `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`
		} else if r.Method == http.MethodPut {
			putCount++
			if putCount == 1 {
				statusCode = http.StatusInternalServerError
				body = `<Error><Code>InternalError</Code><Message>temporary failure</Message></Error>`
			}
		} else {
			statusCode = http.StatusNotFound
		}

		return &http.Response{
			StatusCode: statusCode,
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})

	dir := t.TempDir()
	recordPath := filepath.Join(dir, "%path/%Y-%m-%d_%H-%M-%S-%f")
	pathConf := &conf.Path{RecordPath: recordPath, RecordFormat: conf.RecordFormatMPEGTS}
	start := time.Date(2026, 11, 27, 1, 2, 3, 0, time.UTC)
	segmentPath := (recordstore.Path{Path: "camera1", Start: start}).EncodeWithLocation(
		recordstore.PathAddExtension(strings.ReplaceAll(recordPath, "%path", "camera1"), pathConf.RecordFormat),
		time.UTC)
	require.NoError(t, os.MkdirAll(filepath.Dir(segmentPath), 0o755))
	require.NoError(t, os.WriteFile(segmentPath, []byte("segment"), 0o600))

	uploader, err := newWithTransport(conf.RecordingS3{
		Endpoint: "http://s3.example.test", Bucket: "recordings", Region: "us-east-1",
		AccessKey: "access", SecretKey: "secret",
	}, transport)
	require.NoError(t, err)

	objectKey, err := uploader.UploadWithRetry(context.Background(), pathConf, "camera1", segmentPath, time.Second)
	require.NoError(t, err)
	require.Equal(t, "camera1/2026-11-27_01-02-03-000000.ts", objectKey)
	require.Equal(t, 2, putCount)
}

func TestUploadFailureKeepsLocalFile(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		statusCode := http.StatusInternalServerError
		body := `<Error><Code>InternalError</Code><Message>upload failed</Message></Error>`
		if r.Method == http.MethodGet && r.URL.Query().Has("location") {
			statusCode = http.StatusOK
			body = `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`
		}
		return &http.Response{
			StatusCode: statusCode,
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})

	dir := t.TempDir()
	recordPath := filepath.Join(dir, "%path/%Y-%m-%d_%H-%M-%S-%f")
	pathConf := &conf.Path{RecordPath: recordPath, RecordFormat: conf.RecordFormatMPEGTS}
	start := time.Date(2026, 11, 27, 1, 2, 3, 0, time.UTC)
	segmentPath := (recordstore.Path{Path: "camera1", Start: start}).EncodeWithLocation(
		recordstore.PathAddExtension(strings.ReplaceAll(recordPath, "%path", "camera1"), pathConf.RecordFormat),
		time.UTC)
	require.NoError(t, os.MkdirAll(filepath.Dir(segmentPath), 0o755))
	require.NoError(t, os.WriteFile(segmentPath, []byte("segment"), 0o600))

	uploader, err := newWithTransport(conf.RecordingS3{
		Endpoint: "http://s3.example.test", Bucket: "recordings", Region: "us-east-1",
		AccessKey: "access", SecretKey: "secret",
	}, transport)
	require.NoError(t, err)

	_, err = uploader.Upload(context.Background(), pathConf, "camera1", segmentPath, time.Second)
	require.Error(t, err)
	_, err = os.Stat(segmentPath)
	require.NoError(t, err)
}
