package deploy_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestACRImageMetadata(t *testing.T) {
	sha := "01234567" + strings.Repeat("a", 24) + "deadbeef"
	for _, tc := range []struct {
		name, acrNamespace, namespace, want string
	}{
		{"namespace-variable", "", "team", "team"},
		{"acr-precedence", "other-team", "team", "other-team"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "output")
			cmd := exec.Command("bash", "../.github/scripts/image-metadata.sh")
			cmd.Env = append(os.Environ(), "GITHUB_OUTPUT="+output, "GITHUB_SHA="+sha,
				"ACR_REGISTRY=registry.example.com", "ACR_NAMESPACE="+tc.acrNamespace, "NAMESPACE="+tc.namespace)
			logs, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", logs)
			data, err := os.ReadFile(output)
			require.NoError(t, err)
			repo := "registry.example.com/" + tc.want + "/aiyoung"
			require.Equal(t, "name="+repo+"\nregistry=registry.example.com\ndependency_registry=registry.example.com/"+tc.want+
				"\nmall_tag="+repo+":mall-deadbeef\nminio_tag="+repo+":minio-deadbeef\n", string(data))
		})
	}
	for _, tc := range []struct{ registry, namespace, sha string }{
		{"", "team", sha},
		{"https://registry.example.com", "team", sha},
		{"registry.example.com/path", "team", sha},
		{"registry.example.com\nname=evil", "team", sha},
		{"registry.example.com", "../other", sha},
		// No ACR_NAMESPACE/NAMESPACE: no vendor default may be assumed.
		{"registry.example.com", "", sha},
		{"registry.example.com", "team", "deadbeef"},
	} {
		output := filepath.Join(t.TempDir(), "output")
		cmd := exec.Command("bash", "../.github/scripts/image-metadata.sh")
		cmd.Env = append(os.Environ(), "GITHUB_OUTPUT="+output, "GITHUB_SHA="+tc.sha,
			"ACR_REGISTRY="+tc.registry, "ACR_NAMESPACE="+tc.namespace, "NAMESPACE=")
		logs, err := cmd.CombinedOutput()
		require.Error(t, err, "%s", logs)
		require.NoFileExists(t, output)
	}
}

func TestSSHDeploymentPassesBothImages(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(bin, 0700))
	logFile := filepath.Join(root, "ssh.log")
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ssh"), []byte(`#!/usr/bin/env bash
printf '%s\n' "$*" >> "$MOCK_SSH_LOG"
case "$*" in
  *'tar -xzf'*) dd of=/dev/null 2>/dev/null ;;
esac
`), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf archive-fixture\n"), 0700))
	mall := "registry.example.com/example/aiyoung@sha256:" + strings.Repeat("a", 64)
	minio := "registry.example.com/example/aiyoung@sha256:" + strings.Repeat("b", 64)
	for _, image := range []string{minio, "", "registry.example.com/example/aiyoung:minio-deadbeef", minio + "'; touch injected"} {
		_ = os.Remove(logFile)
		cmd := exec.Command("bash", "../.github/scripts/deploy-test.sh")
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"MOCK_SSH_LOG="+logFile, "MALL_IMAGE="+mall, "MINIO_IMAGE="+image,
			"DEPENDENCY_REGISTRY=registry.example.com/example",
			"TEST_SSH_HOST=server.example.com", "TEST_SSH_USER=deploy", "TEST_SSH_PORT=22",
			"TEST_SSH_PRIVATE_KEY=fake-key", "TEST_SSH_KNOWN_HOSTS=fake-host-key",
			"TEST_DEPLOY_PATH=/opt/ecommerce-test", "RELEASE_ID=release-test")
		out, err := cmd.CombinedOutput()
		if image != minio {
			require.Error(t, err, "%s", out)
			require.NoFileExists(t, logFile, "invalid references must fail before SSH")
			continue
		}
		require.NoError(t, err, "%s", out)
		data, err := os.ReadFile(logFile)
		require.NoError(t, err)
		require.Contains(t, string(data), "'"+mall+"' '"+minio+"' 'registry.example.com/example'")
		require.Contains(t, string(data), "StrictHostKeyChecking=yes")
		require.NotContains(t, string(data), "fake-key")
	}
}

func TestSSHDeploymentRejectsInvalidDependencyRegistry(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(bin, 0700))
	logFile := filepath.Join(root, "ssh.log")
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ssh"), []byte(`#!/usr/bin/env bash
printf '%s\n' "$*" >> "$MOCK_SSH_LOG"
`), 0700))
	image := "registry.example.com/example/aiyoung@sha256:" + strings.Repeat("a", 64)
	for _, dependencyRegistry := range []string{
		"",
		"registry.example.com",
		"registry.example.com/../other",
		"https://registry.example.com/example",
		"registry.example.com/example'; touch injected",
	} {
		_ = os.Remove(logFile)
		cmd := exec.Command("bash", "../.github/scripts/deploy-test.sh")
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"MOCK_SSH_LOG="+logFile, "MALL_IMAGE="+image, "MINIO_IMAGE="+image,
			"DEPENDENCY_REGISTRY="+dependencyRegistry,
			"TEST_SSH_HOST=server.example.com", "TEST_SSH_USER=deploy", "TEST_SSH_PORT=22",
			"TEST_SSH_PRIVATE_KEY=fake-key", "TEST_SSH_KNOWN_HOSTS=fake-host-key",
			"TEST_DEPLOY_PATH=/opt/ecommerce-test", "RELEASE_ID=release-test")
		out, err := cmd.CombinedOutput()
		require.Error(t, err, "%s", out)
		require.NoFileExists(t, logFile, "invalid dependency registry must fail before SSH")
	}
}
