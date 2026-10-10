package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncCLIOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		script     string
		call       func() (syncStatusResponse, error)
		configured bool
		status     string
		wantErr    bool
	}{
		{
			name:       "status not configured",
			script:     "#!/bin/sh\nif [ \"$1\" = sync-status ]; then echo \"No sync configuration found for /vault\" >&2; exit 3; fi\nexit 0\n",
			call:       syncStatus,
			configured: false,
			status:     "not-configured",
		},
		{
			name:    "sync not configured",
			script:  "#!/bin/sh\nif [ \"$1\" = sync-status ]; then echo \"No sync configuration found for /vault\" >&2; exit 3; fi\nexit 0\n",
			call:    syncNow,
			wantErr: true,
		},
		{
			name:       "sync ok",
			script:     "#!/bin/sh\ncase \"$1\" in\n  sync-status) echo linked; exit 0 ;;\n  sync) echo sync ok; exit 0 ;;\nesac\nexit 1\n",
			call:       syncNow,
			configured: true,
			status:     "ok",
		},
		{
			name:    "status symlink failure",
			script:  "#!/bin/sh\nif [ \"$1\" = sync-status ]; then echo failed to resolve symlink >&2; exit 1; fi\nexit 0\n",
			call:    syncStatus,
			wantErr: true,
		},
		{
			name:    "sync hard failure",
			script:  "#!/bin/sh\necho boom >&2\nexit 1\n",
			call:    syncNow,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			binDir := t.TempDir()
			t.Setenv("OBSIDIAN_VAULT_DIR", root)
			t.Setenv("OBSIDIAN_SYNC_CONFIG_DIR", ".obsidian")
			t.Setenv("PATH", fmt.Sprintf("%s:%s", binDir, os.Getenv("PATH")))
			if err := os.WriteFile(filepath.Join(binDir, "ob"), []byte(tt.script), 0o755); err != nil {
				t.Fatalf("write ob stub: %v", err)
			}
			resp, err := tt.call()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("sync call returned error: %v", err)
			}
			if resp.Configured != tt.configured || resp.Status != tt.status {
				t.Fatalf("unexpected response %#v", resp)
			}
		})
	}
}
