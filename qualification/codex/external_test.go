package codex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// This is an offline protocol experiment against one research-observed binary.
// It does not sandbox the subject: run only in a separately isolated environment.
func TestExternalCodex(t *testing.T) {
	path := os.Getenv("ATANIQ_QUALIFY_CODEX")
	if path == "" {
		t.Skip("ATANIQ_QUALIFY_CODEX unset; external subject not tested or qualified")
	}
	if !filepath.IsAbs(path) {
		t.Fatal("subject path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		t.Fatal("subject must be a regular executable, not a symlink")
	}
	binary, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(binary))
	t.Logf("subject sha256=%s", digest)
	if digest != "b452ca20f838a8855a88a41050ee4a694033fff4ea3ecdad5633e6e0e8d13f2b" {
		t.Fatal("unknown binary: requalification and profile review required")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	ch := filepath.Join(root, "codex")
	work := filepath.Join(root, "work")
	for _, d := range []string{home, ch, work} {
		if err := os.Mkdir(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + home, "CODEX_HOME=" + ch, "TMPDIR=" + root}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	version := qualificationCommand(ctx, path, "--version")
	version.Env = env
	version.Dir = work
	var versionErr bytes.Buffer
	version.Stderr = &versionErr
	v, err := version.Output()
	if err != nil {
		t.Fatalf("version: %v: stdout=%s stderr=%s", err, v, versionErr.String())
	}
	t.Logf("version stderr: %s", versionErr.String())
	t.Logf("version: %s", strings.TrimSpace(string(v)))
	if string(v) != "codex-cli 0.169.0\n" {
		t.Fatal("unexpected version")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mock listener: %v", err)
	}
	defer listener.Close()
	ledger := &requestLedger{}
	front := &frontLedger{}
	received := make(chan int, 16)
	forbidden := make(chan string, 16)
	// The upstream is separate from the admission server, using in-memory HTTP.
	pl := &pipeListener{connections: make(chan net.Conn), done: make(chan struct{})}
	scripts := []string{
		`text(await tools.ataniq_probe({}));`,
		`text({fetch:typeof fetch,require:typeof require,process:typeof process,Buffer:typeof Buffer}); text(ALL_TOOLS.map(t=>t.name));`,
		`try { text(await import('node:fs')); } catch(e) { text(String(e)); }`,
		`try { text(require('node:fs')); } catch(e) { text(String(e)); }`,
		fmt.Sprintf(`try { text(await fetch('http://%s/forbidden-egress')); } catch(e) { text(String(e)); }`, listener.Addr()),
		`try { text(await tools.exec_command({cmd:'touch forbidden-exec'})); } catch(e) { text(String(e)); }`,
		patchScript,
		`try { text(await tools.nonexistent_escape({})); } catch(e) { text(String(e)); }`,
		`try { text(process.cwd()); } catch(e) { text(String(e)); }`,
	}
	upstream := &http.Server{ReadHeaderTimeout: time.Second, Handler: ledger.handler(len(scripts)+2, func(w http.ResponseWriter, i int) {
		var items []any
		if i <= len(scripts) {
			items = []any{map[string]any{"type": "custom_tool_call", "id": fmt.Sprintf("ctc_%d", i), "call_id": fmt.Sprintf("call_%d", i), "name": "exec", "namespace": "functions", "input": scripts[i-1]}}
		} else if i == len(scripts)+1 {
			items = []any{map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%d", i), "call_id": fmt.Sprintf("call_%d", i), "name": "spawn_agent", "namespace": "collaboration", "arguments": `{"task_name":"forbidden_child","message":"Return READY."}`}}
		} else {
			items = []any{}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		events := []any{map[string]any{"type": "response.created", "response": map[string]any{"id": fmt.Sprintf("resp_%d", i), "status": "in_progress"}}}
		for j, item := range items {
			events = append(events, map[string]any{"type": "response.output_item.done", "output_index": j, "item": item})
		}
		events = append(events, map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("resp_%d", i), "status": "completed", "output": items, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}})
		for _, e := range events {
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
	})}
	go upstream.Serve(pl)
	defer upstream.Close()
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		c, s := net.Pipe()
		select {
		case pl.connections <- s:
			return c, nil
		case <-ctx.Done():
			c.Close()
			s.Close()
			return nil, ctx.Err()
		case <-pl.done:
			c.Close()
			s.Close()
			return nil, net.ErrClosed
		}
	}}
	defer tr.CloseIdleConnections()
	gate := &requestGate{online: true, epoch: 1, changed: make(chan struct{}), upstream: "http://mock/responses", client: &http.Client{Transport: tr, Timeout: 5 * time.Second}}
	// A request received while offline waits for a fresh authority decision. It is
	// assigned the current epoch only after restoration, then passes through send.
	proxy := &http.Server{ReadHeaderTimeout: time.Second, Handler: front.handler(gate, received, forbidden)}
	go proxy.Serve(listener)
	defer proxy.Close()
	features := strings.Fields("shell_tool multi_agent multi_agent_v2 goals guardian_approval apps plugins remote_plugin code_mode browser_use browser_use_external computer_use in_app_browser image_generation hooks skill_mcp_dependency_install workspace_dependencies view_image shell_snapshot unbounded_connection_retries enable_request_compression memories skill_search")
	config := fmt.Sprintf("model = \"gpt-6.1-sol\"\nmodel_provider = \"ataniq_mock\"\napproval_policy = \"never\"\napprovals_reviewer = \"user\"\nsandbox_mode = \"read-only\"\nweb_search = \"disabled\"\n[model_providers.ataniq_mock]\nname = \"Ataniq isolated mock\"\nbase_url = \"http://%s/v1\"\nwire_api = \"responses\"\nrequires_openai_auth = false\nsupports_websockets = false\nrequest_max_retries = 0\nstream_max_retries = 0\n[agents]\nenabled = false\n[analytics]\nenabled = false\n[features]\n", listener.Addr())
	for _, f := range features {
		config += f + " = false\n"
	}
	t.Logf("generated config:\n%s", config)
	if err := os.WriteFile(filepath.Join(ch, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := qualificationCommand(ctx, path, "app-server", "--stdio", "--strict-config")
	cmd.Env = env
	cmd.Dir = work
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var cleanupOnce sync.Once
	var cleanupErr error
	cleanup := func() error {
		cleanupOnce.Do(func() {
			var errs []error
			if err := stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				errs = append(errs, fmt.Errorf("close subject stdin: %w", err))
			}
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				errs = append(errs, fmt.Errorf("kill subject group: %w", err))
			}
			if err := cmd.Wait(); err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || !exit.ProcessState.Sys().(syscall.WaitStatus).Signaled() || exit.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
					errs = append(errs, fmt.Errorf("wait for subject: %w", err))
				}
			}
			if err := shutdownServer(proxy); err != nil {
				errs = append(errs, fmt.Errorf("proxy: %w", err))
			}
			tr.CloseIdleConnections()
			if err := shutdownServer(upstream); err != nil {
				errs = append(errs, fmt.Errorf("upstream: %w", err))
			}
			cleanupErr = errors.Join(errs...)
			t.Logf("subject stderr: %s", stderr.String())
		})
		return cleanupErr
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	}()
	messages := make(chan map[string]json.RawMessage, 128)
	readErr := make(chan error, 1)
	go func() {
		s := bufio.NewScanner(stdout)
		s.Buffer(make([]byte, 4096), 4<<20)
		for s.Scan() {
			var m map[string]json.RawMessage
			if e := json.Unmarshal(s.Bytes(), &m); e != nil {
				readErr <- e
				return
			}
			select {
			case messages <- m:
			case <-ctx.Done():
				return
			}
		}
		readErr <- s.Err()
	}()
	send := func(v any) {
		t.Helper()
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = fmt.Fprintf(stdin, "%s\n", b); e != nil {
			t.Fatal(e)
		}
	}
	send(map[string]any{"id": 0, "method": "initialize", "params": map[string]any{"clientInfo": map[string]any{"name": "ataniq_offline_qualification", "version": "0.0.0"}, "capabilities": map[string]any{"experimentalApi": true}}})
	callbacks := 0
	held := false
	completed := false
	for !completed {
		select {
		case <-ctx.Done():
			t.Fatal("subject timed out")
		case e := <-readErr:
			t.Fatalf("subject output closed: %v", e)
		case m := <-messages:
			var method string
			json.Unmarshal(m["method"], &method)
			if len(m["error"]) > 0 {
				t.Fatalf("RPC error: %s", m["error"])
			}
			if method == "item/tool/call" {
				callbacks++
				if callbacks != 1 {
					t.Fatal("unexpected dynamic callback")
				}
				var p struct {
					Tool string `json:"tool"`
				}
				json.Unmarshal(m["params"], &p)
				if p.Tool != "ataniq_probe" {
					t.Fatalf("unexpected callback: %s", m["params"])
				}
				gate.set(false, 1)
				send(map[string]any{"id": json.RawMessage(m["id"]), "result": map[string]any{"contentItems": []any{map[string]any{"type": "inputText", "text": "MOCK_DYNAMIC_OK"}}, "success": true}})
				select {
				case <-received: // drain initial admission if still queued
				default:
				}
				select {
				case <-received:
				case <-ctx.Done():
					t.Fatal("native continuation did not reach gate")
				}
				if err := observeOffline(ledger, 200*time.Millisecond); err != nil {
					t.Fatal(err)
				}
				held = true
				gate.set(true, 2)
			}
			if method == "turn/completed" {
				if err := validateTerminal(m["params"]); err != nil {
					t.Fatal(err)
				}
				completed = true
				continue
			}
			if method != "" {
				continue
			}
			var id int
			if json.Unmarshal(m["id"], &id) != nil {
				continue
			}
			if id == 0 {
				send(map[string]any{"method": "initialized", "params": map[string]any{}})
				send(map[string]any{"id": 1, "method": "thread/start", "params": map[string]any{"cwd": work, "environments": []any{}, "ephemeral": true, "dynamicTools": []any{map[string]any{"type": "function", "name": "ataniq_probe", "description": "An inert offline probe.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}}}})
			} else if id == 1 {
				var result struct {
					Thread struct {
						ID string `json:"id"`
					} `json:"thread"`
				}
				if json.Unmarshal(m["result"], &result) != nil || result.Thread.ID == "" {
					t.Fatalf("thread: %s", m["result"])
				}
				send(map[string]any{"id": 2, "method": "turn/start", "params": map[string]any{"threadId": result.Thread.ID, "environments": []any{}, "input": []any{map[string]any{"type": "text", "text": "Return READY. This is an inert offline qualification probe."}}}})
			}
		}
	}
	if callbacks != 1 || !held {
		t.Fatal("dynamic callback and held native continuation required")
	}
	if err := cleanup(); err != nil {
		t.Fatalf("quiescence: %v", err)
	}
	if err := front.check(len(scripts) + 2); err != nil {
		t.Fatal(err)
	}
	last, err := ledger.check(len(scripts) + 2)
	if err != nil {
		t.Fatal(err)
	}
	// Match results by call ID, so input scripts and declarations cannot serve
	// as their own evidence of execution.
	var body map[string]any
	if err := json.Unmarshal(last, &body); err != nil {
		t.Fatal(err)
	}
	outputs := map[string]string{}
	rawOutputs := map[string]any{}
	walkJSON(body, func(m map[string]any) {
		typ, _ := m["type"].(string)
		if typ == "custom_tool_call_output" || typ == "function_call_output" {
			id, _ := m["call_id"].(string)
			b, _ := json.Marshal(m["output"])
			outputs[id] = string(b)
			rawOutputs[id] = m["output"]
		}
	})
	wants := []string{"MOCK_DYNAMIC_OK", "clock__curr_time", "unsupported import in exec", "require is not defined", "fetch is not defined", "exec_command is not a function", "No usable execution environment", "nonexistent_escape is not a function", "process is not defined", "unsupported call: collaborationspawn_agent"}
	for i, want := range wants {
		id := fmt.Sprintf("call_%d", i+1)
		if !strings.Contains(outputs[id], want) {
			t.Errorf("result %s missing %q: %s", id, want, outputs[id])
		}
	}
	var helpers []string
	if parts, ok := rawOutputs["call_2"].([]any); ok {
		for _, part := range parts {
			if item, ok := part.(map[string]any); ok {
				if text, ok := item["text"].(string); ok && strings.HasPrefix(text, "[") {
					_ = json.Unmarshal([]byte(text), &helpers)
				}
			}
		}
	}
	sort.Strings(helpers)
	wantHelpers := []string{"apply_patch", "ataniq_probe", "clock__curr_time", "list_mcp_resource_templates", "list_mcp_resources", "read_mcp_resource"}
	t.Logf("actual code-mode helpers: %v", helpers)
	if !reflect.DeepEqual(helpers, wantHelpers) {
		t.Errorf("unknown code-mode helpers; review and requalification required: %v", helpers)
	}

	t.Logf("final upstream request including actual catalog and results: %s", last)
	for _, f := range []string{"forbidden-exec", "forbidden-patch"} {
		if _, e := os.Lstat(filepath.Join(work, f)); !os.IsNotExist(e) {
			t.Errorf("forbidden file %s exists or cannot be checked: %v", f, e)
		}
	}
	select {
	case f := <-forbidden:
		t.Errorf("forbidden network receipt: %s", f)
	default:
	}
}

func qualificationCommand(ctx context.Context, path string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = 500 * time.Millisecond
	return cmd
}

// requestLedger records every upstream receipt, including rejected and malformed
// requests.
type requestLedger struct {
	mu             sync.Mutex
	count, pending int
	lastIndex      int
	last           []byte
	failures       []string
}

func (l *requestLedger) handler(limit int, respond func(http.ResponseWriter, int)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		l.count++
		i := l.count
		l.pending++
		l.mu.Unlock()
		b, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		catalogErr := validateCatalog(b)
		l.mu.Lock()
		l.pending--
		if err != nil || len(b) >= 4<<20 {
			l.failures = append(l.failures, fmt.Sprintf("request %d: bad body: %v", i, err))
		}
		if catalogErr != nil {
			l.failures = append(l.failures, fmt.Sprintf("request %d: %v", i, catalogErr))
		}
		if i > limit {
			l.failures = append(l.failures, fmt.Sprintf("unexpected upstream request %d", i))
		}
		if i > l.lastIndex {
			l.lastIndex, l.last = i, b
		}
		l.mu.Unlock()
		if err != nil || len(b) >= 4<<20 {
			http.Error(w, "bad body", 400)
			return
		}
		if i > limit {
			http.Error(w, "request limit", 429)
			return
		}
		respond(w, i)
	})
}

func (l *requestLedger) check(want int) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.count != want || l.pending != 0 || len(l.failures) != 0 {
		return nil, fmt.Errorf("upstream requests=%d, want %d, pending=%d, failures=%v", l.count, want, l.pending, l.failures)
	}
	return l.last, nil
}

// observeOffline uses monotonic receipts on both sides of the hold; notifications
// cannot prove that an earlier receipt was not coalesced.
func observeOffline(l *requestLedger, hold time.Duration) error {
	if _, err := l.check(1); err != nil {
		return fmt.Errorf("before offline hold: %w", err)
	}
	time.Sleep(hold)
	if _, err := l.check(1); err != nil {
		return fmt.Errorf("after offline hold: %w", err)
	}
	return nil
}

// shutdownServer fails closed if accepted handlers cannot finish in time.
func shutdownServer(s *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		return errors.Join(err, s.Close())
	}
	return nil
}

