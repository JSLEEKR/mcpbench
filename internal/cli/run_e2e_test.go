package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// buildMock compiles the transport testmock binary (a tiny JSON-RPC stdio
// server used by transport_test.go) for end-to-end run tests.
var (
	e2eOnce sync.Once
	e2eBin  string
	e2eErr  error
)

func e2eMockBinary(t *testing.T) string {
	t.Helper()
	e2eOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mcpbench-cli-e2e-")
		if err != nil {
			e2eErr = err
			return
		}
		out := filepath.Join(dir, "testmock")
		// Build the mock from its real location.
		cmd := exec.Command("go", "build", "-o", out, "./internal/transport/testmock")
		cmd.Dir = repoRoot(t)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			e2eErr = err
			return
		}
		e2eBin = out
	})
	if e2eErr != nil {
		t.Fatalf("build mock: %v", e2eErr)
	}
	return e2eBin
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// Tests run from internal/cli — go up two levels.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func TestRunEndToEndStdioJSONOutput(t *testing.T) {
	bin := e2eMockBinary(t)
	dir := t.TempDir()
	scenarioPath := filepath.Join(dir, "s.yml")
	outPath := filepath.Join(dir, "out.json")
	_ = os.WriteFile(scenarioPath, []byte(fmt.Sprintf(`
name: e2e
transport:
  type: stdio
  cmd: %q
workload:
  requests: 20
  concurrency: 2
  timeout: 2s
tools:
  - name: ping
    weight: 1
`, bin)), 0o600)

	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"run", "--scenario", scenarioPath, "--output", "json", "--output-file", outPath, "--silent-server", "--seed", "1"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var rf map[string]any
	if err := json.Unmarshal(data, &rf); err != nil {
		t.Fatalf("bad json: %v\n%s", err, data)
	}
	if rf["scenario"] != "e2e" {
		t.Fatalf("scenario = %v", rf["scenario"])
	}
	summary := rf["summary"].(map[string]any)
	overall := summary["overall"].(map[string]any)
	latency := overall["latency"].(map[string]any)
	if int(latency["count"].(float64)) != 20 {
		t.Fatalf("count = %v", latency["count"])
	}
}

func TestRunEndToEndCSV(t *testing.T) {
	bin := e2eMockBinary(t)
	dir := t.TempDir()
	scenarioPath := filepath.Join(dir, "s.yml")
	_ = os.WriteFile(scenarioPath, []byte(fmt.Sprintf(`
name: csv-e2e
transport: {type: stdio, cmd: %q}
workload: {requests: 5, concurrency: 1, timeout: 2s}
tools: [{name: ping}]
`, bin)), 0o600)
	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"run", "--scenario", scenarioPath, "--output", "csv", "--silent-server", "--seed", "1"}, &stdout, &stderr); err != nil {
		t.Fatal(err, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "tool,count") {
		t.Fatalf("bad csv: %s", stdout.String())
	}
}

func TestRunEndToEndHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{}}`, body["id"])
	}))
	defer srv.Close()
	dir := t.TempDir()
	scenarioPath := filepath.Join(dir, "s.yml")
	_ = os.WriteFile(scenarioPath, []byte(fmt.Sprintf(`
name: http-e2e
transport: {type: http, url: %q}
workload: {requests: 8, concurrency: 2, timeout: 2s}
tools: [{name: ping}]
`, srv.URL)), 0o600)
	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"run", "--scenario", scenarioPath, "--output", "json", "--seed", "1"}, &stdout, &stderr); err != nil {
		t.Fatal(err, stderr.String())
	}
	var rf map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &rf); err != nil {
		t.Fatal(err)
	}
}

func TestRunEndToEndOverrideSpawnFlag(t *testing.T) {
	bin := e2eMockBinary(t)
	dir := t.TempDir()
	scenarioPath := filepath.Join(dir, "s.yml")
	_ = os.WriteFile(scenarioPath, []byte(`
name: spawn-e2e
transport: {type: stdio, cmd: /nope/placeholder}
workload: {requests: 3, concurrency: 1, timeout: 2s}
tools: [{name: ping}]
`), 0o600)
	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"run", "--scenario", scenarioPath, "--spawn", bin, "--output", "json", "--silent-server", "--seed", "1"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\n%s", err, stderr.String())
	}
}

func TestRunEndToEndPromOutput(t *testing.T) {
	bin := e2eMockBinary(t)
	dir := t.TempDir()
	scenarioPath := filepath.Join(dir, "s.yml")
	_ = os.WriteFile(scenarioPath, []byte(fmt.Sprintf(`
name: prom-e2e
transport: {type: stdio, cmd: %q}
workload: {requests: 4, concurrency: 1, timeout: 2s}
tools: [{name: ping}]
`, bin)), 0o600)
	var stdout, stderr bytes.Buffer
	if err := Execute([]string{"run", "--scenario", scenarioPath, "--output", "prom", "--silent-server", "--seed", "1"}, &stdout, &stderr); err != nil {
		t.Fatal(err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "mcpbench_requests_total") {
		t.Fatalf("not prom: %s", stdout.String())
	}
}
