package deploy_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// Resource caps are part of the deployment contract: the test server is a
// small 2-core/4 GB host, so every service must declare a memory limit (the
// base file is exactly what GitHub Actions deploys, per
// .github/workflows/go-test.yml), postgres may use or disable host swap,
// and the mall service must bound trace volume through the standard
// OTel sampling variables. Declared caps total ~2.5 GB (~3 GB with the
// optional MinIO overlay), leaving >= 1 GB for the kernel, dockerd, page
// cache and bursts.
func TestResourceLimitsAndSampling(t *testing.T) {
	type service struct {
		MemLimit     string            `yaml:"mem_limit"`
		MemswapLimit string            `yaml:"memswap_limit"`
		Environment  map[string]string `yaml:"environment"`
		Command      any               `yaml:"command"`
		Volumes      []string          `yaml:"volumes"`
		Restart      string            `yaml:"restart"`
	}

	redisConfig, err := os.ReadFile("redis.conf")
	require.NoError(t, err)
	redisSettings := make(map[string]string)
	for _, line := range strings.Split(string(redisConfig), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		require.Len(t, fields, 2, "unexpected Redis directive: %q", line)
		redisSettings[fields[0]] = fields[1]
	}
	redisBudget, err := sizeToMB(redisSettings["maxmemory"])
	require.NoError(t, err)
	require.Positive(t, redisBudget, "Redis must have an internal memory budget")
	require.Equal(t, "noeviction", redisSettings["maxmemory-policy"],
		"Redis must not evict sessions, refresh replay markers or limiter state")

	for _, file := range []string{"../docker-compose.yml", "../docker-compose.dev.yml"} {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(file)
			require.NoError(t, err)

			var compose struct {
				Services map[string]service `yaml:"services"`
			}
			require.NoError(t, yaml.Unmarshal(raw, &compose))
			require.NotEmpty(t, compose.Services)

			total := 0.0
			for name, svc := range compose.Services {
				require.NotEmpty(t, svc.MemLimit, "%s must declare mem_limit", name)
				mb, err := sizeToMB(svc.MemLimit)
				require.NoError(t, err, "%s mem_limit %q", name, svc.MemLimit)
				require.Positive(t, mb, "%s must have a positive memory cap", name)
				total += mb
			}
			// 4 GB host: keep >= 1 GB (25%) for the OS, dockerd, page cache, bursts.
			require.LessOrEqual(t, total, 3072.0,
				"declared mem_limit total %.0f MB must fit a 2c/4 GB host with >= 1 GB headroom", total)

			pg := compose.Services["postgres"]
			pgLimit, err := sizeToMB(pg.MemLimit)
			require.NoError(t, err)
			pgSwap, err := sizeToMB(pg.MemswapLimit)
			require.NoError(t, err)
			require.GreaterOrEqual(t, pgSwap, pgLimit,
				"postgres RAM+swap budget must cover RAM; equality disables swap")

			redis := compose.Services["redis"]
			require.Contains(t, redis.Volumes, "./deploy/redis.conf:/usr/local/etc/redis/redis.conf:ro")
			require.Contains(t, fmt.Sprint(redis.Command), "/usr/local/etc/redis/redis.conf")
			redisLimit, err := sizeToMB(redis.MemLimit)
			require.NoError(t, err)
			require.LessOrEqual(t, redisBudget, redisLimit*0.5,
				"Redis must reserve headroom for allocator, buffers and persistence overhead")

			mall := compose.Services["mall"]
			require.Equal(t, "parentbased_traceidratio", mall.Environment["OTEL_TRACES_SAMPLER"],
				"mall must head-sample traces; all-in-one Jaeger keeps them in memory")
			arg := mall.Environment["OTEL_TRACES_SAMPLER_ARG"]
			require.NotEmpty(t, arg)
			ratio, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimPrefix(arg, "${OTEL_TRACES_SAMPLER_ARG:-"), "}"), 64)
			require.NoError(t, err, "sampler arg must default to a parseable ratio: %q", arg)
			require.GreaterOrEqual(t, ratio, 0.0)
			require.LessOrEqual(t, ratio, 1.0)

			jaeger := compose.Services["jaeger"]
			maxTraces, err := strconv.ParseUint(jaeger.Environment["MEMORY_MAX_TRACES"], 10, 64)
			require.NoError(t, err,
				"in-memory Jaeger storage must be capped by trace count")
			require.Positive(t, maxTraces)
			jaegerMem, err := sizeToMB(jaeger.MemLimit)
			require.NoError(t, err)
			// A trace count is not a byte budget. Keep an independent Go soft
			// limit below the cgroup cap, and recover from unexpectedly large
			// traces rather than pretending every trace occupies a fixed size.
			goBudget, err := sizeToMB(jaeger.Environment["GOMEMLIMIT"])
			require.NoError(t, err)
			require.Positive(t, goBudget)
			require.LessOrEqual(t, goBudget, jaegerMem*0.75,
				"Jaeger must leave headroom outside Go-managed memory")
			require.Equal(t, "unless-stopped", jaeger.Restart)
		})
	}
}