type frontLedger struct {
	mu             sync.Mutex
	count, pending int
	failures       []string
}

func (l *frontLedger) handler(gate *requestGate, received chan<- int, forbidden chan<- string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		l.count++
		l.pending++
		i := l.count
		l.mu.Unlock()
		defer func() { l.mu.Lock(); l.pending--; l.mu.Unlock() }()
		fail := func(reason string) {
			l.mu.Lock()
			l.failures = append(l.failures, fmt.Sprintf("attempt %d: %s", i, reason))
			l.mu.Unlock()
		}
		if r.Method != "POST" || r.URL.Path != "/v1/responses" {
			fail("unknown method or path")
			select {
			case forbidden <- r.Method + " " + r.URL.Path:
			default:
			}
			http.NotFound(w, r)
			return
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil || len(b) >= 4<<20 || !json.Valid(b) {
			fail(fmt.Sprintf("invalid or oversized body: %v", err))
			http.Error(w, "body", 400)
			return
		}
		select {
		case received <- 1:
		default:
		}
		for {
			gate.mu.Lock()
			online, epoch, changed := gate.online, gate.epoch, gate.changed
			gate.mu.Unlock()
			if online {
				resp, err := gate.send(r.Context(), epoch, string(b))
				if err != nil {
					fail(fmt.Sprintf("gate: %v", err))
					http.Error(w, "gate", 503)
					return
				}
				defer resp.Body.Close()
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(resp.StatusCode)
				if _, err := io.Copy(w, resp.Body); err != nil {
					fail(fmt.Sprintf("response: %v", err))
				}
				return
			}
			select {
			case <-changed:
			case <-r.Context().Done():
				fail("cancelled while offline")
				return
			}
		}
	})
}

