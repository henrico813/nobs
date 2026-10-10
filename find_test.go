package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeNotes(t *testing.T, root string, relPaths ...string) {
	t.Helper()
	for _, rel := range relPaths {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("note\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Agents act on the note find returns, so a wrong pick means editing the wrong
// plan. Each case covers one rule or the order between rules.
func TestFindPicksNoteByMatchRules(t *testing.T) {
	tests := []struct {
		name        string
		notes       []string
		ref         string
		wantStatus  string
		wantPath    string
		wantMatches []string
	}{
		{
			name:       "issue code stops at word boundary",
			notes:      []string{"Projects/ABC-04 Alpha.md", "Projects/ABC-042 Beta.md"},
			ref:        "abc-04",
			wantStatus: "found",
			wantPath:   "Projects/ABC-04 Alpha.md",
		},
		{
			name:       "exact name wins over prefix",
			notes:      []string{"Plan.md", "Plan - old.md"},
			ref:        "Plan",
			wantStatus: "found",
			wantPath:   "Plan.md",
		},
		{
			name:       "exact vault path",
			notes:      []string{"A/Plan.md", "B/Plan.md"},
			ref:        "B/Plan.md",
			wantStatus: "found",
			wantPath:   "B/Plan.md",
		},
		{
			name:       "title ignores issue code and punctuation",
			notes:      []string{"Projects/ABC-113 Simplify vault: resolution.md"},
			ref:        "simplify vault resolution",
			wantStatus: "found",
			wantPath:   "Projects/ABC-113 Simplify vault: resolution.md",
		},
		{
			// Agents show ambiguous matches to the user, so the order must be stable.
			// "Plans 2/" is visited after "Plans/" but sorts before it, so this case
			// catches missing sorting.
			name:        "same title in two notes",
			notes:       []string{"Plans/ABC-113 Simplify vault.md", "Plans 2/ABC-214 Simplify vault.md"},
			ref:         "simplify vault",
			wantStatus:  "ambiguous",
			wantMatches: []string{"Plans 2/ABC-214 Simplify vault.md", "Plans/ABC-113 Simplify vault.md"},
		},
		{
			name:       "prefix skips backup notes",
			notes:      []string{"ABC-113 Plan.md", "ABC-113 Plan.backup-2026-05-18.md"},
			ref:        "ABC-113",
			wantStatus: "found",
			wantPath:   "ABC-113 Plan.md",
		},
		{
			name:       "trash notes are not found",
			notes:      []string{".trash/ABC-200 Gone.md"},
			ref:        "ABC-200",
			wantStatus: "not_found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("OBSIDIAN_VAULT_DIR", root)
			writeNotes(t, root, tt.notes...)
			want := findResponse{Status: tt.wantStatus}
			if tt.wantPath != "" {
				want.Path = filepath.Join(root, tt.wantPath)
			}
			for _, rel := range tt.wantMatches {
				want.Matches = append(want.Matches, filepath.Join(root, rel))
			}

			got, err := handleFind(findRequest{Ref: tt.ref})

			if err != nil {
				t.Fatalf("handleFind returned error: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v, want %#v", got, want)
			}
		})
	}
}

// find must not create files in the vault: workstation vaults are git
// repositories, and a stray file shows up as a change. In a read-only vault a
// write that find depends on, such as readyHealth's write probe, fails, so a
// successful find shows it needs none.
func TestFindRouteNeverWritesToVault(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OBSIDIAN_VAULT_DIR", root)
	writeNotes(t, root, "ABC-110 Move config.md")
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })
	if os.WriteFile(filepath.Join(root, "write-check"), nil, 0o644) == nil {
		t.Skip("read-only folders are writable for this user")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/find", strings.NewReader(`{"ref":"ABC-110"}`))
	newBrokerMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

// An unmounted network drive looks like an empty folder. find must report
// that, not not_found, or an agent concludes the note does not exist.
func TestFindFailsWhenVaultNotMounted(t *testing.T) {
	t.Setenv("OBSIDIAN_VAULT_DIR", t.TempDir())
	t.Setenv("NOBS_VAULT_IS_MOUNTED", "true")

	_, err := handleFind(findRequest{Ref: "ABC-110"})

	var nobsErr *NOBSError
	if !errors.As(err, &nobsErr) || nobsErr.Code != NOBSErrBrokerUnavailable {
		t.Fatalf("error = %v, want broker unavailable", err)
	}
}

// A vault folder can be a link to another folder, such as a network mount.
// find must walk the link's target and read must compare notes with the real
// folder, or find returns not_found and read rejects every note.
func TestFindWorksThroughSymlinkedVaultRoot(t *testing.T) {
	target := t.TempDir()
	writeNotes(t, target, "Projects/ABC-110 Move config.md")
	link := filepath.Join(t.TempDir(), "Vault")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OBSIDIAN_VAULT_DIR", link)

	found, err := handleFind(findRequest{Ref: "ABC-110"})
	if err != nil {
		t.Fatalf("handleFind returned error: %v", err)
	}
	read, readErr := handleRead(notePathRequest{Path: "Projects/ABC-110 Move config.md"})

	want := findResponse{Status: "found", Path: filepath.Join(link, "Projects", "ABC-110 Move config.md")}
	if !reflect.DeepEqual(found, want) {
		t.Fatalf("find = %#v, want %#v", found, want)
	}
	if readErr != nil || read.Body != "note\n" {
		t.Fatalf("read = %#v, %v", read, readErr)
	}
}

// find must wait while sync-now replaces notes, or it can report not_found for
// a note that is briefly gone.
func TestFindWaitsForSyncLock(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OBSIDIAN_VAULT_DIR", root)
	writeNotes(t, root, "ABC-110 Move config.md")

	vaultMu.Lock()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/find", strings.NewReader(`{"ref":"ABC-110"}`))
		newBrokerMux().ServeHTTP(rec, req)
		done <- rec
	}()
	select {
	case <-done:
		vaultMu.Unlock()
		t.Fatal("find answered while sync held the lock")
	case <-time.After(20 * time.Millisecond):
	}
	vaultMu.Unlock()

	select {
	case rec := <-done:
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("find stayed blocked after the lock was released")
	}
}

// Agents call find with --json and parse status and path by name, so rejecting
// the flag or renaming a field breaks every prompt.
func TestFindCommandAcceptsJSONFlag(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OBSIDIAN_VAULT_DIR", root)
	writeNotes(t, root, "ABC-110 Move config.md")
	server := httptest.NewServer(newBrokerMux())
	defer server.Close()
	t.Setenv("NOBS_BROKER_URL", server.URL)

	code, stdout := captureStdout(t, func() int { return runMain([]string{"find", "--json", "ABC-110"}) })

	if code != 0 {
		t.Fatalf("exit code = %d, output %q", code, stdout)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	want := map[string]any{"status": "found", "path": filepath.Join(root, "ABC-110 Move config.md")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func captureStdout(t *testing.T, run func() int) (int, string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = stdout }()
	output := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		output <- string(b)
	}()
	code := run()
	w.Close()
	return code, <-output
}
