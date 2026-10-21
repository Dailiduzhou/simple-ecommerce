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
		storage   bool
		inspectRC string
		upRC      string
		probeRC   string
		wantOK    bool
	}{
		{name: "preloaded-image", storage: true, wantOK: true},
		{name: "existing-service", wantOK: true},
		{name: "missing-local-minio-image", storage: true, inspectRC: "1"},
		{name: "unhealthy-application", upRC: "1"},
		{name: "unreachable-minio", probeRC: "1"},
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
printf '%s\n' "$*" >> "$MOCK_LOG"
case "$*" in
  'image inspect '*) exit "${MOCK_INSPECT_RC:-0}" ;;
  *' config --services')
    printf '%s\n' postgres redis mall
    if [ "$MOCK_STORAGE" = true ]; then echo minio; fi ;;
  *' config --images minio') echo ecommerce-minio:preloaded ;;
  *' up -d --wait --wait-timeout 180') exit "${MOCK_UP_RC:-0}" ;;
  *' exec -T mall sh -ec '*) exit "${MOCK_PROBE_RC:-0}" ;;
esac
`), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(bin, "flock"), []byte("#!/bin/sh\nexit 0\n"), 0700))
			image := "ghcr.io/example/repo/mall@sha256:" + strings.Repeat("a", 64)
			logFile := filepath.Join(root, "docker.log")
			cmd := exec.Command("bash", scriptPath, root, image)
			storage := "false"
			if tc.storage {
				storage = "true"
			}
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"MOCK_LOG="+logFile, "MOCK_STORAGE="+storage,
				"MOCK_INSPECT_RC="+zeroDefault(tc.inspectRC),
				"MOCK_UP_RC="+zeroDefault(tc.upRC), "MOCK_PROBE_RC="+zeroDefault(tc.probeRC))
			output, err := cmd.CombinedOutput()
			if tc.wantOK {
				require.NoError(t, err, "%s", output)
				stored, err := os.ReadFile(filepath.Join(release, "image"))
				require.NoError(t, err)
				require.Equal(t, image+"\n", string(stored))
			} else {
				require.Error(t, err, "%s", output)
				require.NoFileExists(t, filepath.Join(release, "image"))
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
			require.NotContains(t, string(log), " pull minio")
			require.NotContains(t, string(log), " build")
			require.NotContains(t, string(log), "down")
			if tc.inspectRC != "" {
				require.NotContains(t, string(log), " pull mall", "fail before changing services if the local image is absent")
			} else {
				require.Contains(t, string(log), " pull mall")
			}
			if !tc.storage {
				require.NotContains(t, string(log), "image inspect", "existing MinIO is not managed by this deployment")
			}
		})
	}
}

func zeroDefault(value string) string {
	if value == "" {
		return "0"
	}
	return value
}
