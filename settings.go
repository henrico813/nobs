package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// The first port vault set gives out. Brokers listen on 127.0.0.1 only.
const firstVaultPort = 8781

// Names become Docker project names (nobs-NAME), so they are kept to what
// Docker accepts.
var vaultNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// The vaults on this computer, stored in config.json. Only the vault and tls
// commands write this file; a hand edit can disagree with the brokers they
// start. InsecureTLS turns off certificate checks in every broker here; it is
// left out of the file when false, because nobs v0.3.0 to v0.3.2 refuse
// keys they do not know.
type vaultSettings struct {
	Default     string                `json:"default,omitempty"`
	Vaults      map[string]vaultEntry `json:"vaults"`
	InsecureTLS bool                  `json:"insecure_tls,omitempty"`
}

type vaultEntry struct {
	Path string `json:"path"`
	Port int    `json:"port"`
}

// One vault as nobs vault list prints it. Running is true when its broker
// answers on its port.
type vaultListItem struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Port    int    `json:"port"`
	Running bool   `json:"running"`
}

// $XDG_CONFIG_HOME/nobs/config.json when XDG_CONFIG_HOME is absolute, else
// ~/.config/nobs/config.json. Anything else that reads the file must use the
// same rule.
func settingsPath() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "nobs", "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", newNOBSError(NOBSErrUnexpected, "cannot find the home folder: %v", err)
	}
	return filepath.Join(home, ".config", "nobs", "config.json"), nil
}

