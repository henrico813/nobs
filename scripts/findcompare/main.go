// Command findcompare checks that nobs find picks the same notes as
// pde vault locate, the lookup command in the user's personal dev tool that
// agents use today to open plans by issue code, file name or title. pde vault
// is being removed; if the two disagree, an agent would open the wrong plan or
// report a real one as missing. It is run by hand and is not part of go test.
//
// For each vault it starts a nobs service (the HTTP broker that answers find)
// outside Docker on a free local port, then asks both tools about the issue
// code, file name and title of every issue note in the given project folders.
// It sends the broker only /find requests, because the health, search and
// sync routes write a .nobs-write-probe file into the vault.
//
// Example:
//
//	go build -o /tmp/nobs .
//	go run ./scripts/findcompare -nobs /tmp/nobs \
//	  -vault "main=/path/to/vault" \
//	  -projects "Projects/DevEnv"
//
// SELECTOR is the name pde vault locate --vault uses for that vault. Project
// folders are relative to each vault; a missing one is skipped.
//
// Exit codes:
//
//	0  no differences, or only matches locate found in a hidden folder such as
//	   .trash, which find skips on purpose
//	1  at least one other difference
//	2  setup error: bad flags, pde pointing at another folder, a vault with no
//	   issue notes, or a broker that will not start
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// One tool's answer for one reference.
type lookup struct {
	Status  string   `json:"status"`
	Path    string   `json:"path,omitempty"`
	Matches []string `json:"matches,omitempty"`
	Error   string   `json:"error,omitempty"`
}

func (l lookup) paths() []string {
	if l.Path != "" {
		return []string{l.Path}
	}
	return l.Matches
}

type outcome int

const (
	outcomeSame outcome = iota
	outcomeHidden
	outcomeReal
)

type vault struct {
	selector string
	path     string
}

// Matches file names such as "ABC-110 Move config.md".
var issueName = regexp.MustCompile(`^([A-Za-z]+-[0-9]+) (.+)\.md$`)

func main() {
	nobsPath := flag.String("nobs", "", "path to a nobs binary built from the branch under test")
	var vaults []vault
	var projects []string
	flag.Func("vault", "SELECTOR=PATH; repeat for each vault", func(value string) error {
		selector, path, ok := strings.Cut(value, "=")
		if !ok || selector == "" || !filepath.IsAbs(path) {
			return errors.New("want SELECTOR=/absolute/vault/path")
		}
		vaults = append(vaults, vault{selector: selector, path: filepath.Clean(path)})
		return nil
	})
	flag.Func("projects", "project folder relative to each vault; repeatable", func(value string) error {
		projects = append(projects, value)
		return nil
	})
	flag.Parse()
	if *nobsPath == "" || len(vaults) == 0 || len(projects) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	differences := 0
	for _, v := range vaults {
		n, err := compareVault(*nobsPath, v, projects)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", v.selector, err)
			os.Exit(2)
		}
		differences += n
	}
	if differences > 0 {
		os.Exit(1)
	}
}

// Returns how many references got different answers that a hidden folder does
// not explain.
func compareVault(nobsPath string, v vault, projects []string) (int, error) {
	// locate must search the same folder the broker serves.
	out, err := exec.Command("pde", "vault", "path", v.selector).Output()
	if err != nil {
		return 0, fmt.Errorf("pde vault path: %w", err)
	}
	if got := strings.TrimSpace(string(out)); got != v.path {
		return 0, fmt.Errorf("pde vault path %s is %q, not %q", v.selector, got, v.path)
	}
	refs, err := issueReferences(v.path, projects)
	if err != nil {
		return 0, err
	}
	if len(refs) == 0 {
		return 0, errors.New("no issue notes found; is the vault mounted?")
	}
	brokerURL, stop, err := startBroker(nobsPath, v.path)
	if err != nil {
		return 0, err
	}
	defer stop()

	findEnv := append(os.Environ(), "NOBS_BROKER_URL="+brokerURL)
	var sameCount, hiddenCount, realCount int
	var slowest time.Duration
	for _, ref := range refs {
		started := time.Now()
		fromFind, err := runLookup(findEnv, nobsPath, "find", "--json", ref)
		slowest = max(slowest, time.Since(started))
		if err != nil {
			return 0, err
		}
		fromLocate, err := runLookup(os.Environ(), "pde", "vault", "locate", "--json", "--vault", v.selector, ref)
		if err != nil {
			return 0, err
		}
		switch compareLookups(v.path, fromFind, fromLocate) {
		case outcomeSame:
			sameCount++
		case outcomeHidden:
			hiddenCount++
			printDifference("HIDDEN", v.selector, ref, fromFind, fromLocate)
		default:
			realCount++
			printDifference("REAL", v.selector, ref, fromFind, fromLocate)
		}
	}
	fmt.Printf("%s: %d refs, %d same, %d hidden-folder, %d real; slowest find %s\n",
		v.selector, len(refs), sameCount, hiddenCount, realCount, slowest.Round(time.Millisecond))
	return realCount, nil
}

