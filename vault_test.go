package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setTestNow(t *testing.T, value time.Time) {
	t.Helper()
	previous := nowFunc
	nowFunc = func() time.Time { return value }
	t.Cleanup(func() {
		nowFunc = previous
	})
}

func TestHandleSearchSkipsSymlinkMarkdown(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	t.Setenv("OBSIDIAN_VAULT_DIR", root)
	resp, err := handleSearch(searchRequest{Query: "secret"})
	if err != nil {
		t.Fatalf("handleSearch returned error: %v", err)
	}
	if len(resp.Matches) != 0 {
		t.Fatalf("unexpected matches %#v", resp.Matches)
	}
}

func TestHandleWriteRejectsUnsafePaths(t *testing.T) {
	t.Setenv("OBSIDIAN_VAULT_DIR", t.TempDir())
	tests := []struct {
		name string
		path string
	}{
		{name: "trash dir", path: ".trash/test.md"},
		{name: "obsidian dir", path: ".obsidian/app.md"},
		{name: "parent traversal", path: "../escape.md"},
		{name: "non markdown", path: "Inbox/test.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := handleWrite(noteWriteRequest{Path: tt.path, Body: "x"}, false); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestHandleMoveAndDeleteCollisions(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{name: "move destination exists", run: func(t *testing.T) {
			root := t.TempDir()
			_ = os.MkdirAll(filepath.Join(root, "Inbox"), 0o755)
			_ = os.MkdirAll(filepath.Join(root, "Projects"), 0o755)
			_ = os.WriteFile(filepath.Join(root, "Inbox", "a.md"), []byte("a"), 0o644)
			_ = os.WriteFile(filepath.Join(root, "Projects", "a.md"), []byte("b"), 0o644)
			t.Setenv("OBSIDIAN_VAULT_DIR", root)
			if _, err := handleMove(noteMoveRequest{Source: "Inbox/a.md", Destination: "Projects/a.md"}); err == nil {
				t.Fatal("expected error")
			}
		}},
		{name: "delete unique trash path", run: func(t *testing.T) {
			root := t.TempDir()
			fixed := time.Date(2026, time.May, 18, 12, 0, 0, 0, time.FixedZone("PDT", -7*60*60))
			_ = os.MkdirAll(filepath.Join(root, "Inbox"), 0o755)
			_ = os.MkdirAll(filepath.Join(root, ".trash", "2026-05-18", "Inbox"), 0o755)
			_ = os.WriteFile(filepath.Join(root, "Inbox", "note.md"), []byte("x"), 0o644)
			_ = os.WriteFile(filepath.Join(root, ".trash", "2026-05-18", "Inbox", "note.md"), []byte("old"), 0o644)
			t.Setenv("OBSIDIAN_VAULT_DIR", root)
			t.Setenv("TZ", "America/Los_Angeles")
			setTestNow(t, fixed)
			resp, err := handleDelete(notePathRequest{Path: "Inbox/note.md"})
			if err != nil {
				t.Fatalf("handleDelete returned error: %v", err)
			}
			if resp.TrashPath == ".trash/2026-05-18/Inbox/note.md" {
				t.Fatalf("trash path did not uniquify: %q", resp.TrashPath)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestHandleDailyCommands(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{name: "daily explicit date", run: func(t *testing.T) {
			root := t.TempDir()
			_ = os.MkdirAll(filepath.Join(root, "Daily"), 0o755)
			_ = os.WriteFile(filepath.Join(root, "Daily", "2026-05-18.md"), []byte("today"), 0o644)
			t.Setenv("OBSIDIAN_VAULT_DIR", root)
			resp, err := handleDaily(dailyRequest{Date: "2026-05-18"})
			if err != nil {
				t.Fatalf("handleDaily returned error: %v", err)
			}
			if resp.Path != "Daily/2026-05-18.md" {
				t.Fatalf("unexpected path %q", resp.Path)
			}
		}},
		{name: "today uses timezone", run: func(t *testing.T) {
			root := t.TempDir()
			fixed := time.Date(2026, time.May, 18, 12, 0, 0, 0, time.FixedZone("PDT", -7*60*60))
			_ = os.MkdirAll(filepath.Join(root, "Daily"), 0o755)
			_ = os.WriteFile(filepath.Join(root, "Daily", "2026-05-18.md"), []byte("today"), 0o644)
			t.Setenv("OBSIDIAN_VAULT_DIR", root)
			t.Setenv("TZ", "America/Los_Angeles")
			setTestNow(t, fixed)
			resp, err := handleToday()
			if err != nil {
				t.Fatalf("handleToday returned error: %v", err)
			}
			if resp.Path != "Daily/2026-05-18.md" {
				t.Fatalf("unexpected path %q", resp.Path)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestAppendNoteInsertsCloseout(t *testing.T) {
	tests := []struct {
		name string
		note string
		want string
	}{
		{name: "after divider", note: "# Notes\n---\n\nold\n", want: "# Notes\n---\n\n## Closeout\n\nold\n"},
		{name: "without divider", note: "# Notes\nold\n", want: "# Notes\n\n## Closeout\nold\n"},
		{name: "CRLF divider", note: "# Notes\r\n---\r\n\r\nold\r\n", want: "# Notes\r\n---\r\n\r\n## Closeout\r\n\r\nold\r\n"},
		{name: "divider at end", note: "# Notes\n---", want: "# Notes\n---\n\n## Closeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "Daily.md")
			if err := os.WriteFile(path, []byte(tt.note), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("OBSIDIAN_VAULT_DIR", root)

			resp, err := handleAppendNote(noteWriteRequest{Path: "Daily.md", Body: "## Closeout"})
			if err != nil {
				t.Fatalf("handleAppendNote returned error: %v", err)
			}
			if resp.AlreadyPresent == nil || *resp.AlreadyPresent {
				t.Fatal("new closeout reported as already present")
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(body); got != tt.want {
				t.Fatalf("body = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAppendNoteReplayIsNoop(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Daily.md")
	closeout := "## Engineering Closeout\n\n### Summary\n\nDone"
	note := "# Notes\n---\n\n" + closeout + "\n\n# Tasks\n"
	if err := os.WriteFile(path, []byte(note), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OBSIDIAN_VAULT_DIR", root)

	resp, err := handleAppendNote(noteWriteRequest{Path: "Daily.md", Body: closeout + "\n"})
	if err != nil {
		t.Fatalf("handleAppendNote returned error: %v", err)
	}
	if resp.AlreadyPresent == nil || !*resp.AlreadyPresent || resp.Bytes != 0 {
		t.Fatalf("unexpected replay response %#v", resp)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != note {
		t.Fatalf("body = %q, want %q", got, note)
	}
}

func TestAppendNoteRejectsFalseReplay(t *testing.T) {
	tests := []struct {
		name string
		note string
		body string
		want string
	}{
		{name: "same block outside notes", note: "# Tasks\n\n## Engineering Closeout\n\n# Notes\n---\n", body: "## Engineering Closeout", want: "# Tasks\n\n## Engineering Closeout\n\n# Notes\n---\n\n## Engineering Closeout\n"},
		{name: "longer block at insertion point", note: "# Notes\n---\n\n## Engineering Closeout Details\n", body: "## Engineering Closeout", want: "# Notes\n---\n\n## Engineering Closeout\n\n## Engineering Closeout Details\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "Daily.md")
			if err := os.WriteFile(path, []byte(tt.note), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("OBSIDIAN_VAULT_DIR", root)
			resp, err := handleAppendNote(noteWriteRequest{Path: "Daily.md", Body: tt.body})
			if err != nil {
				t.Fatalf("handleAppendNote returned error: %v", err)
			}
			if resp.AlreadyPresent == nil || *resp.AlreadyPresent {
				t.Fatal("new closeout reported as already present")
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(body); got != tt.want {
				t.Fatalf("body = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAppendNoteNormalizesMultilineBody(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Daily.md")
	if err := os.WriteFile(path, []byte("# Notes\r\n---\r\n\r\nold\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OBSIDIAN_VAULT_DIR", root)
	_, err := handleAppendNote(noteWriteRequest{Path: "Daily.md", Body: "## Engineering Closeout\n\n### Summary\n\nDone\n"})
	if err != nil {
		t.Fatalf("handleAppendNote returned error: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "# Notes\r\n---\r\n\r\n## Engineering Closeout\r\n\r\n### Summary\r\n\r\nDone\r\n\r\nold\r\n"
	if got := string(body); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}

func TestAppendNoteRejectsInvalidTarget(t *testing.T) {
	tests := []struct {
		name string
		note string
		body string
	}{
		{name: "missing heading", note: "# Tasks\n", body: "## Closeout"},
		{name: "nested heading", note: "## Notes\n", body: "## Closeout"},
		{name: "duplicate heading", note: "# Notes\n\n# Notes\n", body: "## Closeout"},
		{name: "empty body", note: "# Notes\n---\n", body: "  \n"},
		{name: "body adds notes heading", note: "# Notes\n---\n", body: "## Closeout\n# Notes"},
		{name: "body has bare carriage return", note: "# Notes\n---\n", body: "## Closeout\rtext"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "Daily.md")
			if err := os.WriteFile(path, []byte(tt.note), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("OBSIDIAN_VAULT_DIR", root)
			_, err := handleAppendNote(noteWriteRequest{Path: "Daily.md", Body: tt.body})
			if err == nil {
				t.Fatal("expected error")
			}
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if got := string(body); got != tt.note {
				t.Fatalf("body = %q, want %q", got, tt.note)
			}
		})
	}
}

func TestAppendNoteRejectsMissingFile(t *testing.T) {
	t.Setenv("OBSIDIAN_VAULT_DIR", t.TempDir())
	_, err := handleAppendNote(noteWriteRequest{Path: "Daily.md", Body: "## Closeout"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestReadyHealthChecksMountedVault(t *testing.T) {
	tests := []struct {
		name       string
		mounted    string
		unset      bool
		vaultEntry bool
		wantErr    bool
	}{
		{name: "unset", unset: true},
		{name: "mounted empty", mounted: "true", wantErr: true},
		{name: "mounted vault", mounted: "true", vaultEntry: true},
		{name: "invalid setting", mounted: "yes", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("OBSIDIAN_VAULT_DIR", root)
			t.Setenv("NOBS_VAULT_IS_MOUNTED", "")
			if tt.unset {
				os.Unsetenv("NOBS_VAULT_IS_MOUNTED")
			} else {
				t.Setenv("NOBS_VAULT_IS_MOUNTED", tt.mounted)
			}
			if tt.vaultEntry {
				if err := os.Mkdir(filepath.Join(root, ".obsidian"), 0o755); err != nil {
					t.Fatalf("make vault entry: %v", err)
				}
			}

			err := readyHealth()
			if (err != nil) != tt.wantErr {
				t.Fatalf("readyHealth error = %v, want error: %t", err, tt.wantErr)
			}
		})
	}
}

// find prints full paths, and agents pass them straight to read.
func TestReadAcceptsFullPathInsideVault(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Projects", "Plan.md"), []byte("note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OBSIDIAN_VAULT_DIR", root)

	resp, err := handleRead(notePathRequest{Path: filepath.Join(root, "Projects", "Plan.md")})

	if err != nil {
		t.Fatalf("read full path: %v", err)
	}
	if resp.Path != "Projects/Plan.md" || resp.Body != "note\n" {
		t.Fatalf("unexpected response %#v", resp)
	}
}

// A neighboring folder such as Vault-other starts with the vault's name, so a
// name-prefix check would treat its notes as inside the vault.
func TestReadRejectsFullPathOutsideVault(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "Vault")
	sibling := filepath.Join(parent, "Vault-other")
	for _, dir := range []string{root, sibling} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sibling, "Plan.md"), []byte("note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OBSIDIAN_VAULT_DIR", root)

	_, err := handleRead(notePathRequest{Path: filepath.Join(sibling, "Plan.md")})

	var nobsErr *NOBSError
	if !errors.As(err, &nobsErr) || nobsErr.Code != NOBSErrInvalidPath {
		t.Fatalf("error = %v, want invalid path", err)
	}
}