// sizeToMB converts the binary size units used by our Compose, Redis and Go
// configurations to MiB (e.g. 512m, 64mb, 192MiB, 268435456).
func sizeToMB(v string) (float64, error) {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return 0, fmt.Errorf("empty size")
	}
	mult := 1.0 / (1024 * 1024) // plain bytes
	for _, unit := range []struct {
		suffix string
		mult   float64
	}{
		{"gib", 1024}, {"gb", 1024}, {"g", 1024},
		{"mib", 1}, {"mb", 1}, {"m", 1},
		{"kib", 1.0 / 1024}, {"kb", 1.0 / 1024}, {"k", 1.0 / 1024},
		{"b", 1.0 / (1024 * 1024)},
	} {
		if strings.HasSuffix(v, unit.suffix) {
			mult = unit.mult
			v = strings.TrimSuffix(v, unit.suffix)
			break
		}
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, err
	}
	return n * mult, nil
}

func TestSizeToMB(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  float64
	}{
		{"512m", 512}, {"1g", 1024}, {"268435456", 256},
		{"64mb", 64}, {"192MiB", 192}, {" 1GiB ", 1024},
		{"1KiB", 1.0 / 1024}, {"32b", 32.0 / (1024 * 1024)},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := sizeToMB(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
	for _, input := range []string{"", "garbage", "12tb"} {
		_, err := sizeToMB(input)
		require.Error(t, err, "invalid size %q", input)
	}
}

// Log retention must stay bounded: the stock Loki local-config keeps every
// log forever, so the compose files must mount our own config with the
// compactor enabled, and Prometheus must set an explicit TSDB retention.
func TestLogRetentionIsBounded(t *testing.T) {
	for _, file := range []string{"../docker-compose.yml", "../docker-compose.dev.yml"} {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(file)
			require.NoError(t, err)
			var compose struct {
				Services map[string]struct {
					Command interface{} `yaml:"command"`
					Volumes []string    `yaml:"volumes"`
				}
			}
			require.NoError(t, yaml.Unmarshal(raw, &compose))

			loki := compose.Services["loki"]
			mounted := false
			for _, v := range loki.Volumes {
				if strings.HasPrefix(v, "./deploy/loki-config.yaml") {
					mounted = true
				}
			}
			require.True(t, mounted, "loki must mount deploy/loki-config.yaml, not the stock local-config")

			cfg, err := os.ReadFile("loki-config.yaml")
			require.NoError(t, err)
			var lokiCfg struct {
				LimitsConfig struct {
					RetentionPeriod string `yaml:"retention_period"`
				} `yaml:"limits_config"`
				Compactor struct {
					RetentionEnabled   bool   `yaml:"retention_enabled"`
					DeleteRequestStore string `yaml:"delete_request_store"`
				} `yaml:"compactor"`
			}
			require.NoError(t, yaml.Unmarshal(cfg, &lokiCfg))
			require.True(t, lokiCfg.Compactor.RetentionEnabled, "loki compactor retention must be enabled")
			require.Equal(t, "filesystem", lokiCfg.Compactor.DeleteRequestStore,
				"Loki 2.9 requires delete_request_store with the filesystem object store")
			require.NotEmpty(t, lokiCfg.LimitsConfig.RetentionPeriod, "loki retention_period must be set")

			prom := compose.Services["prometheus"]
			cmdStr := fmt.Sprintf("%v", prom.Command)
			require.Contains(t, cmdStr, "--storage.tsdb.retention.time=", "prometheus must set an explicit TSDB retention")
			require.Contains(t, prom.Command, "--storage.tsdb.path=/prometheus",
				"overriding the image command must preserve the existing TSDB path")
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
					PullPolicy  string      `json:"pull_policy"`
					MemLimit    json.Number `json:"mem_limit"`
					Environment map[string]string
					Ports       []struct {
						HostIP    string `json:"host_ip"`
						Target    int
						Published string
					}
				}
			}
			require.NoError(t, json.Unmarshal(output, &compose))
			var totalMemory int64
			for name, svc := range compose.Services {
				// Compose versions emit byte counts as JSON numbers or strings.
				limit, err := svc.MemLimit.Int64()
				require.NoError(t, err, "%s memory cap after merging", name)
				require.Positive(t, limit, "%s must retain its memory cap after merging", name)
				totalMemory += limit
			}
			require.LessOrEqual(t, totalMemory, int64(3072*1024*1024),
				"the merged stack, including optional MinIO, must leave 1 GiB on the 4 GiB host")
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

// The test server cannot reach Docker Hub. Every literal third-party image in
// the deployed Compose files must therefore be listed in deploy/mirror-images.txt
// and overridden by docker-compose.registry.yml with that entry's ACR mirror.
// Keep this test honest when adding a service or bumping an image tag.
func TestDependencyMirrorsMatchCompose(t *testing.T) {
	mirrors := readMirrorList(t)
	overlay := readServiceImages(t, "../docker-compose.registry.yml")

	literal := map[string]string{}
	for _, file := range []string{"../docker-compose.yml", "../docker-compose.test.yml", "../docker-compose.caddy.yml"} {
		for service, image := range readServiceImages(t, file) {
			if strings.HasPrefix(image, "$") {
				continue
			}
			require.NotContains(t, literal, service, "%s declares a second literal image", service)
			literal[service] = image
		}
	}
	require.NotEmpty(t, literal)

	const overlayPrefix = "${DEPENDENCY_REGISTRY:?set DEPENDENCY_REGISTRY to <ACR_REGISTRY>/<ACR_NAMESPACE>}/"
	used := map[string]bool{}
	targets := map[string]bool{}
	for _, entry := range mirrors {
		targets[entry.target] = true
	}
	for service, image := range literal {
		entry, ok := mirrors[normalizeDockerHubReference(image)]
		require.True(t, ok, "%s image %q has no ACR mirror in deploy/mirror-images.txt", service, image)
		used[entry.source] = true
		require.Equal(t, overlayPrefix+entry.target, overlay[service],
			"docker-compose.registry.yml must mirror %s to ACR", service)
	}
	for source := range mirrors {
		require.True(t, used[source], "deploy/mirror-images.txt mirrors %s but no Compose file uses it", source)
	}
	for service, image := range overlay {
		if _, ok := literal[service]; ok {
			continue
		}
		// `extends` is resolved per file, so one-off services that reuse another
		// service's image need their own override pointing at the same mirror.
		target, ok := strings.CutPrefix(image, overlayPrefix)
		require.True(t, ok, "docker-compose.registry.yml must use the ACR prefix for %s", service)
		require.True(t, targets[target],
			"docker-compose.registry.yml mirrors %s to %q, which deploy/mirror-images.txt does not list", service, target)
	}
}

type mirrorEntry struct{ source, target string }

func readMirrorList(t *testing.T) map[string]mirrorEntry {
	t.Helper()
	raw, err := os.ReadFile("mirror-images.txt")
	require.NoError(t, err)
	mirrors := map[string]mirrorEntry{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		require.Len(t, fields, 2, "unexpected mirror entry: %q", line)
		source, target := fields[0], fields[1]
		require.Regexp(t, `^[a-z0-9][a-z0-9._/-]*:[A-Za-z0-9._-]+$`, source)
		require.Regexp(t, `^[a-z0-9]+(-[a-z0-9]+)*:[A-Za-z0-9._-]+$`, target)
		require.NotContains(t, mirrors, source, "duplicate mirror entry: %s", source)
		mirrors[source] = mirrorEntry{source: source, target: target}
	}
	require.NotEmpty(t, mirrors)
	return mirrors
}

func readServiceImages(t *testing.T, file string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(file)
	require.NoError(t, err)
	var compose struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &compose))
	images := map[string]string{}
	for service, svc := range compose.Services {
		if svc.Image != "" {
			images[service] = svc.Image
		}
	}
	return images
}