// Returns the issue code, file name, and title of each issue note, which
// exercise find's three match rules.
func issueReferences(vaultPath string, projects []string) ([]string, error) {
	var refs []string
	for _, project := range projects {
		entries, err := os.ReadDir(filepath.Join(vaultPath, project))
		if errors.Is(err, os.ErrNotExist) {
			fmt.Printf("skip %s: no %s\n", vaultPath, project)
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			parts := issueName.FindStringSubmatch(entry.Name())
			if entry.IsDir() || parts == nil {
				continue
			}
			refs = append(refs, parts[1], strings.TrimSuffix(entry.Name(), ".md"), parts[2])
		}
	}
	slices.Sort(refs)
	return slices.Compact(refs), nil
}

// Runs nobs service for one vault and waits until it answers.
func startBroker(nobsPath, vaultPath string) (string, func(), error) {
	address, err := freeLocalAddress()
	if err != nil {
		return "", nil, err
	}
	cmd := exec.Command(nobsPath, "service")
	// NOBS_VAULT_IS_MOUNTED=true makes find fail when .obsidian is missing, so
	// an unmounted NAS share shows as errors, not as notes that are not found.
	cmd.Env = append(os.Environ(), "NOBS_LISTEN="+address, "OBSIDIAN_VAULT_DIR="+vaultPath, "NOBS_VAULT_IS_MOUNTED=true")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return "", nil, err
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	stop := func() {
		_ = cmd.Process.Kill()
		<-exited
	}
	brokerURL := "http://" + address
	if err := waitForBroker(brokerURL, exited); err != nil {
		stop()
		return "", nil, fmt.Errorf("broker for %s: %w", vaultPath, err)
	}
	return brokerURL, stop, nil
}

// Returns a 127.0.0.1 address with a port that was free a moment ago.
func freeLocalAddress() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer listener.Close()
	return listener.Addr().String(), nil
}

// Polls brokerURL until it answers, failing early if the broker process exits.
// GET /find is rejected with 400 before the vault is read, so any HTTP answer
// means the broker is listening. The route takes the read lock first, so this
// can wait for a running sync.
func waitForBroker(brokerURL string, exited <-chan struct{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, brokerURL+"/find", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			break
		}
		select {
		case <-exited:
			return errors.New("nobs service exited early")
		case <-ctx.Done():
			return errors.New("no answer within 30 seconds")
		case <-time.After(200 * time.Millisecond):
		}
	}
	// If another process held the port, that process answered, and our broker
	// has failed to listen and exited.
	select {
	case <-exited:
		return errors.New("nobs service exited; is the port in use?")
	case <-time.After(500 * time.Millisecond):
		return nil
	}
}

// Runs one tool for one reference. A nonzero exit becomes a lookup with status
// "error" and the tool's stderr, so it is reported as a difference. It returns
// an error only when the tool cannot run or prints output that is not JSON.
func runLookup(env []string, name string, args ...string) (lookup, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return lookup{Status: "error", Error: strings.TrimSpace(stderr.String())}, nil
	}
	if err != nil {
		return lookup{}, fmt.Errorf("%s: %w", name, err)
	}
	var result lookup
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return lookup{}, fmt.Errorf("%s %s printed %q: %w", name, strings.Join(args, " "), stdout.String(), err)
	}
	return result, nil
}

// Returns outcomeSame when both tools give the same paths, outcomeHidden when
// they match once locate's paths in hidden folders are dropped (find skips
// those on purpose), and outcomeReal otherwise, including when either tool
// failed. Both tools sort their matches, so the lists compare in order, and
// find never returns a path in a hidden folder.
func compareLookups(vaultPath string, fromFind, fromLocate lookup) outcome {
	if fromFind.Status == "error" || fromLocate.Status == "error" {
		return outcomeReal
	}
	if slices.Equal(fromFind.paths(), fromLocate.paths()) {
		return outcomeSame
	}
	visible := slices.DeleteFunc(slices.Clone(fromLocate.paths()), func(p string) bool {
		return inHiddenFolder(vaultPath, p)
	})
	if slices.Equal(fromFind.paths(), visible) {
		return outcomeHidden
	}
	return outcomeReal
}

// True when path is inside a folder under the vault whose name starts with a
// dot. A path outside the vault is not hidden.
func inHiddenFolder(vaultPath, path string) bool {
	rel, err := filepath.Rel(vaultPath, path)
	if err != nil || !filepath.IsLocal(rel) {
		return false
	}
	dirs := strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/")
	return slices.ContainsFunc(dirs, func(dir string) bool { return strings.HasPrefix(dir, ".") && dir != "." })
}

func printDifference(kind, selector, ref string, fromFind, fromLocate lookup) {
	f, _ := json.Marshal(fromFind)
	l, _ := json.Marshal(fromLocate)
	fmt.Printf("%s %s %q\n  find:   %s\n  locate: %s\n", kind, selector, ref, f, l)
}
