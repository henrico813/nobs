package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteBrokerError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "invalid args", err: newNOBSError(NOBSErrInvalidArgs, "bad input"), want: http.StatusBadRequest},
		{name: "not found", err: newNOBSError(NOBSErrNotFound, "missing"), want: http.StatusNotFound},
		{name: "forbidden", err: newNOBSError(NOBSErrForbidden, "nope"), want: http.StatusForbidden},
		{name: "sync unavailable", err: newNOBSError(NOBSErrSyncUnavailable, "down"), want: http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeBrokerError(rec, tt.err)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestBrokerHealth(t *testing.T) {
	tests := []struct {
		name         string
		configStatus int
		wantErr      bool
	}{
		{name: "configured ok", configStatus: http.StatusOK},
		{name: "not configured conflict", configStatus: http.StatusConflict},
		{name: "config bad gateway", configStatus: http.StatusBadGateway, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/livez", "/readyz":
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"status":"ok"}`))
				case "/configz":
					w.WriteHeader(tt.configStatus)
					_, _ = w.Write([]byte(`{"configured":false,"status":"not-configured"}`))
				default:
					t.Fatalf("unexpected path %s", r.URL.Path)
				}
			}))
			defer server.Close()
			t.Setenv("NOBS_BROKER_URL", server.URL)
			resp, err := brokerHealth()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("brokerHealth returned error: %v", err)
			}
			if resp["live"].(map[string]any)["status"] != "ok" {
				t.Fatalf("unexpected live response %#v", resp)
			}
		})
	}
}

func TestPatchHandlersRejectInvalidRequests(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		method  string
		body    string
		want    int
	}{
		{name: "rg wrong method", handler: handleRGHTTP, method: http.MethodGet, want: http.StatusBadRequest},
		{name: "rg bad json", handler: handleRGHTTP, method: http.MethodPost, body: `{`, want: http.StatusBadRequest},
		{name: "patch wrong method", handler: handleApplyPatchHTTP, method: http.MethodGet, want: http.StatusBadRequest},
		{name: "patch bad json", handler: handleApplyPatchHTTP, method: http.MethodPost, body: `{`, want: http.StatusBadRequest},
		{name: "append note wrong method", handler: handleAppendNoteHTTP, method: http.MethodGet, want: http.StatusBadRequest},
		{name: "append note bad json", handler: handleAppendNoteHTTP, method: http.MethodPost, body: `{`, want: http.StatusBadRequest},
		{name: "append note empty body", handler: handleAppendNoteHTTP, method: http.MethodPost, body: `{"path":"Daily.md","body":"  "}`, want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, "/", strings.NewReader(tt.body))
			tt.handler(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestAppendNoteRouteUsesWriteLock(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Daily.md")
	if err := os.WriteFile(path, []byte("# Notes\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OBSIDIAN_VAULT_DIR", root)

	vaultMu.RLock()
	started := make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		close(started)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/append-note", strings.NewReader(`{"path":"Daily.md","body":"## Closeout"}`))
		newBrokerMux().ServeHTTP(rec, req)
		done <- rec
	}()
	<-started
	select {
	case <-done:
		vaultMu.RUnlock()
		t.Fatal("append-note route bypassed write lock")
	case <-time.After(20 * time.Millisecond):
	}
	vaultMu.RUnlock()
	select {
	case rec := <-done:
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	case <-time.After(time.Second):
		t.Fatal("append-note route remained blocked")
	}
}

func TestAppendNoteHandlerIsIdempotent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Daily.md")
	if err := os.WriteFile(path, []byte("# Notes\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OBSIDIAN_VAULT_DIR", root)

	for attempt, wantAlreadyPresent := range []bool{false, true} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/append-note", strings.NewReader(`{"path":"Daily.md","body":"## Closeout"}`))
		newBrokerMux().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if !strings.Contains(rec.Body.String(), `"already_present":`) {
			t.Fatalf("attempt %d response omitted already_present", attempt)
		}
		var resp noteResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("attempt %d decode response: %v", attempt, err)
		}
		if resp.AlreadyPresent == nil || *resp.AlreadyPresent != wantAlreadyPresent {
			t.Fatalf("attempt %d already_present = %v, want %v", attempt, resp.AlreadyPresent, wantAlreadyPresent)
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(body), "## Closeout"); got != 1 {
		t.Fatalf("closeout count = %d, want 1", got)
	}
}

func TestReadResponseOmitsReplayState(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Daily.md"), []byte("# Notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OBSIDIAN_VAULT_DIR", root)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/read", strings.NewReader(`{"path":"Daily.md"}`))
	handleReadHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if strings.Contains(rec.Body.String(), `"already_present"`) {
		t.Fatalf("read response included append-note state: %s", rec.Body.String())
	}
}

func TestBrokerListenAddress(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "default address", want: ":8080"},
		{name: "configured address", value: "127.0.0.1:8790", want: "127.0.0.1:8790"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NOBS_LISTEN", tt.value)
			if got := brokerListenAddress(); got != tt.want {
				t.Fatalf("address = %q, want %q", got, tt.want)
			}
		})
	}
}

// An agent's read during a sync must wait for the sync, not fail first. So
// every read command must wait longer than the longest sync.
func TestReadCommandsOutlastSyncNow(t *testing.T) {
	longestSync := syncRequestTimeout + obWaitDelay
	for _, command := range []string{"search", "rg", "read", "today", "daily"} {
		if got := brokerCommandTimeout(command); got <= longestSync {
			t.Errorf("%s waits %s, want more than sync-now's %s", command, got, longestSync)
		}
	}
}