// Docker treats "postgres:16-alpine" and "docker.io/library/postgres:16-alpine"
// as the same image; normalise Compose shorthand so the mirror list can use the
// explicit form without depending on Compose interpolation rules.
func normalizeDockerHubReference(image string) string {
	if strings.HasPrefix(image, "docker.io/") {
		return image
	}
	first, _, found := strings.Cut(image, "/")
	if !found {
		return "docker.io/library/" + image
	}
	if !strings.ContainsAny(first, ".:") && first != "localhost" {
		return "docker.io/" + image
	}
	return image
}

// Run the real Compose merger with the registry overlay and confirm the merged
// stack pulls every dependency from ACR, while the CI-built digests survive.
func TestRegistryOverlayResolvesToACR(t *testing.T) {
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

	const registry = "registry.example.com/example"
	mall := "registry.example.com/example/aiyoung@sha256:" + strings.Repeat("a", 64)
	minio := "registry.example.com/example/aiyoung@sha256:" + strings.Repeat("b", 64)
	cmd := exec.Command("docker", "compose", "--project-name", "ecommerce-test",
		"--env-file", envFile,
		"-f", "../docker-compose.yml", "-f", "../docker-compose.test.yml",
		"-f", "../docker-compose.caddy.yml", "-f", "../docker-compose.registry.yml",
		"config", "--format", "json")
	cmd.Env = append(os.Environ(),
		"DEPENDENCY_REGISTRY="+registry,
		"MALL_IMAGE="+mall, "MINIO_IMAGE="+minio,
		"POSTGRES_PASSWORD=test-only", "REDIS_PASSWORD=test-only",
		"GRAFANA_ADMIN_PASSWORD=test-only",
		"AUTH_ACCESS_TOKEN_SECRET="+strings.Repeat("a", 32),
		"AUTH_REFRESH_TOKEN_SECRET="+strings.Repeat("b", 32),
		"AUTH_PHONE_SECRET="+strings.Repeat("c", 32),
		"MINIO_ROOT_USER=test-admin", "MINIO_ROOT_PASSWORD=test-admin-only",
		"STORAGE_ACCESS_KEY=test-app", "STORAGE_SECRET_KEY=test-only")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)

	var compose struct {
		Services map[string]struct{ Image string }
	}
	require.NoError(t, json.Unmarshal(output, &compose))
	require.NotEmpty(t, compose.Services)
	for service, svc := range compose.Services {
		switch service {
		case "mall":
			require.Equal(t, mall, svc.Image)
		case "minio":
			require.Equal(t, minio, svc.Image)
		default:
			require.True(t, strings.HasPrefix(svc.Image, registry+"/mirror-"),
				"%s must resolve to an ACR mirror, got %q", service, svc.Image)
		}
	}
	require.Contains(t, compose.Services["caddy"].Image, "/mirror-caddy:")
}
