package otlpconfig

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEndpointOverride(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		invalid     bool
	}{
		{"", "http://otel-collector:4317", false},
		{"127.0.0.1:14317", "http://127.0.0.1:14317", false},
		{"localhost:14317", "http://localhost:14317", false},
		{"[::1]:14317", "http://[::1]:14317", false},
		{"http://localhost:4317", "", false}, {"https://localhost:4317", "", false},
		{"unix:///tmp/otlp.sock", "", false},
		{"http://host:99999", "", true}, {"http://host:", "", true},
		{"localhost", "", true}, {"host:bad", "", true}, {"host:99999", "", true},
		{"ftp://host:4317", "", true}, {"https:///missing-host", "", true},
	} {
		t.Run(tc.input, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", tc.input)
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
			got, err := EndpointOverride("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
			if tc.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			}
		})
	}
}