// Returns no vaults when the file does not exist. Fails on unknown keys, bad
// names, relative paths, shared ports, or a default that is not in the list,
// so a hand edit cannot point two vaults at one broker.
func loadVaultSettings() (vaultSettings, error) {
	settings := vaultSettings{Vaults: map[string]vaultEntry{}}
	path, err := settingsPath()
	if err != nil {
		return settings, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return settings, newNOBSError(NOBSErrUnexpected, "cannot read %s: %v", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&settings); err != nil {
		return settings, newNOBSError(NOBSErrUnexpected, "%s is not valid: %v", path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return settings, newNOBSError(NOBSErrUnexpected, "%s is not valid: extra data after the settings", path)
	}
	if settings.Vaults == nil {
		settings.Vaults = map[string]vaultEntry{}
	}
	ports := map[int]string{}
	for name, vault := range settings.Vaults {
		switch {
		case !vaultNamePattern.MatchString(name):
			return settings, newNOBSError(NOBSErrUnexpected, "%s is not valid: bad vault name %q", path, name)
		case !filepath.IsAbs(vault.Path):
			return settings, newNOBSError(NOBSErrUnexpected, "%s is not valid: vault %s path must be absolute", path, name)
		case vault.Port < 1 || vault.Port > 65535:
			return settings, newNOBSError(NOBSErrUnexpected, "%s is not valid: vault %s has port %d", path, name, vault.Port)
		case ports[vault.Port] != "":
			return settings, newNOBSError(NOBSErrUnexpected, "%s is not valid: vaults %s and %s share port %d", path, ports[vault.Port], name, vault.Port)
		}
		ports[vault.Port] = name
	}
	if _, ok := settings.Vaults[settings.Default]; settings.Default != "" && !ok {
		return settings, newNOBSError(NOBSErrUnexpected, "%s is not valid: default vault %s is not in the list", path, settings.Default)
	}
	return settings, nil
}

// Writes a temporary file in the same folder, flushes it to disk, then renames
// it over config.json, so a crash leaves either the previous file or the new
// one, never half of one. Two vault commands run at once can lose one's change.
func saveVaultSettings(settings vaultSettings) error {
	path, err := settingsPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return newNOBSError(NOBSErrUnexpected, "%s", err.Error())
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return newNOBSError(NOBSErrUnexpected, "cannot write %s: %v", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return newNOBSError(NOBSErrUnexpected, "cannot write %s: %v", path, err)
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(append(data, '\n'))
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return newNOBSError(NOBSErrUnexpected, "cannot write %s: %v", path, err)
	}
	return nil
}

// Returns the cleaned absolute path. Links are not resolved, because ob
// records a vault's enrollment under the path as given. The folder must
// contain .obsidian/, which an unenrolled folder or an unmounted network
// drive lacks.
func checkVaultFolder(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if !filepath.IsAbs(path) {
		return "", newNOBSError(NOBSErrInvalidPath, "vault path must be absolute: %s", raw)
	}
	path = filepath.Clean(path)
	info, err := os.Stat(filepath.Join(path, ".obsidian"))
	if err != nil || !info.IsDir() {
		return "", newNOBSError(NOBSErrInvalidPath, "%s has no .obsidian folder; is it an Obsidian vault, and is its drive mounted?", path)
	}
	return path, nil
}

// Returns the lowest port from firstVaultPort up that no vault in settings
// uses and nothing on 127.0.0.1 is listening on. If a program takes it
// before the broker starts, docker compose reports the port in use.
func pickVaultPort(settings vaultSettings) (int, error) {
	used := map[int]bool{}
	for _, vault := range settings.Vaults {
		used[vault.Port] = true
	}
	for port := firstVaultPort; port <= 65535; port++ {
		if used[port] {
			continue
		}
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			listener.Close()
			return port, nil
		}
	}
	return 0, newNOBSError(NOBSErrUnexpected, "no free port from %d up", firstVaultPort)
}

// Adds the vault or changes its path, keeping its port. When the file has no
// default, the vault being set becomes the default.
func setVault(settings *vaultSettings, name, folder string) error {
	if !vaultNamePattern.MatchString(name) {
		return newNOBSError(NOBSErrInvalidArgs, "vault name %q must start with a lowercase letter and use only lowercase letters, digits and hyphens", name)
	}
	vault, ok := settings.Vaults[name]
	if !ok {
		port, err := pickVaultPort(*settings)
		if err != nil {
			return err
		}
		vault.Port = port
	}
	vault.Path = folder
	settings.Vaults[name] = vault
	if settings.Default == "" {
		settings.Default = name
	}
	return nil
}

func unknownVaultError(name string) error {
	return newNOBSError(NOBSErrInvalidArgs, "no vault named %s; see nobs vault list", name)
}

// Which broker a CLI command talks to. The first match wins: the --vault flag
// (cliVault), then NOBS_BROKER_URL, then the default vault. Returns the
// broker's address and, for a vault on this computer, its name and this
// computer's settings; the name is empty for NOBS_BROKER_URL. With
// NOBS_BROKER_URL set and no flag, the settings file is not read, so
// containers that have no settings file are not affected by it.
func brokerBaseURL() (string, string, vaultSettings, error) {
	vault := cliVault
	if vault == "" {
		if url := strings.TrimRight(strings.TrimSpace(os.Getenv("NOBS_BROKER_URL")), "/"); url != "" {
			return url, "", vaultSettings{}, nil
		}
	}
	settings, err := loadVaultSettings()
	if err != nil {
		return "", "", vaultSettings{}, err
	}
	if vault == "" {
		vault = settings.Default
		if vault == "" {
			return "", "", vaultSettings{}, newNOBSError(NOBSErrBrokerUnavailable, "no broker chosen: pass --vault NAME, set NOBS_BROKER_URL, or add a vault with nobs vault set NAME PATH")
		}
	}
	entry, ok := settings.Vaults[vault]
	if !ok {
		return "", "", vaultSettings{}, unknownVaultError(vault)
	}
	return fmt.Sprintf("http://127.0.0.1:%d", entry.Port), vault, settings, nil
}

// Runs nobs vault SUBCOMMAND and returns the exit code. set starts the vault's
// broker and rm stops and removes it. Both change the settings file only after
// Docker succeeds, so the file never lists a vault whose broker could not be
// created, or drops one whose broker is running.
func runVault(args []string) int {
	if len(args) == 0 {
		return printCLIError(newNOBSError(NOBSErrInvalidArgs, "vault needs a subcommand: set, rm, list, path or default"))
	}
	settings, err := loadVaultSettings()
	if err != nil {
		return printCLIError(err)
	}
	switch command, args := args[0], args[1:]; command {
	case "set":
		if len(args) != 2 {
			return printCLIError(newNOBSError(NOBSErrInvalidArgs, "vault set requires NAME and PATH"))
		}
		folder, err := checkVaultFolder(args[1])
		if err != nil {
			return printCLIError(err)
		}
		if err := setVault(&settings, args[0], folder); err != nil {
			return printCLIError(err)
		}
		vault := settings.Vaults[args[0]]
		if err := startBroker(args[0], settings); err != nil {
			return printCLIError(err)
		}
		if err := saveVaultSettings(settings); err != nil {
			return printCLIError(err)
		}
		return printJSON(vaultListItem{Name: args[0], Path: vault.Path, Port: vault.Port, Running: brokerIsLive(vault.Port)})
	case "rm":
		if len(args) != 1 {
			return printCLIError(newNOBSError(NOBSErrInvalidArgs, "vault rm requires NAME"))
		}
		if _, ok := settings.Vaults[args[0]]; !ok {
			return printCLIError(unknownVaultError(args[0]))
		}
		if err := runCompose(args[0], settings, "down"); err != nil {
			return printCLIError(err)
		}
		delete(settings.Vaults, args[0])
		if settings.Default == args[0] {
			settings.Default = ""
		}
		if err := saveVaultSettings(settings); err != nil {
			return printCLIError(err)
		}
		return 0
	case "list":
		if len(args) != 0 {
			return printCLIError(newNOBSError(NOBSErrInvalidArgs, "vault list takes no arguments"))
		}
		items := make([]vaultListItem, 0, len(settings.Vaults))
		for name, vault := range settings.Vaults {
			items = append(items, vaultListItem{Name: name, Path: vault.Path, Port: vault.Port, Running: brokerIsLive(vault.Port)})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		return printJSON(items)
	case "path":
		if len(args) > 1 {
			return printCLIError(newNOBSError(NOBSErrInvalidArgs, "vault path takes at most one NAME"))
		}
		name := settings.Default
		if len(args) == 1 {
			name = args[0]
		}
		if name == "" {
			return printCLIError(newNOBSError(NOBSErrInvalidArgs, "no default vault; pass a NAME or run nobs vault default NAME"))
		}
		vault, ok := settings.Vaults[name]
		if !ok {
			return printCLIError(unknownVaultError(name))
		}
		fmt.Fprintln(os.Stdout, vault.Path)
		return 0
	case "default":
		switch len(args) {
		case 0:
			if settings.Default == "" {
				return printCLIError(newNOBSError(NOBSErrInvalidArgs, "no default vault; run nobs vault default NAME"))
			}
			fmt.Fprintln(os.Stdout, settings.Default)
			return 0
		case 1:
			if _, ok := settings.Vaults[args[0]]; !ok {
				return printCLIError(unknownVaultError(args[0]))
			}
			settings.Default = args[0]
			if err := saveVaultSettings(settings); err != nil {
				return printCLIError(err)
			}
			fmt.Fprintln(os.Stdout, settings.Default)
			return 0
		default:
			return printCLIError(newNOBSError(NOBSErrInvalidArgs, "vault default takes at most one NAME"))
		}
	default:
		return printCLIError(newNOBSError(NOBSErrInvalidArgs, "unknown vault command: %s", command))
	}
}

// Runs nobs tls insecure [on|off] and returns the exit code. With no on or
// off it prints the setting. on and off save it first, then run docker compose
// up -d for every vault, which replaces each running broker so the change
// takes effect now. A broker that cannot start, for example because its drive
// is not mounted, is reported and the command exits nonzero. If that broker is
// still running it keeps its old environment, because nobs starts a broker
// again only when it refuses connections, so run the command again once the
// broker can start.
func runTLS(args []string) int {
	if len(args) == 0 || args[0] != "insecure" || len(args) > 2 {
		return printCLIError(newNOBSError(NOBSErrInvalidArgs, "usage: nobs tls insecure [on|off]"))
	}
	settings, err := loadVaultSettings()
	if err != nil {
		return printCLIError(err)
	}
	code := 0
	if len(args) == 2 {
		switch args[1] {
		case "on", "off":
			settings.InsecureTLS = args[1] == "on"
		default:
			return printCLIError(newNOBSError(NOBSErrInvalidArgs, "tls insecure takes on or off, not %s", args[1]))
		}
		if err := saveVaultSettings(settings); err != nil {
			return printCLIError(err)
		}
		for _, name := range slices.Sorted(maps.Keys(settings.Vaults)) {
			if err := startBroker(name, settings); err != nil {
				code = printCLIError(newNOBSError(NOBSErrBrokerUnavailable, "vault %s was not restarted: %v; run nobs tls insecure %s again once it can start", name, err, args[1]))
			}
		}
	}
	if settings.InsecureTLS {
		fmt.Fprintln(os.Stdout, "on")
	} else {
		fmt.Fprintln(os.Stdout, "off")
	}
	return code
}
