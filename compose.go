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

// Returns composeFile, plus NODE_TLS_REJECT_UNAUTHORIZED=0 after HOME when
// insecureTLS is true. ob runs on Node, which then accepts any TLS
// certificate, such as one a network proxy re-signs. When insecureTLS is
// false the file is composeFile unchanged, so brokers stay as before.
func brokerComposeFile(insecureTLS bool) string {
	if !insecureTLS {
		return composeFile
	}
	return strings.Replace(composeFile, "      HOME: /tmp\n", "      HOME: /tmp\n      NODE_TLS_REJECT_UNAUTHORIZED: \"0\"\n", 1)
}

// Goes to stderr, so commands that print JSON on stdout stay readable.
func warnInsecureTLS() {
	fmt.Fprintln(os.Stderr, "warning: insecure TLS is on; the broker does not verify TLS certificates")
}

// How long startBroker waits for a started broker to answer. Tests shorten it.
var brokerStartWait = 10 * time.Second

var releaseVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// The release tag, such as v1.2.3. The release workflow sets it with
// go build -ldflags "-X main.version=v1.2.3"; it is empty unless a build
// stamps it.
var version string

// Returns the broker image's version, which is also the git tag it is built
// from. NOBS_VERSION wins, for a local build or an image built by hand. Then
// comes the tag stamped into a release binary, then the version Go records
// for go install github.com/henrico813/nobs@v1.2.3, so either kind of release
// starts a broker of its own version.
func brokerVersion() (string, error) {
	if override := strings.TrimSpace(os.Getenv("NOBS_VERSION")); override != "" {
		return override, nil
	}
	if version != "" {
		return version, nil
	}
	if info, ok := debug.ReadBuildInfo(); ok && releaseVersion.MatchString(info.Main.Version) {
		return info.Main.Version, nil
	}
	return "", newNOBSError(NOBSErrBrokerUnavailable, "this nobs has no release version, so it does not know which broker image to run; install a release binary from https://github.com/henrico813/nobs/releases, or run go install github.com/henrico813/nobs@VERSION, or set NOBS_VERSION to the tag of an image you built")
}

// Runs docker compose for the named vault's broker (project nobs-NAME) with
// brokerComposeFile. It takes the whole settings, not one vault, so callers
// pass this computer's TLS setting along with the vault. Docker's output goes
// to stderr, so a person sees an image build as it runs and Docker's reason
// when it fails, such as a port in use or a failed build.
func runCompose(name string, settings vaultSettings, args ...string) error {
	vault := settings.Vaults[name]
	tag, err := brokerVersion()
	if err != nil {
		return err
	}
	configPath, err := settingsPath()
	if err != nil {
		return err
	}
	cmd := exec.Command("docker", append([]string{"compose", "-p", "nobs-" + name, "-f", "-"}, args...)...)
	cmd.Stdin = strings.NewReader(brokerComposeFile(settings.InsecureTLS))
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	// nobs passes the user and group IDs because the shell does not export UID.
	// ob keeps its settings in the same config folder as nobs.
	cmd.Env = append(os.Environ(),
		"NOBS_VERSION="+tag,
		"NOBS_UID="+strconv.Itoa(os.Getuid()),
		"NOBS_GID="+strconv.Itoa(os.Getgid()),
		"NOBS_VAULT_PATH="+vault.Path,
		"NOBS_PORT="+strconv.Itoa(vault.Port),
		"NOBS_OB_CONFIG="+filepath.Join(filepath.Dir(filepath.Dir(configPath)), "obsidian-headless"),
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
// request reports why. Warns first while insecure TLS is on.
func startBroker(name string, settings vaultSettings) error {
	vault := settings.Vaults[name]
	if _, err := checkVaultFolder(vault.Path); err != nil {
		return err
	}
	if settings.InsecureTLS {
		warnInsecureTLS()
	}
	if err := runCompose(name, settings, "up", "-d"); err != nil {
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
