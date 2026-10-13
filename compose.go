package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// The Compose file for one vault's broker, given to docker compose on stdin.
// runCompose sets every ${...} value in the environment, so a $ in a folder
// name is kept as it is. What each setting protects:
//   - The vault is mounted at the same path as on the computer, because ob,
//     the Obsidian Sync client, finds a vault's sync setup by its folder path.
//   - create_host_path: false makes the start fail when the drive is not
//     mounted, instead of Docker creating an empty folder that a sync fills.
//   - The port is published on 127.0.0.1 only, because the broker has no
//     password.
//   - NOBS_VAULT_IS_MOUNTED keeps the broker from using a folder that has no
//     .obsidian, which is how an unmounted drive looks.
//   - The container runs as the user who ran nobs, so new files belong to
//     them. HOME is /tmp, which any user can write, since the image may have
//     no home folder for that user.
const composeFile = `services:
  nobs:
    build: https://github.com/henrico813/nobs.git#${NOBS_VERSION}
    image: nobs:${NOBS_VERSION}
    user: "${NOBS_UID}:${NOBS_GID}"
    environment:
      OBSIDIAN_VAULT_DIR: ${NOBS_VAULT_PATH}
      NOBS_VAULT_IS_MOUNTED: "true"
      HOME: /tmp
    ports: ["127.0.0.1:${NOBS_PORT}:8080"]
    volumes:
      - type: bind
        source: ${NOBS_VAULT_PATH}
        target: ${NOBS_VAULT_PATH}
        bind:
          create_host_path: false
      - type: bind
        source: ${NOBS_OB_CONFIG}
        target: /config/xdg/obsidian-headless
        bind:
          create_host_path: false
    restart: unless-stopped
`

// How long startBroker waits for a started broker to answer. Tests shorten it.
var brokerStartWait = 10 * time.Second

var releaseVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// Returns the broker image's version, which is also the git tag it is built
// from. A binary from go install github.com/henrico813/nobs@v1.2.3 reports
// v1.2.3, so it starts a broker of its own version. NOBS_VERSION overrides it,
// for a local build or an image built by hand.
func brokerVersion() (string, error) {
	if version := strings.TrimSpace(os.Getenv("NOBS_VERSION")); version != "" {
		return version, nil
	}
	if info, ok := debug.ReadBuildInfo(); ok && releaseVersion.MatchString(info.Main.Version) {
		return info.Main.Version, nil
	}
	return "", newNOBSError(NOBSErrBrokerUnavailable, "this nobs is not a release, so it does not know which broker image to run; install one with go install github.com/henrico813/nobs@VERSION, or set NOBS_VERSION to the tag of an image you built")
}

// Runs docker compose for the named vault's broker (project nobs-NAME) with
// composeFile. Docker's output goes to stderr, so a person sees an image build
// as it runs and Docker's reason when it fails, such as a port in use or a
// failed build.
func runCompose(name string, vault vaultEntry, args ...string) error {
	version, err := brokerVersion()
	if err != nil {
		return err
	}
	settings, err := settingsPath()
	if err != nil {
		return err
	}
	cmd := exec.Command("docker", append([]string{"compose", "-p", "nobs-" + name, "-f", "-"}, args...)...)
	cmd.Stdin = strings.NewReader(composeFile)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	// nobs passes the user and group IDs because the shell does not export UID.
	// ob keeps its settings in the same config folder as nobs.
	cmd.Env = append(os.Environ(),
		"NOBS_VERSION="+version,
		"NOBS_UID="+strconv.Itoa(os.Getuid()),
		"NOBS_GID="+strconv.Itoa(os.Getgid()),
		"NOBS_VAULT_PATH="+vault.Path,
		"NOBS_PORT="+strconv.Itoa(vault.Port),
		"NOBS_OB_CONFIG="+filepath.Join(filepath.Dir(filepath.Dir(settings)), "obsidian-headless"),
	)
	err = cmd.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return newNOBSError(NOBSErrBrokerUnavailable, "docker was not found; vault brokers on this computer need Docker")
	}
	if err != nil {
		return newNOBSError(NOBSErrBrokerUnavailable, "docker compose %s for vault %s failed (%v); Docker's output is above", args[0], name, err)
	}
	return nil
}

// Starts the vault's broker with docker compose up -d, then waits up to
// brokerStartWait for it to answer. Up -d leaves a running broker with the
// same settings alone and replaces one whose settings changed. A broker that
// starts but does not answer in time is not an error here; the caller's next
// request reports why.
func startBroker(name string, vault vaultEntry) error {
	if _, err := checkVaultFolder(vault.Path); err != nil {
		return err
	}
	if err := runCompose(name, vault, "up", "-d"); err != nil {
		return err
	}
	for deadline := time.Now().Add(brokerStartWait); !brokerIsLive(vault.Port) && time.Now().Before(deadline); {
		time.Sleep(250 * time.Millisecond)
	}
	return nil
}

// True when a broker on 127.0.0.1:port answers /livez, which writes nothing to
// the vault.
func brokerIsLive(port int) bool {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/livez", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