func (l *frontLedger) check(want int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.count != want || l.pending != 0 || len(l.failures) != 0 {
		return fmt.Errorf("frontend attempts=%d, want %d, pending=%d, failures=%v", l.count, want, l.pending, l.failures)
	}
	return nil
}

func validateTerminal(params json.RawMessage) error {
	var p struct {
		Turn struct {
			Status string `json:"status"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Turn.Status != "completed" {
		return fmt.Errorf("turn did not complete successfully: %s", params)
	}
	return nil
}

// Walk the JSON tree rather than interpreting request prompts as results.
func walkJSON(v any, visit func(map[string]any)) {
	switch x := v.(type) {
	case map[string]any:
		visit(x)
		for _, y := range x {
			walkJSON(y, visit)
		}
	case []any:
		for _, y := range x {
			walkJSON(y, visit)
		}
	}
}

const patchScript = `try { text(await tools.apply_patch("*** Begin Patch\n*** Add File: forbidden-patch\n+forbidden\n*** End Patch")); } catch(e) { text(String(e)); }`

//go:embed testdata/catalog.json
var knownCatalog []byte

// Descriptions are prose; retain routing, type, format and schema fields.
func withoutDescriptions(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, v := range x {
			if _, prose := v.(string); k == "description" && prose {
				continue
			}
			m[k] = withoutDescriptions(v)
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, v := range x {
			a[i] = withoutDescriptions(v)
		}
		return a
	default:
		return v
	}
}

// Examine both protocol catalog locations, including every direct entry.
func validateCatalog(b []byte) error {
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil || root == nil {
		return fmt.Errorf("invalid request: %v", err)
	}
	var found []any
	add := func(v any) error {
		a, ok := v.([]any)
		if !ok {
			return fmt.Errorf("catalog is not an array")
		}
		found = append(found, a...)
		return nil
	}
	if v, ok := root["tools"]; ok {
		if err := add(v); err != nil {
			return err
		}
	}
	input, ok := root["input"].([]any)
	if !ok {
		return fmt.Errorf("input is not an array")
	}
	for _, item := range input {
		m, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid input item")
		}
		if m["type"] == "additional_tools" {
			if err := add(m["tools"]); err != nil {
				return err
			}
		}
	}
	var expected []any
	if err := json.Unmarshal(knownCatalog, &expected); err != nil {
		return err
	}
	if !reflect.DeepEqual(withoutDescriptions(found), expected) {
		actual, _ := json.Marshal(found)
		return fmt.Errorf("unknown catalog; review and requalification required: %s", actual)
	}
	return nil
}
