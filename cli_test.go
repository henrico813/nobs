package main

import (
	"strings"
	"testing"
)

func TestUsageMentionsDailyCommands(t *testing.T) {
	for _, needle := range []string{"nobs today", "nobs daily [DATE]", "nobs sync-now", "nobs rg QUERY", "nobs append-note PATH < BODY", "nobs apply-patch < PATCH"} {
		if !strings.Contains(usage(), needle) {
			t.Fatalf("usage missing %q", needle)
		}
	}
}

func TestParsePathLikeCommands(t *testing.T) {
	tests := []struct {
		name    string
		fn      func() error
		wantErr bool
	}{
		{name: "read ok", fn: func() error { _, err := parsePathCLI("read", []string{"Inbox/test.md"}); return err }},
		{name: "read missing path", fn: func() error { _, err := parsePathCLI("read", nil); return err }, wantErr: true},
		{name: "daily default", fn: func() error { _, err := parseDailyCLI(nil); return err }},
		{name: "daily explicit", fn: func() error { _, err := parseDailyCLI([]string{"2026-05-18"}); return err }},
		{name: "daily extra args", fn: func() error { _, err := parseDailyCLI([]string{"2026-05-18", "extra"}); return err }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fn()
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestParseWriteCLI(t *testing.T) {
	tests := []struct {
		name    string
		command string
		args    []string
		body    string
		want    noteWriteRequest
		wantErr bool
	}{
		{name: "write ok", command: "write", args: []string{"Inbox/test.md"}, body: "hello\n", want: noteWriteRequest{Path: "Inbox/test.md", Body: "hello\n"}},
		{name: "append ok", command: "append", args: []string{"Daily/2026-05-18.md"}, body: "line\n", want: noteWriteRequest{Path: "Daily/2026-05-18.md", Body: "line\n"}},
		{name: "append note ok", command: "append-note", args: []string{"Daily/2026-05-18.md"}, body: "closeout\n", want: noteWriteRequest{Path: "Daily/2026-05-18.md", Body: "closeout\n"}},
		{name: "missing path", command: "write", body: "hello\n", wantErr: true},
		{name: "empty body", command: "write", args: []string{"Inbox/test.md"}, body: "   ", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseWriteCLI(tt.command, tt.args, strings.NewReader(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseWriteCLI returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %#v want %#v", got, tt.want)
			}
		})
	}
}

func TestParsePatchCLI(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		body    string
		want    patchRequest
		wantErr bool
	}{
		{name: "ok", body: "diff --git a/Inbox/test.md b/Inbox/test.md\n@@ -1 +1 @@\n-a\n+b\n", want: patchRequest{Patch: "diff --git a/Inbox/test.md b/Inbox/test.md\n@@ -1 +1 @@\n-a\n+b\n"}},
		{name: "unexpected args", args: []string{"Inbox/test.md"}, body: "diff --git a/Inbox/test.md b/Inbox/test.md\n@@ -1 +1 @@\n-a\n+b\n", wantErr: true},
		{name: "empty body", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePatchCLI(tt.args, strings.NewReader(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePatchCLI returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %#v want %#v", got, tt.want)
			}
		})
	}
}
