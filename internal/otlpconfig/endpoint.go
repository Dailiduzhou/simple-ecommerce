// Package otlpconfig preserves legacy bare OTLP addresses without replacing
// the SDK's URL, TLS, certificate and signal-specific environment handling.
package otlpconfig

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// EndpointOverride returns a plaintext URL only for the local default or a
// legacy host:port. Explicit URLs are validated and left to the SDK.
func EndpointOverride(signal string) (string, error) {
	endpoint := os.Getenv(signal)
	if endpoint == "" {
		endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	if endpoint == "" {
		return "http://otel-collector:4317", nil
	}
	if !strings.Contains(endpoint, "://") {
		host, port, err := net.SplitHostPort(endpoint)
		n, portErr := strconv.Atoi(port)
		if err != nil || host == "" || strings.ContainsAny(host, " /?#@\\\t\r\n") || portErr != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("invalid OTLP endpoint: expected a URL or host:port")
		}
		return "http://" + endpoint, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid OTLP endpoint URL: %w", err)
	}
	switch u.Scheme {
	case "http", "https":
		if u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return "", fmt.Errorf("invalid OTLP HTTP endpoint")
		}
		if port := u.Port(); port != "" {
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return "", fmt.Errorf("invalid OTLP endpoint port")
			}
		} else if strings.HasSuffix(u.Host, ":") {
			return "", fmt.Errorf("missing OTLP endpoint port")
		}
	case "unix":
		if u.Path == "" {
			return "", fmt.Errorf("invalid OTLP unix endpoint")
		}
	default:
		return "", fmt.Errorf("unsupported OTLP endpoint scheme %q", u.Scheme)
	}
	return "", nil
}
