package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type patchResponse struct {
	Path      string `json:"path"`
	Operation string `json:"operation"`
	Hunks     int    `json:"hunks"`
}

type patchInfo struct {
	Path  string
	Hunks int
}

func handleRG(req searchRequest) (searchResponse, error) {
	if err := readyHealth(); err != nil {
		return searchResponse{}, err
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return searchResponse{}, newNOBSError(NOBSErrInvalidArgs, "rg query is required")
	}
	if _, err := exec.LookPath("rg"); err != nil {
		return searchResponse{}, newNOBSError(NOBSErrBrokerUnavailable, "rg binary unavailable")
	}
	stdout, stderr, err := runVaultCommand(15*time.Second, "", "rg", "--null", "--line-number", "--color", "never", "--no-heading", "--glob", "*.md", "-e", query, ".")
	if err != nil {
		var nobsErr *NOBSError
		if errors.As(err, &nobsErr) {
			return searchResponse{}, err
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && strings.TrimSpace(stderr) == "" {
			return searchResponse{Matches: []searchMatch{}}, nil
		}
		message := strings.TrimSpace(stderr)
		if message == "" {
			message = strings.TrimSpace(stdout)
		}
		if message == "" {
			message = err.Error()
		}
		return searchResponse{}, newNOBSError(NOBSErrUnexpected, message)
	}
	return parseRGOutput(stdout)
}

func handleApplyPatch(req patchRequest) (patchResponse, error) {
	if err := readyHealth(); err != nil {
		return patchResponse{}, err
	}
	info, err := inspectPatch(req.Patch)
	if err != nil {
		return patchResponse{}, err
	}
	if _, _, err := resolveExistingMarkdown(info.Path); err != nil {
		return patchResponse{}, err
	}
	if _, err := exec.LookPath("git"); err != nil {
		return patchResponse{}, newNOBSError(NOBSErrBrokerUnavailable, "git binary unavailable")
	}
	stdout, stderr, err := runVaultCommand(30*time.Second, req.Patch, "git", "apply", "--check", "--recount", "-")
	if err != nil {
		message := strings.TrimSpace(stderr)
		if message == "" {
			message = strings.TrimSpace(stdout)
		}
		if message == "" {
			message = err.Error()
		}
		return patchResponse{}, newNOBSError(NOBSErrInvalidArgs, message)
	}
	stdout, stderr, err = runVaultCommand(30*time.Second, req.Patch, "git", "apply", "--recount", "-")
	if err != nil {
		message := strings.TrimSpace(stderr)
		if message == "" {
			message = strings.TrimSpace(stdout)
		}
		if message == "" {
			message = err.Error()
		}
		return patchResponse{}, newNOBSError(NOBSErrUnexpected, message)
	}
	return patchResponse{Path: info.Path, Operation: "apply-patch", Hunks: info.Hunks}, nil
}

func parseRGOutput(output string) (searchResponse, error) {
	resp := searchResponse{Matches: []searchMatch{}}
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		nul := strings.IndexByte(line, 0)
		if nul == -1 {
			return searchResponse{}, newNOBSError(NOBSErrUnexpected, "invalid rg output")
		}
		parts := strings.SplitN(line[nul+1:], ":", 2)
		if len(parts) != 2 {
			return searchResponse{}, newNOBSError(NOBSErrUnexpected, "invalid rg output")
		}
		lineNumber, err := strconv.Atoi(parts[0])
		if err != nil {
			return searchResponse{}, newNOBSError(NOBSErrUnexpected, "invalid rg line number")
		}
		path, err := cleanUserPath(strings.TrimPrefix(line[:nul], "./"))
		if err != nil {
			return searchResponse{}, err
		}
		resp.Matches = append(resp.Matches, searchMatch{Path: path, Line: lineNumber, Text: parts[1]})
	}
	if err := scanner.Err(); err != nil {
		return searchResponse{}, newNOBSError(NOBSErrUnexpected, err.Error())
	}
	return resp, nil
}

func inspectPatch(raw string) (patchInfo, error) {
	patch := strings.TrimSpace(raw)
	if patch == "" {
		return patchInfo{}, newNOBSError(NOBSErrInvalidArgs, "apply-patch diff is required on stdin")
	}
	info := patchInfo{}
	diffCount := 0
	oldPath := ""
	newPath := ""
	for _, rawLine := range strings.Split(patch, "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		switch {
		case strings.HasPrefix(line, "diff --git "):
			diffCount++
			if diffCount > 1 {
				return patchInfo{}, newNOBSError(NOBSErrForbidden, "apply-patch accepts exactly one file")
			}
		case strings.HasPrefix(line, "rename from "), strings.HasPrefix(line, "rename to "), strings.HasPrefix(line, "copy from "), strings.HasPrefix(line, "copy to "), strings.HasPrefix(line, "old mode "), strings.HasPrefix(line, "new mode "), strings.HasPrefix(line, "new file mode "), strings.HasPrefix(line, "deleted file mode "), strings.HasPrefix(line, "GIT binary patch"), strings.HasPrefix(line, "Binary files "):
			return patchInfo{}, newNOBSError(NOBSErrForbidden, "only single-file markdown edit patches are allowed")
		case strings.HasPrefix(line, "--- "):
			path, err := parseUnifiedDiffPath(line, "--- ", "a/")
			if err != nil {
				return patchInfo{}, err
			}
			oldPath = path
		case strings.HasPrefix(line, "+++ "):
			path, err := parseUnifiedDiffPath(line, "+++ ", "b/")
			if err != nil {
				return patchInfo{}, err
			}
			newPath = path
		case strings.HasPrefix(line, "@@"):
			info.Hunks++
		}
	}
	if diffCount != 1 {
		return patchInfo{}, newNOBSError(NOBSErrInvalidArgs, "patch must contain exactly one diff --git header")
	}
	if oldPath == "" || newPath == "" {
		return patchInfo{}, newNOBSError(NOBSErrInvalidArgs, "patch must contain unified diff paths")
	}
	if oldPath != newPath {
		return patchInfo{}, newNOBSError(NOBSErrForbidden, "rename and copy patches are not allowed")
	}
	if info.Hunks == 0 {
		return patchInfo{}, newNOBSError(NOBSErrInvalidArgs, "patch must contain at least one hunk")
	}
	info.Path = oldPath
	return info, nil
}

func parseUnifiedDiffPath(line string, prefix string, wantPrefix string) (string, error) {
	value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if value == "/dev/null" {
		return "", newNOBSError(NOBSErrForbidden, "patches may not create or delete notes")
	}
	if !strings.HasPrefix(value, wantPrefix) {
		return "", newNOBSError(NOBSErrInvalidArgs, "patch paths must use %s prefixes", wantPrefix)
	}
	return cleanUserPath(strings.TrimPrefix(value, wantPrefix))
}

func runVaultCommand(timeout time.Duration, stdin string, name string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = vaultDir()
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return stdout.String(), stderr.String(), newNOBSError(NOBSErrBrokerUnavailable, "%s timed out", name)
	}
	return stdout.String(), stderr.String(), err
}
