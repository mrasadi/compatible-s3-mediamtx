package conf

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var recordingS3MetadataKeyRE = regexp.MustCompile(`^[a-zA-Z0-9-]+$`)

// RecordingS3 contains settings for uploading completed recordings to S3.
type RecordingS3 struct {
	Enabled                bool              `json:"enabled"`
	Endpoint               string            `json:"endpoint"`
	Bucket                 string            `json:"bucket"`
	Region                 string            `json:"region"`
	AccessKey              string            `json:"accessKey"`
	SecretKey              string            `json:"secretKey"`
	Prefix                 string            `json:"prefix"`
	ObjectKey              string            `json:"objectKey"`
	Metadata               map[string]string `json:"metadata"`
	DeleteLocalAfterUpload bool              `json:"deleteLocalAfterUpload"`
}

func validateRecordingS3Template(name string, value string) error {
	for i := 0; i < len(value); i++ {
		if value[i] != '%' {
			continue
		}

		found := false
		for _, variable := range []string{"%path", "%Y", "%m", "%d", "%H", "%M", "%S", "%f", "%z", "%s"} {
			if strings.HasPrefix(value[i:], variable) {
				i += len(variable) - 1
				found = true
				break
			}
		}

		if !found {
			return fmt.Errorf("'%s' contains an unknown variable", name)
		}
	}

	return nil
}

func hasParentPathComponent(value string) bool {
	for _, component := range strings.Split(strings.ReplaceAll(value, "\\", "/"), "/") {
		if component == ".." {
			return true
		}
	}
	return false
}

func (c *RecordingS3) validate() error {
	if c.AccessKey == redactedCredential || c.SecretKey == redactedCredential {
		return fmt.Errorf("'recordingS3.accessKey' and 'recordingS3.secretKey' cannot be '%s'", redactedCredential)
	}

	if !c.Enabled {
		return nil
	}

	if c.Endpoint == "" {
		return fmt.Errorf("'recordingS3.endpoint' must be set when S3 recording upload is enabled")
	}

	endpoint, err := url.Parse(c.Endpoint)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" ||
		endpoint.User != nil || (endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return fmt.Errorf("'recordingS3.endpoint' must be an HTTP or HTTPS URL without credentials, a path, or query parameters")
	}

	if c.Bucket == "" {
		return fmt.Errorf("'recordingS3.bucket' must be set when S3 recording upload is enabled")
	}

	if c.Region == "" {
		c.Region = "us-east-1"
	}

	if c.AccessKey == "" || c.SecretKey == "" {
		return fmt.Errorf("'recordingS3.accessKey' and 'recordingS3.secretKey' must be set when S3 recording upload is enabled")
	}

	if err := validateRecordingS3Template("recordingS3.objectKey", c.ObjectKey); err != nil {
		return err
	}
	if hasParentPathComponent(c.Prefix) || hasParentPathComponent(c.ObjectKey) {
		return fmt.Errorf("'recordingS3.prefix' and 'recordingS3.objectKey' cannot contain '..' path components")
	}

	for key, value := range c.Metadata {
		if !recordingS3MetadataKeyRE.MatchString(key) {
			return fmt.Errorf("'recordingS3.metadata' key %q must contain only letters, numbers, and hyphens", key)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("'recordingS3.metadata.%s' cannot contain line breaks", key)
		}
	}

	return nil
}
