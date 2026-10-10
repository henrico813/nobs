package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleRG(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		script    string
		wantLines int
		wantFirst searchMatch
		wantErr   bool
	}{
		{
			name:      "matches",
			query:     "Tasks",
			script:    "#!/bin/sh\nprintf 'Projects/Plan.md\\0'\nprintf '3:## Tasks\n'\nprintf 'Projects/Plan.md\\0'\nprintf '4:- item\n'\n",
			wantLines: 2,
			wantFirst: searchMatch{Path: "Projects/Plan.md", Line: 3, Text: "## Tasks"},
		},
		{
			name:      "query starting with dash uses -e",
			query:     "-uu",
			script:    "#!/bin/sh\n[ \"$8\" = \"-e\" ] || exit 9\n[ \"$9\" = \"-uu\" ] || exit 8\nprintf 'Projects/Plan.md\\0'\nprintf '5:-uu\n'\n",
			wantLines: 1,
			wantFirst: searchMatch{Path: "Projects/Plan.md", Line: 5, Text: "-uu"},
		},
		{
			name:      "path containing colon",
			query:     "Tasks",
			script:    "#!/bin/sh\nprintf 'Projects/Plan:Q1.md\\0'\nprintf '3:## Tasks\n'\n",
			wantLines: 1,
			wantFirst: searchMatch{Path: "Projects/Plan:Q1.md", Line: 3, Text: "## Tasks"},
		},
		{
			name:      "no matches",
			query:     "Tasks",
			script:    "#!/bin/sh\nexit 1\n",
			wantLines: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			binDir := t.TempDir()
			t.Setenv("OBSIDIAN_VAULT_DIR", root)
			t.Setenv("PATH", fmt.Sprintf("%s:%s", binDir, os.Getenv("PATH")))
			if err := os.WriteFile(filepath.Join(binDir, "rg"), []byte(tt.script), 0o755); err != nil {
				t.Fatalf("write rg stub: %v", err)
			}
			resp, err := handleRG(searchRequest{Query: tt.query})
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("handleRG returned error: %v", err)
			}
			if len(resp.Matches) != tt.wantLines {
				t.Fatalf("matches = %d, want %d", len(resp.Matches), tt.wantLines)
			}
			if tt.wantLines > 0 && resp.Matches[0] != tt.wantFirst {
				t.Fatalf("first match = %#v, want %#v", resp.Matches[0], tt.wantFirst)
			}
		})
	}
}

func TestInspectPatchRejectsUnsafeDiffs(t *testing.T) {
	tests := []struct {
		name  string
		patch string
	}{
		{
			name: "multi-file",
			patch: strings.Join([]string{
				"diff --git a/Inbox/a.md b/Inbox/a.md",
				"--- a/Inbox/a.md",
				"+++ b/Inbox/a.md",
				"@@ -1 +1 @@",
				"-a",
				"+b",
				"diff --git a/Inbox/b.md b/Inbox/b.md",
				"--- a/Inbox/b.md",
				"+++ b/Inbox/b.md",
				"@@ -1 +1 @@",
				"-a",
				"+b",
			}, "\n") + "\n",
		},
		{
			name: "create note",
			patch: strings.Join([]string{
				"diff --git a/Inbox/new.md b/Inbox/new.md",
				"new file mode 100644",
				"--- /dev/null",
				"+++ b/Inbox/new.md",
				"@@ -0,0 +1 @@",
				"+hello",
			}, "\n") + "\n",
		},
		{
			name: "rename note",
			patch: strings.Join([]string{
				"diff --git a/Inbox/old.md b/Inbox/new.md",
				"rename from Inbox/old.md",
				"rename to Inbox/new.md",
				"--- a/Inbox/old.md",
				"+++ b/Inbox/new.md",
				"@@ -1 +1 @@",
				"-hello",
				"+world",
			}, "\n") + "\n",
		},
		{
			name: "hidden path",
			patch: strings.Join([]string{
				"diff --git a/.obsidian/app.md b/.obsidian/app.md",
				"--- a/.obsidian/app.md",
				"+++ b/.obsidian/app.md",
				"@@ -1 +1 @@",
				"-a",
				"+b",
			}, "\n") + "\n",
		},
		{
			name: "mode change",
			patch: strings.Join([]string{
				"diff --git a/Inbox/note.md b/Inbox/note.md",
				"old mode 100644",
				"new mode 100755",
				"--- a/Inbox/note.md",
				"+++ b/Inbox/note.md",
				"@@ -1 +1 @@",
				"-a",
				"+b",
			}, "\n") + "\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := inspectPatch(tt.patch); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestHandleApplyPatchUpdatesOneNote(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Projects"), 0o755); err != nil {
		t.Fatalf("mkdir projects: %v", err)
	}
	path := filepath.Join(root, "Projects", "Plan.md")
	before := "# Plan\n\n## Tasks\n- old item\n\n## Notes\nkeep\n"
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}
	t.Setenv("OBSIDIAN_VAULT_DIR", root)
	patch := strings.Join([]string{
		"diff --git a/Projects/Plan.md b/Projects/Plan.md",
		"--- a/Projects/Plan.md",
		"+++ b/Projects/Plan.md",
		"@@ -1,7 +1,7 @@",
		" # Plan",
		" ",
		" ## Tasks",
		"-- old item",
		"+- new item",
		" ",
		" ## Notes",
		" keep",
	}, "\n") + "\n"
	resp, err := handleApplyPatch(patchRequest{Patch: patch})
	if err != nil {
		t.Fatalf("handleApplyPatch returned error: %v", err)
	}
	if resp.Path != "Projects/Plan.md" || resp.Operation != "apply-patch" || resp.Hunks != 1 {
		t.Fatalf("unexpected response %#v", resp)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	want := "# Plan\n\n## Tasks\n- new item\n\n## Notes\nkeep\n"
	if string(body) != want {
		t.Fatalf("body = %q, want %q", string(body), want)
	}
}

func TestHandleApplyPatchRejectsDriftedContext(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Projects"), 0o755); err != nil {
		t.Fatalf("mkdir projects: %v", err)
	}
	path := filepath.Join(root, "Projects", "Plan.md")
	if err := os.WriteFile(path, []byte("# Plan\n\n## Tasks\n- different item\n"), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}
	t.Setenv("OBSIDIAN_VAULT_DIR", root)
	patch := strings.Join([]string{
		"diff --git a/Projects/Plan.md b/Projects/Plan.md",
		"--- a/Projects/Plan.md",
		"+++ b/Projects/Plan.md",
		"@@ -1,4 +1,4 @@",
		" # Plan",
		" ",
		" ## Tasks",
		"-- old item",
		"+- new item",
	}, "\n") + "\n"
	if _, err := handleApplyPatch(patchRequest{Patch: patch}); err == nil {
		t.Fatal("expected error")
	}
}
