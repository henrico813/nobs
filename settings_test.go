package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Gives the test its own home and config folders and clears NOBS_BROKER_URL,
// so nothing reads or writes the real settings. Returns the settings file path.
func useTempSettings(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("NOBS_BROKER_URL", "")
	return filepath.Join(config, "nobs", "config.json")
}

// Returns a new folder that looks like an Obsidian vault.
func makeVault(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".obsidian"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeSettings(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func listVaults(t *testing.T) []vaultListItem {
	t.Helper()
	code, stdout := captureStdout(t, func() int { return runMain([]string{"vault", "list"}) })
	if code != 0 {
		t.Fatalf("vault list exit code = %d", code)
	}
	var items []vaultListItem
	if err := json.Unmarshal([]byte(stdout), &items); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	return items
}

// A bad name breaks the Docker project name brokers get, and a folder without
// .obsidian is not enrolled or its drive is not mounted. set must refuse both
// and leave the settings file alone.
func TestVaultSetRejectsBadNamesAndFolders(t *testing.T) {
	tests := []struct {
		name  string
		vault string
		path  func(t *testing.T) string
	}{
		{name: "uppercase name", vault: "Main", path: makeVault},
		{name: "name starts with digit", vault: "1main", path: makeVault},
		{name: "relative path", vault: "main", path: func(t *testing.T) string { return "notes" }},
		{name: "no obsidian folder", vault: "main", path: func(t *testing.T) string { return t.TempDir() }},
		{name: "missing folder", vault: "main", path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "gone") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := useTempSettings(t)

			code, _ := captureStdout(t, func() int { return runMain([]string{"vault", "set", tt.vault, tt.path(t)}) })

			if code == 0 {
				t.Fatal("vault set succeeded")
			}
			if _, err := os.Stat(settings); !os.IsNotExist(err) {
				t.Fatalf("settings file was written: %v", err)
			}
		})
	}
}

// Two vaults on one port would send one vault's commands to the other's
// broker. Ports must stay unique, survive a path change, and be reused after
// rm. Any vault names work, not only main and work.
func TestVaultSetKeepsPortsUnique(t *testing.T) {
	useTempSettings(t)
	folders := map[string]string{"main": makeVault(t), "work": makeVault(t), "notes": makeVault(t)}
	for _, name := range []string{"main", "work", "notes"} {
		if code, _ := captureStdout(t, func() int { return runMain([]string{"vault", "set", name, folders[name]}) }); code != 0 {
			t.Fatalf("vault set %s exit code = %d", name, code)
		}
	}
	before := listVaults(t)
	ports := map[string]int{}
	for _, item := range before {
		ports[item.Name] = item.Port
	}

	moved := makeVault(t)
	captureStdout(t, func() int { return runMain([]string{"vault", "set", "work", moved}) })
	captureStdout(t, func() int { return runMain([]string{"vault", "rm", "notes"}) })
	captureStdout(t, func() int { return runMain([]string{"vault", "set", "journal", makeVault(t)}) })
	after := listVaults(t)

	if len(before) != 3 || ports["main"] == ports["work"] || ports["main"] == ports["notes"] || ports["work"] == ports["notes"] {
		t.Fatalf("three vaults with distinct ports expected, got %#v", before)
	}
	for _, port := range ports {
		if port < firstVaultPort {
			t.Fatalf("port %d is below %d", port, firstVaultPort)
		}
	}
	want := []vaultListItem{
		{Name: "journal", Path: after[0].Path, Port: ports["notes"]},
		{Name: "main", Path: folders["main"], Port: ports["main"]},
		{Name: "work", Path: moved, Port: ports["work"]},
	}
	if !reflect.DeepEqual(after, want) {
		t.Fatalf("after changes got %#v, want %#v", after, want)
	}
}

// A computer with no vaults is a normal state. vault list must print [] rather
// than fail, so people and scripts that list vaults get an empty list.
func TestVaultListEmptyWithoutSettingsFile(t *testing.T) {
	useTempSettings(t)

	code, stdout := captureStdout(t, func() int { return runMain([]string{"vault", "list"}) })

	if code != 0 || strings.TrimSpace(stdout) != "[]" {
		t.Fatalf("exit code %d, output %q; want 0 and []", code, stdout)
	}
}

