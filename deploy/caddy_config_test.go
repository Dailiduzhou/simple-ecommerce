package deploy_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCaddyCompose(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("Docker Compose plugin is not installed")
	}
	example, err := os.ReadFile("test.env.example")
	require.NoError(t, err)
	envFile := filepath.Join(t.TempDir(), "test.env")
	require.NoError(t, os.WriteFile(envFile, example, 0600))
	cmd := exec.Command("docker", "compose", "--project-name", "ecommerce-test",
		"--env-file", envFile, "-f", "../docker-compose.yml", "-f", "../docker-compose.test.yml",
		"-f", "../docker-compose.caddy.yml", "config", "--format", "json")
	cmd.Env = append(os.Environ(),
		"COMPOSE_PROFILES=", "MALL_IMAGE=registry.example.com/example/aiyoung@sha256:"+strings.Repeat("a", 64),
		"POSTGRES_PASSWORD=test-only", "REDIS_PASSWORD=test-only", "GRAFANA_ADMIN_PASSWORD=test-only",
		"AUTH_ACCESS_TOKEN_SECRET="+strings.Repeat("a", 32),
		"AUTH_REFRESH_TOKEN_SECRET="+strings.Repeat("b", 32), "AUTH_PHONE_SECRET="+strings.Repeat("c", 32),
		"MINIO_IMAGE=registry.example.com/example/aiyoung@sha256:"+strings.Repeat("b", 64),
		"MINIO_ROOT_USER=test-admin", "MINIO_ROOT_PASSWORD=test-admin-secret",
		"STORAGE_ACCESS_KEY=test-app", "STORAGE_SECRET_KEY=test-app-secret",
		"API_DOMAIN=api.example.com", "MINIO_DOMAIN=minio.example.com",
		"CONSOLE_DOMAIN=console.example.com", "CONSOLE_ALLOWED_CIDRS=192.0.2.1/32",
		// A stale server .env must not silently turn off TLS protection.
		"STORAGE_USE_TLS=false", "STORAGE_PUBLIC_USE_TLS=false", "STORAGE_ALLOW_INSECURE=true")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	var compose struct {
		Services map[string]struct {
			Image       string
			Build       any
			PullPolicy  string `json:"pull_policy"`
			Profiles    []string
			NetworkMode string      `json:"network_mode"`
			MemLimit    json.Number `json:"mem_limit"`
			Environment map[string]string
			Ports       []struct {
				HostIP    string `json:"host_ip"`
				Target    int
				Published string
			}
			Networks map[string]struct {
				IPv4Address string `json:"ipv4_address"`
				Aliases     []string
			}
			Volumes []struct {
				Source   string
				Target   string
				ReadOnly bool `json:"read_only"`
				Bind     struct {
					CreateHostPath bool `json:"create_host_path"`
				}
			}
		}
	}
	require.NoError(t, json.Unmarshal(output, &compose))
	var total int64
	for name, svc := range compose.Services {
		limit, err := svc.MemLimit.Int64()
		require.NoError(t, err)
		require.Positive(t, limit, name)
		total += limit
		if name == "caddy" {
			continue
		}
		for _, port := range svc.Ports {
			require.Equal(t, "127.0.0.1", port.HostIP, "%s must not be public", name)
		}
	}
	// Caddy adds 128 MiB to the original 3 GiB stack, leaving 896 MiB on 4 GiB.
	require.LessOrEqual(t, total, int64(3200*1024*1024))
	for _, name := range []string{"mall", "postgres", "redis", "minio"} {
		require.Contains(t, compose.Services, name)
		require.Empty(t, compose.Services[name].Ports, name)
	}
	mall := compose.Services["mall"]
	require.Equal(t, "*", mall.Environment["HTTP_CORS_ALLOWED_ORIGINS"],
		"the frontend is deployed separately, so CORS defaults to every origin")
	require.Equal(t, "172.30.50.10/32", mall.Environment["HTTP_TRUSTED_PROXY"])
	for _, key := range []string{"STORAGE_ENDPOINT", "STORAGE_PUBLIC_ENDPOINT"} {
		require.Equal(t, "minio.example.com", mall.Environment[key])
	}
	require.Equal(t, "true", mall.Environment["STORAGE_USE_TLS"])
	require.Equal(t, "true", mall.Environment["STORAGE_PUBLIC_USE_TLS"])
	require.Equal(t, "false", mall.Environment["STORAGE_ALLOW_INSECURE"])
	minio := compose.Services["minio"]
	require.Nil(t, minio.Build)
	require.Empty(t, minio.Profiles, "Caddy deployment must start MinIO without a profile")
	require.Equal(t, "missing", minio.PullPolicy, "ACR MinIO must be pullable, not limited to preloaded images")
	require.Equal(t, "registry.example.com/example/aiyoung@sha256:"+strings.Repeat("b", 64), minio.Image)
	require.Equal(t, "registry.example.com/example/aiyoung@sha256:"+strings.Repeat("a", 64), mall.Image)
	require.Equal(t, "https://console.example.com", minio.Environment["MINIO_BROWSER_REDIRECT_URL"])
	require.Equal(t, "*", minio.Environment["MINIO_API_CORS_ALLOW_ORIGIN"])
	caddy := compose.Services["caddy"]
	require.Len(t, caddy.Ports, 3)
	for _, port := range caddy.Ports {
		require.Contains(t, []int{80, 443}, port.Target, "never publish Caddy's admin API")
	}
	require.Equal(t, "172.30.50.10", caddy.Networks["edge"].IPv4Address)
	require.ElementsMatch(t, []string{"api.example.com", "minio.example.com", "console.example.com"}, caddy.Networks["edge"].Aliases)
	require.Equal(t, "192.0.2.1/32", caddy.Environment["CONSOLE_ALLOWED_CIDRS"])
	mounts := make(map[string]string)
	for _, volume := range caddy.Volumes {
		mounts[volume.Target] = volume.Source
	}
	require.Equal(t, "caddy_data", mounts["/data"])
	require.Equal(t, "caddy_config", mounts["/config"])
	require.NotContains(t, mounts, "/srv/web", "the frontend is not deployed with the backend")
	require.NotContains(t, compose.Services, "caddy-check", "normal deployments must not run the validator")

	args := append([]string{}, cmd.Args[1:len(cmd.Args)-3]...)
	args = append(args, "--profile", "validation", "config", "--format", "json")
	validate := exec.Command("docker", args...)
	validate.Env = cmd.Env
	output, err = validate.CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.NoError(t, json.Unmarshal(output, &compose))
	checker := compose.Services["caddy-check"]
	require.Equal(t, "none", checker.NetworkMode)
	require.Empty(t, checker.Networks, "validator must not claim the live Caddy static IP")
	require.Empty(t, checker.Ports)
	require.Equal(t, caddy.Image, checker.Image)
}
