package deploy_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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

// Run the real Compose merger: plain YAML parsing cannot detect accidentally
// appended port mappings or a storage profile that starts an existing MinIO.
// This does not need a Docker daemon, pull images or start any containers.
func TestTestEnvironmentCompose(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker CLI is not installed")
	}
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("Docker Compose plugin is not installed")
	}

	example, err := os.ReadFile("test.env.example")
	require.NoError(t, err)
	envFile := filepath.Join(t.TempDir(), "test.env")
	require.NoError(t, os.WriteFile(envFile, example, 0600))

	for _, profile := range []string{"storage", ""} {
		name := "existing-minio-service"
		if profile != "" {
			name = "existing-minio-image"
		}
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command("docker", "compose", "--project-name", "ecommerce-test",
				"--env-file", envFile, "-f", "../docker-compose.yml", "-f", "../docker-compose.test.yml",
				"config", "--format", "json")
			cmd.Env = append(os.Environ(),
				"COMPOSE_PROFILES="+profile,
				"MALL_IMAGE=ghcr.io/example/mall@sha256:"+strings.Repeat("a", 64),
				"MALL_BIND_ADDRESS=127.0.0.1", "MALL_HTTP_PORT=8000",
				"POSTGRES_PASSWORD=test-only", "REDIS_PASSWORD=test-only",
				"GRAFANA_ADMIN_PASSWORD=test-only",
				"AUTH_ACCESS_TOKEN_SECRET="+strings.Repeat("a", 32),
				"AUTH_REFRESH_TOKEN_SECRET="+strings.Repeat("b", 32),
				"AUTH_PHONE_SECRET="+strings.Repeat("c", 32),
				"MINIO_IMAGE=ecommerce-minio:preloaded",
				"STORAGE_ACCESS_KEY=test-app", "STORAGE_SECRET_KEY=test-only")
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			var compose struct {
				Services map[string]struct {
					Image       string
					Build       any
					PullPolicy  string `json:"pull_policy"`
					Environment map[string]string
					Ports       []struct {
						HostIP    string `json:"host_ip"`
						Target    int
						Published string
					}
				}
			}
			require.NoError(t, json.Unmarshal(output, &compose))
			for _, service := range []string{"postgres", "redis"} {
				require.Contains(t, compose.Services, service)
				require.Empty(t, compose.Services[service].Ports, "%s must not publish ports", service)
			}
			mall := compose.Services["mall"]
			require.Contains(t, mall.Image, "@sha256:")
			require.Len(t, mall.Ports, 1, "must replace, not append to base HTTP/gRPC ports")
			require.Equal(t, "127.0.0.1", mall.Ports[0].HostIP)
			require.Equal(t, 8000, mall.Ports[0].Target)
			require.Equal(t, "8000", mall.Ports[0].Published)
			require.Equal(t, "s3", mall.Environment["STORAGE_PROVIDER"])
			require.NotEmpty(t, mall.Environment["STORAGE_ACCESS_KEY"])
			require.Equal(t, "true", mall.Environment["STORAGE_PUBLIC_USE_TLS"])

			if profile == "" {
				require.NotContains(t, compose.Services, "minio", "must not manage an existing MinIO service")
				return
			}
			require.Contains(t, compose.Services, "minio")
			minio := compose.Services["minio"]
			require.Equal(t, "ecommerce-minio:preloaded", minio.Image)
			require.Nil(t, minio.Build, "must never build MinIO on the test server")
			require.Equal(t, "never", minio.PullPolicy, "must use the server's existing MinIO image")
			require.Len(t, minio.Ports, 2)
			for _, port := range minio.Ports {
				require.Equal(t, "127.0.0.1", port.HostIP)
			}
		})
	}
}