// A hand-edited file that two vaults share a port in, or that has a typo,
// must stop every command rather than send it to the wrong broker.
func TestVaultCommandsRejectBadSettingsFile(t *testing.T) {
	vault := makeVault(t)
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown key", body: `{"vaults":{},"defualt":"main"}`},
		{name: "shared port", body: `{"vaults":{"main":{"path":VAULT,"port":8781},"work":{"path":VAULT,"port":8781}}}`},
		{name: "default not listed", body: `{"default":"work","vaults":{"main":{"path":VAULT,"port":8781}}}`},
		{name: "bad name", body: `{"vaults":{"Main":{"path":VAULT,"port":8781}}}`},
		{name: "relative path", body: `{"vaults":{"main":{"path":"notes","port":8781}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := useTempSettings(t)
			writeSettings(t, settings, strings.ReplaceAll(tt.body, "VAULT", strconv.Quote(vault)))

			code, _ := captureStdout(t, func() int { return runMain([]string{"vault", "list"}) })

			if code == 0 {
				t.Fatal("vault list accepted a bad settings file")
			}
		})
	}
}

// Planner and agents read nobs vault path to find notes, so it must follow
// the default vault, and fail clearly rather than print a wrong path once the
// default is removed.
func TestVaultPathFollowsDefaultVault(t *testing.T) {
	useTempSettings(t)
	main, work := makeVault(t), makeVault(t)
	run := func(args ...string) (int, string) {
		code, stdout := captureStdout(t, func() int { return runMain(append([]string{"vault"}, args...)) })
		return code, strings.TrimSpace(stdout)
	}

	run("set", "main", main)
	run("set", "work", work)
	_, first := run("path")
	defaultCode, _ := run("default", "work")
	_, second := run("path")
	_, named := run("path", "main")
	unknownCode, _ := run("default", "notes")
	run("rm", "work")
	rmCode, _ := run("path")
	listCode, _ := run("list")

	if first != main {
		t.Errorf("first vault set should be the default: path = %q, want %q", first, main)
	}
	if defaultCode != 0 || second != work {
		t.Errorf("after default work: exit %d, path = %q, want %q", defaultCode, second, work)
	}
	if named != main {
		t.Errorf("path main = %q, want %q", named, main)
	}
	if unknownCode == 0 {
		t.Error("default accepted a vault that is not in the list")
	}
	if rmCode == 0 || listCode != 0 {
		t.Errorf("after rm of the default: path exit %d, list exit %d; want path to fail and list to work", rmCode, listCode)
	}
}

// Every command picks its broker in one order: --vault, then NOBS_BROKER_URL,
// then the default vault. A wrong pick sends a sync or a search to the wrong
// vault. Broker containers have NOBS_BROKER_URL and no settings file, so a
// broken file must not affect them.
func TestBrokerOrderFlagThenEnvThenDefault(t *testing.T) {
	brokers := map[string]string{}
	for _, name := range []string{"main", "work", "env"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeBrokerJSON(w, findResponse{Status: "found", Path: name})
		}))
		defer server.Close()
		brokers[name] = server.URL
	}
	port := func(name string) int {
		u, _ := url.Parse(brokers[name])
		p, _ := strconv.Atoi(u.Port())
		return p
	}
	withDefault := fmt.Sprintf(`{"default":"main","vaults":{"main":{"path":"/m","port":%d},"work":{"path":"/w","port":%d}}}`, port("main"), port("work"))
	noDefault := fmt.Sprintf(`{"vaults":{"main":{"path":"/m","port":%d},"work":{"path":"/w","port":%d}}}`, port("main"), port("work"))
	tests := []struct {
		name     string
		settings string
		env      bool
		flag     string
		want     string
		wantCode int
	}{
		{name: "flag beats env and default", settings: withDefault, env: true, flag: "work", want: "work"},
		{name: "flag without default", settings: noDefault, flag: "work", want: "work"},
		{name: "env beats default", settings: withDefault, env: true, want: "env"},
		{name: "env without settings file", env: true, want: "env"},
		{name: "env ignores broken settings", settings: `{"vaults":`, env: true, want: "env"},
		{name: "default when nothing else", settings: withDefault, want: "main"},
		{name: "unknown flag name with env", settings: withDefault, env: true, flag: "notes", wantCode: int(NOBSErrInvalidArgs)},
		{name: "no default and no env", settings: noDefault, wantCode: int(NOBSErrBrokerUnavailable)},
		{name: "no settings file and no env", wantCode: int(NOBSErrBrokerUnavailable)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := useTempSettings(t)
			if tt.settings != "" {
				writeSettings(t, settings, tt.settings)
			}
			if tt.env {
				t.Setenv("NOBS_BROKER_URL", brokers["env"])
			}
			args := []string{"find", "Plan"}
			if tt.flag != "" {
				args = append([]string{"--vault", tt.flag}, args...)
			}

			code, stdout := captureStdout(t, func() int { return runMain(args) })

			if code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d (output %q)", code, tt.wantCode, stdout)
			}
			if tt.wantCode != 0 {
				return
			}
			var got findResponse
			if err := json.Unmarshal([]byte(stdout), &got); err != nil {
				t.Fatalf("decode %q: %v", stdout, err)
			}
			if got.Path != tt.want {
				t.Fatalf("command went to the %s broker, want %s", got.Path, tt.want)
			}
		})
	}
}
