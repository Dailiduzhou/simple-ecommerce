package deploy_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServerDeployment(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pullRC    string
		upRC      string
		probeRC   string
		gatewayRC string
		configRC  string
		wantOK    bool
	}{
		{name: "acr-images", wantOK: true},
		{name: "registry-pull-failed", pullRC: "1"},
		{name: "unhealthy-application", upRC: "1"},
		{name: "unreachable-minio", probeRC: "1"},
		{name: "invalid-caddy-or-missing-frontend", configRC: "1"},
		{name: "unhealthy-gateway", gatewayRC: "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			release := filepath.Join(root, "releases", "new")
			require.NoError(t, os.MkdirAll(filepath.Join(release, "deploy"), 0700))
			script, err := os.ReadFile("deploy-test.sh")
			require.NoError(t, err)
			scriptPath := filepath.Join(release, "deploy", "deploy-test.sh")
			require.NoError(t, os.WriteFile(scriptPath, script, 0600))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".env"), nil, 0600))
			previous := filepath.Join(root, "releases", "previous")
			require.NoError(t, os.Symlink(previous, filepath.Join(root, "current")))
			bin := filepath.Join(root, "bin")
			require.NoError(t, os.Mkdir(bin, 0700))
			// All Docker operations are mocked; tests must never contact a daemon.
			require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(`#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" "images=$MALL_IMAGE $MINIO_IMAGE" >> "$MOCK_LOG"
case "$*" in
  *' pull mall minio caddy') exit "${MOCK_PULL_RC:-0}" ;;
  *' up -d --wait --wait-timeout 180') exit "${MOCK_UP_RC:-0}" ;;
  *' exec -T mall sh -ec '*) exit "${MOCK_PROBE_RC:-0}" ;;
  *' run --rm --no-deps caddy-check sh -ec '*) exit "${MOCK_CONFIG_RC:-0}" ;;
  *' exec -T caddy sh -ec '*) exit "${MOCK_GATEWAY_RC:-0}" ;;
esac
`), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(bin, "flock"), []byte("#!/bin/sh\nexit 0\n"), 0700))
			image := "registry.example.com/example/aiyoung@sha256:" + strings.Repeat("a", 64)
			minioImage := "registry.example.com/example/aiyoung@sha256:" + strings.Repeat("b", 64)
			logFile := filepath.Join(root, "docker.log")
			cmd := exec.Command("bash", scriptPath, root, image, minioImage)
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"MOCK_LOG="+logFile, "MINIO_IMAGE=stale-local-minio:old",
				"MOCK_PULL_RC="+zeroDefault(tc.pullRC),
				"MOCK_UP_RC="+zeroDefault(tc.upRC), "MOCK_PROBE_RC="+zeroDefault(tc.probeRC),
				"MOCK_CONFIG_RC="+zeroDefault(tc.configRC), "MOCK_GATEWAY_RC="+zeroDefault(tc.gatewayRC))
			output, err := cmd.CombinedOutput()
			if tc.wantOK {
				require.NoError(t, err, "%s", output)
				for name, want := range map[string]string{"image": image, "minio-image": minioImage} {
					stored, err := os.ReadFile(filepath.Join(release, name))
					require.NoError(t, err)
					require.Equal(t, want+"\n", string(stored))
				}
			} else {
				require.Error(t, err, "%s", output)
				require.NoFileExists(t, filepath.Join(release, "image"))
				require.NoFileExists(t, filepath.Join(release, "minio-image"))
			}
			current, err := os.Readlink(filepath.Join(root, "current"))
			require.NoError(t, err)
			if tc.wantOK {
				require.Equal(t, release, current)
			} else {
				require.Equal(t, previous, current, "failure must preserve last-successful metadata")
			}
			log, err := os.ReadFile(logFile)
			require.NoError(t, err)
			require.NotRegexp(t, `(?m)^compose [^\n]*\.yml build(?: |$)`, string(log))
			require.NotContains(t, string(log), "down")
			require.NotContains(t, string(log), "image inspect", "MinIO need not be preloaded")
			require.Contains(t, string(log), " pull mall minio caddy")
			require.Contains(t, string(log), "images="+image+" "+minioImage)
			require.Contains(t, string(log), "docker-compose.caddy.yml")
			if tc.configRC != "" || tc.pullRC != "" {
				require.NotContains(t, string(log), " up -d", "preflight failure must not replace services")
			}
			if tc.wantOK {
				require.Contains(t, string(log), " exec -T caddy sh -ec")
			}
			if tc.gatewayRC != "" {
				// The gateway is verified before the storage hop that depends on it, and
				// its logs are printed when it never becomes ready.
				require.NotContains(t, string(log), " exec -T mall sh -ec")
				require.Contains(t, string(log), " logs --tail=100 caddy mall")
			}
			if tc.probeRC != "" {
				require.Contains(t, string(log), " exec -T caddy sh -ec",
					"Caddy TLS must be checked before probing storage through it")
			}
		})
	}
}

func TestServerDeploymentRequiresBothDigests(t *testing.T) {
	image := "registry.example.com/example/aiyoung@sha256:" + strings.Repeat("a", 64)
	for _, args := range [][]string{
		{"/unused", image},
		{"/unused", image, "registry.example.com/example/aiyoung:minio-12345678"},
		{"/unused", "registry.example.com/example/aiyoung:mall-12345678", image},
		{"/unused", image, image + "'; echo injected"},
	} {
		cmd := exec.Command("bash", append([]string{"deploy-test.sh"}, args...)...)
		output, err := cmd.CombinedOutput()
		require.Error(t, err, "%s", output)
		require.Contains(t, string(output), "immutable")
	}
}

func zeroDefault(value string) string {
	if value == "" {
		return "0"
	}
	return value
}
