package conf_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bluenviron/mediamtx/internal/conf"
)

func TestRecordingS3Config(t *testing.T) {
	c, _, err := conf.Load(createTempFile(t, []byte("recordingS3:\n"+
		"  enabled: yes\n"+
		"  endpoint: http://localhost:9000\n"+
		"  bucket: recordings\n"+
		"  accessKey: access\n"+
		"  secretKey: secret\n")), nil, nil)
	require.NoError(t, err)
	require.Equal(t, "us-east-1", c.RecordingS3.Region)

	redacted := conf.Redact(c)
	require.Equal(t, "<redacted>", redacted.RecordingS3.AccessKey)
	require.Equal(t, "<redacted>", redacted.RecordingS3.SecretKey)

	data, err := json.Marshal(redacted.Global())
	require.NoError(t, err)
	var patch conf.OptionalGlobal
	require.NoError(t, json.Unmarshal(data, &patch))
	require.NoError(t, c.PatchGlobal(&patch))
	require.Equal(t, "access", c.RecordingS3.AccessKey)
	require.Equal(t, "secret", c.RecordingS3.SecretKey)
}

func TestRecordingS3ConfigValidation(t *testing.T) {
	for _, ca := range []struct {
		name   string
		config string
	}{
		{
			name: "missing endpoint",
			config: "recordingS3:\n" +
				"  enabled: yes\n" +
				"  bucket: recordings\n" +
				"  accessKey: access\n" +
				"  secretKey: secret\n",
		},
		{
			name: "endpoint with credentials",
			config: "recordingS3:\n" +
				"  enabled: yes\n" +
				"  endpoint: http://user:pass@localhost:9000\n" +
				"  bucket: recordings\n" +
				"  accessKey: access\n" +
				"  secretKey: secret\n",
		},
		{
			name: "invalid metadata key",
			config: "recordingS3:\n" +
				"  enabled: yes\n" +
				"  endpoint: http://localhost:9000\n" +
				"  bucket: recordings\n" +
				"  accessKey: access\n" +
				"  secretKey: secret\n" +
				"  metadata:\n" +
				"    invalid key: value\n",
		},
	} {
		t.Run(ca.name, func(t *testing.T) {
			_, _, err := conf.Load(createTempFile(t, []byte(ca.config)), nil, nil)
			require.Error(t, err)
		})
	}
}
