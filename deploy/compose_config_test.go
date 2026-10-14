package deploy_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The monitoring stack (Grafana, Jaeger, Prometheus, Loki, OTLP) has no
// authentication of its own. Publishing those ports on every interface — or
// granting anonymous visitors the Grafana Admin role — hands organization and
// datasource control to anyone who can reach the host. Assert the compose
// files keep them loopback-only and anonymous access at most read-only.
func TestMonitoringStackIsNotExposedUnauthenticated(t *testing.T) {
	for _, file := range []string{"../docker-compose.yml", "../docker-compose.dev.yml"} {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(file)
			require.NoError(t, err)

			var compose struct {
				Services map[string]struct {
					Environment map[string]string `yaml:"environment"`
					Ports       []string          `yaml:"ports"`
				}
			}
			require.NoError(t, yaml.Unmarshal(raw, &compose))
			require.NotEmpty(t, compose.Services)

			monitored := map[string]bool{"grafana": true, "jaeger": true, "prometheus": true, "loki": true, "otel-collector": true}
			for name, service := range compose.Services {
				if !monitored[name] {
					continue
				}
				for _, port := range service.Ports {
					parts := strings.Split(port, ":")
					require.Len(t, parts, 3, "unexpected port mapping %q in %s", port, name)
					require.Equal(t, "127.0.0.1", parts[0],
						"%s must only publish %s on the loopback interface, got %q", file, name, port)
				}
				if name == "grafana" {
					role := strings.ToLower(service.Environment["GF_AUTH_ANONYMOUS_ORG_ROLE"])
					anonymous := strings.EqualFold(service.Environment["GF_AUTH_ANONYMOUS_ENABLED"], "true")
					require.False(t, anonymous && role == "admin",
						"grafana must never grant anonymous visitors the Admin role")
					if anonymous {
						require.Equal(t, "viewer", role, "anonymous grafana access may only be read-only")
					}
				}
			}
		})
	}
}

func TestProductionDeploymentSecurityDefaults(t *testing.T) {
	for _, file := range []string{"../docker-compose.yml", "../docker-compose.dev.yml"} {
		raw, err := os.ReadFile(file)
		require.NoError(t, err)
		var compose struct {
			Services map[string]struct {
				Image       string            `yaml:"image"`
				Environment map[string]string `yaml:"environment"`
				Ports       []string          `yaml:"ports"`
			}
		}
		require.NoError(t, yaml.Unmarshal(raw, &compose))
		for name, service := range compose.Services {
			for _, port := range service.Ports {
				if name == "mall" && port == "8000:8000" {
					continue
				}
				// The S3 endpoint hands out presigned upload URLs, so clients must be
				// able to reach it directly; its console stays on loopback.
				if name == "minio" && port == "9002:9000" {
					continue
				}
				require.True(t, strings.HasPrefix(port, "127.0.0.1:"), "private service port: %s %s", name, port)
			}
			if name == "minio" {
				// Upstream only publishes a moving `latest`, so the digest is the pin.
				require.Contains(t, service.Image, "@sha256:", "object storage must be pinned by digest: %s", service.Image)
			}
		}
		if file == "../docker-compose.yml" {
			env := compose.Services["mall"].Environment
			require.Equal(t, "true", env["APP_IS_PRODUCTION"])
			require.Equal(t, "true", env["STORAGE_USE_TLS"])
			require.Equal(t, "true", env["STORAGE_PUBLIC_USE_TLS"])
			require.Equal(t, "false", env["STORAGE_ALLOW_INSECURE"])
			require.NotContains(t, env["STORAGE_PUBLIC_ENDPOINT"], "localhost")
		}
	}
}
