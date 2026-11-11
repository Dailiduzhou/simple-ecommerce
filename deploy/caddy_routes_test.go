package deploy_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Run the actual Caddy routes on an ephemeral loopback HTTP listener. Disable
// automatic HTTPS only in the generated test JSON: never request certificates
// for fixtures, listen on public ports, or contact real backend/storage services.
func TestCaddyRoutes(t *testing.T) {
	binary, err := exec.LookPath("caddy")
	if err != nil {
		t.Skip("Caddy binary is not installed; CI also validates the deployed container config")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Host", r.Host)
		w.Header().Set("X-Test-URI", r.URL.RequestURI())
		w.Header().Set("X-Test-Forwarded", r.Header.Get("X-Forwarded-For"))
		if r.URL.Path == "/unauthorized" {
			w.WriteHeader(http.StatusUnauthorized)
		}
		_, _ = io.WriteString(w, "upstream")
	}))
	defer upstream.Close()
	root := t.TempDir()
	raw, err := os.ReadFile("Caddyfile")
	require.NoError(t, err)
	content := string(raw)
	for _, dial := range []string{"mall:8000", "minio:9000", "minio:9001"} {
		content = strings.ReplaceAll(content, dial, strings.TrimPrefix(upstream.URL, "http://"))
	}
	file := filepath.Join(root, "Caddyfile")
	require.NoError(t, os.WriteFile(file, []byte(content), 0600))
	adapt := exec.Command(binary, "adapt", "--config", file, "--adapter", "caddyfile")
	adapt.Env = append(os.Environ(), "API_DOMAIN=api.example.com",
		"MINIO_DOMAIN=minio.example.com", "CONSOLE_DOMAIN=console.example.com", "CONSOLE_ALLOWED_CIDRS=192.0.2.1/32")
	out, err := adapt.Output()
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(out, &cfg))
	cfg["admin"] = map[string]any{"disabled": true, "config": map[string]any{"persist": false}}
	apps := cfg["apps"].(map[string]any)
	servers := apps["http"].(map[string]any)["servers"].(map[string]any)
	require.Len(t, servers, 1)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	for _, value := range servers {
		server := value.(map[string]any)
		server["listen"] = []string{address}
		server["automatic_https"] = map[string]any{"disable": true}
		delete(server, "tls_connection_policies")
	}
	config, err := json.Marshal(cfg)
	require.NoError(t, err)
	configPath := filepath.Join(root, "caddy.json")
	require.NoError(t, os.WriteFile(configPath, config, 0600))
	log, err := os.Create(filepath.Join(root, "caddy.log"))
	require.NoError(t, err)
	defer log.Close()
	cmd := exec.Command(binary, "run", "--config", configPath)
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+root, "XDG_CONFIG_HOME="+root)
	cmd.Stdout, cmd.Stderr = log, log
	require.NoError(t, cmd.Start())
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			data, _ := os.ReadFile(log.Name())
			t.Log(string(data))
		}
	}()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	require.Eventually(t, func() bool {
		resp, err := client.Get("http://" + address)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	}, 5*time.Second, 50*time.Millisecond)
	for _, tc := range []struct {
		name, host, method, path, origin string
		status                           int
		body                             string
	}{
		// CORS belongs to the backend (HTTP_CORS_ALLOWED_ORIGINS). Caddy must not
		// answer preflights itself nor add a second Access-Control-Allow-Origin.
		{"api-preflight-reaches-backend", "api.example.com", "OPTIONS", "/v1/users", "https://web.example.com", 200, "upstream"},
		{"api-passes-origin-through", "api.example.com", "GET", "/unauthorized", "https://web.example.com", 401, "upstream"},
		{"signed-object", "minio.example.com", "GET", "/bucket/images/a%20b.jpg?X-Amz-Signature=test&x=1", "", 200, "upstream"},
		{"console-denied-despite-spoofed-ip", "console.example.com", "GET", "/", "", 403, "Forbidden"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, "http://"+address+tc.path, nil)
			require.NoError(t, err)
			req.Host = tc.host
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("X-Forwarded-For", "192.0.2.1")
			if tc.method == "OPTIONS" {
				req.Header.Set("Access-Control-Request-Method", "POST")
				req.Header.Set("Access-Control-Request-Headers", "Authorization,Content-Type")
			}
			resp, err := client.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, tc.status, resp.StatusCode)
			require.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"), "Caddy must not set CORS headers")
			require.Equal(t, tc.body, string(body))
			if tc.body == "upstream" {
				require.Equal(t, tc.host, resp.Header.Get("X-Test-Host"))
				require.Equal(t, tc.path, resp.Header.Get("X-Test-URI"))
				require.Equal(t, "127.0.0.1", resp.Header.Get("X-Test-Forwarded"))
			}
		})
	}
}
