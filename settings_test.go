package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Gives the test its own home and config folders, clears NOBS_BROKER_URL, and
// puts a fake docker that succeeds on PATH, so nothing reads or writes the
// real settings or starts a container. Returns the settings file path.
func useTempSettings(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("NOBS_BROKER_URL", "")
	t.Setenv("NOBS_VERSION", "v0.0.0")
	useFakeDocker(t, "exit 0")
	wait := brokerStartWait
	brokerStartWait = 0
	t.Cleanup(func() { brokerStartWait = wait })
	return filepath.Join(config, "nobs", "config.json")
}

// Puts a fake docker first on PATH. Each call appends one line to the returned
// log: its arguments, then the vault folder, port and user it was given. Then
// it runs the shell lines in then.
func useFakeDocker(t *testing.T, then string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	script := fmt.Sprintf("#!/bin/sh\ncat >/dev/null\necho \"$* vault=$NOBS_VAULT_PATH port=$NOBS_PORT user=$NOBS_UID:$NOBS_GID\" >>%q\n%s\n", log, then)
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// Returns the lines of a fake docker log, or none when docker never ran.
func dockerCalls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// Returns a port on 127.0.0.1 that nothing listens on, like a stopped broker's.
func closedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
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
// broker, and a port another program holds stops the broker from starting.
// Ports must stay unique, skip ports in use, survive a path change, and be
// reused after rm. Any vault names work, not only main and work.
func TestVaultSetKeepsPortsUnique(t *testing.T) {
	useTempSettings(t)
	// Another program holds the first port, unless one already does.
	if busy, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", firstVaultPort)); err == nil {
		defer busy.Close()
	}
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
		if port <= firstVaultPort {
			t.Fatalf("port %d is not above %d, which another program holds", port, firstVaultPort)
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

// set must start the broker with the vault's folder and port and the user's
// IDs, and rm must remove it. When Docker fails, the settings file must not
// change, so it never lists a vault without a broker and never forgets one
// whose broker is running.
func TestVaultSetAndRmFollowDocker(t *testing.T) {
	settings := useTempSettings(t)
	exitCode := filepath.Join(t.TempDir(), "exit-code")
	log := useFakeDocker(t, fmt.Sprintf("exit $(cat %q)", exitCode))
	dockerExits := func(code string) {
		if err := os.WriteFile(exitCode, []byte(code), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vault := makeVault(t)
	run := func(args ...string) int {
		code, _ := captureStdout(t, func() int { return runMain(append([]string{"vault"}, args...)) })
		return code
	}

	// Docker fails: set must not save the vault.
	dockerExits("1")
	failedSet := run("set", "main", vault)
	_, statErr := os.Stat(settings)
	// Docker works: set saves the vault and starts its broker.
	dockerExits("0")
	set := run("set", "main", vault)
	items := listVaults(t)
	// Docker fails: rm must keep the vault.
	dockerExits("1")
	failedRm := run("rm", "main")
	kept := listVaults(t)
	// Docker works: rm stops the broker and removes the vault.
	dockerExits("0")
	rm := run("rm", "main")
	removed := listVaults(t)

	if failedSet == 0 || !os.IsNotExist(statErr) {
		t.Fatalf("set with failing docker: exit %d, settings file error %v; want failure and no file", failedSet, statErr)
	}
	if set != 0 || len(items) != 1 {
		t.Fatalf("set: exit %d, vaults %#v", set, items)
	}
	if failedRm == 0 || len(kept) != 1 {
		t.Fatalf("rm with failing docker: exit %d, vaults %#v; want failure and main kept", failedRm, kept)
	}
	if rm != 0 || len(removed) != 0 {
		t.Fatalf("rm: exit %d, vaults %#v", rm, removed)
	}
	user := fmt.Sprintf("user=%d:%d", os.Getuid(), os.Getgid())
	started := fmt.Sprintf("compose -p nobs-main -f - up -d vault=%s port=%d %s", vault, items[0].Port, user)
	stopped := fmt.Sprintf("compose -p nobs-main -f - down vault=%s port=%d %s", vault, items[0].Port, user)
	want := []string{started, started, stopped, stopped}
	if got := dockerCalls(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("docker calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// After a restart, a broker whose drive was not mounted yet stays down, and
// Docker does not retry it. The first command must start it once and send the
// request again, and a start that fails must not be tried again.
func TestStoppedBrokerStartsOnceThenRetries(t *testing.T) {
	tests := []struct {
		name       string
		dockerExit int
		wantCode   int
	}{
		{name: "start works", dockerExit: 0, wantCode: 0},
		{name: "start fails", dockerExit: 1, wantCode: int(NOBSErrBrokerUnavailable)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := useTempSettings(t)
			brokerStartWait = 5 * time.Second
			port := closedPort(t)
			writeSettings(t, settings, fmt.Sprintf(`{"default":"main","vaults":{"main":{"path":%q,"port":%d}}}`, makeVault(t), port))
			// docker creates started, then waits for serving before it exits, so
			// the stand-in broker below is up even when docker fails. A retry
			// after a failed start would then succeed and the test would see it.
			dir := t.TempDir()
			started, serving := filepath.Join(dir, "started"), filepath.Join(dir, "serving")
			log := useFakeDocker(t, fmt.Sprintf("touch %q\nwhile [ ! -e %q ]; do sleep 0.01; done\nexit %d", started, serving, tt.dockerExit))
			// Stands in for the container: answers on the port once docker runs.
			broker := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeBrokerJSON(w, findResponse{Status: "found", Path: "from started broker"})
			})}
			t.Cleanup(func() { broker.Close() })
			go func() {
				for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
					if _, err := os.Stat(started); err == nil {
						listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
						_ = os.WriteFile(serving, nil, 0o644)
						if err == nil {
							broker.Serve(listener)
						}
						return
					}
				}
			}()

			code, stdout := captureStdout(t, func() int { return runMain([]string{"find", "Plan"}) })

			if code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d (output %q)", code, tt.wantCode, stdout)
			}
			if tt.wantCode == 0 && !strings.Contains(stdout, "from started broker") {
				t.Fatalf("output %q did not come from the started broker", stdout)
			}
			calls := dockerCalls(t, log)
			if len(calls) != 1 || !strings.HasPrefix(calls[0], "compose -p nobs-main -f - up -d ") {
				t.Fatalf("docker calls = %q, want one compose up -d for nobs-main", calls)
			}
		})
	}
}

// NOBS_BROKER_URL names a broker this computer does not run, such as the one
// an agent container uses. nobs must never start Docker for it, even when it
// does not answer and the computer has a default vault.
func TestBrokerURLBrokerIsNeverStarted(t *testing.T) {
	settings := useTempSettings(t)
	log := useFakeDocker(t, "exit 0")
	writeSettings(t, settings, fmt.Sprintf(`{"default":"main","vaults":{"main":{"path":%q,"port":%d}}}`, makeVault(t), closedPort(t)))
	t.Setenv("NOBS_BROKER_URL", fmt.Sprintf("http://127.0.0.1:%d", closedPort(t)))

	code, _ := captureStdout(t, func() int { return runMain([]string{"find", "Plan"}) })

	if code != int(NOBSErrBrokerUnavailable) {
		t.Fatalf("exit code = %d, want %d", code, NOBSErrBrokerUnavailable)
	}
	if calls := dockerCalls(t, log); len(calls) != 0 {
		t.Fatalf("docker ran for a NOBS_BROKER_URL broker: %q", calls)
	}
}

// People and agents read running from vault list to see which brokers
// answer. A broker that is down must show false rather than fail the list.
func TestVaultListShowsWhichBrokersRun(t *testing.T) {
	settings := useTempSettings(t)
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeBrokerJSON(w, map[string]string{"status": "ok"})
	}))
	defer broker.Close()
	u, _ := url.Parse(broker.URL)
	up, _ := strconv.Atoi(u.Port())
	down := closedPort(t)
	writeSettings(t, settings, fmt.Sprintf(`{"vaults":{"main":{"path":"/m","port":%d},"work":{"path":"/w","port":%d}}}`, up, down))

	items := listVaults(t)

	want := []vaultListItem{
		{Name: "main", Path: "/m", Port: up, Running: true},
		{Name: "work", Path: "/w", Port: down, Running: false},
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("vault list = %#v, want %#v", items, want)
	}
}
