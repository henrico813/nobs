package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

type syncStatusResponse struct {
	Configured  bool     `json:"configured"`
	Status      string   `json:"status"`
	Detail      string   `json:"detail,omitempty"`
	DetailLines []string `json:"detail_lines,omitempty"`
}

func newSyncStatusResponse(configured bool, status string, detail string) syncStatusResponse {
	resp := syncStatusResponse{Configured: configured, Status: status}
	trimmed := strings.TrimSpace(detail)
	if trimmed == "" {
		return resp
	}
	if strings.Contains(trimmed, "\n") {
		resp.DetailLines = strings.Split(trimmed, "\n")
		return resp
	}
	resp.Detail = trimmed
	return resp
}

// liveHealth verifies the vault service can answer sync commands before traffic reaches it.
func liveHealth() error {
	if err := readyHealth(); err != nil {
		return err
	}
	if _, err := exec.LookPath("ob"); err != nil {
		return newNOBSError(NOBSErrBrokerUnavailable, "ob binary unavailable")
	}
	return nil
}

func syncStatus() (syncStatusResponse, error) {
	if err := liveHealth(); err != nil {
		return syncStatusResponse{}, err
	}
	output, err := runOb("sync-status", "--path", vaultDir())
	if err != nil {
		message := err.Error()
		if looksUnconfigured(message) {
			return newSyncStatusResponse(false, "not-configured", message), nil
		}
		return syncStatusResponse{}, err
	}
	return newSyncStatusResponse(true, "ok", output), nil
}

// syncNow remains explicit and blocking so note mutations stay serialized.
func syncNow() (syncStatusResponse, error) {
	status, err := syncStatus()
	if err != nil {
		return syncStatusResponse{}, err
	}
	if !status.Configured {
		return syncStatusResponse{}, newNOBSError(
			NOBSErrSyncUnavailable,
			"obsidian sync is not configured",
		)
	}
	output, err := runOb("sync", "--path", vaultDir())
	if err != nil {
		return syncStatusResponse{}, err
	}
	return newSyncStatusResponse(true, "ok", output), nil
}

func syncConfigDir() string {
	if dir := strings.TrimSpace(os.Getenv("OBSIDIAN_SYNC_CONFIG_DIR")); dir != "" {
		return dir
	}
	return ".obsidian"
}

// Matches ob sync-status's missing-enrollment message; other ob errors must reach the caller.
func looksUnconfigured(message string) bool {
	value := strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(value, "no sync configuration found for ")
}

// runOb is the only shell-out path so sync behavior stays easy to audit.
func runOb(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ob", args...)
	cmd.Dir = vaultDir()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", newNOBSError(NOBSErrSyncUnavailable, "obsidian sync timed out")
		}
		if message == "" {
			message = err.Error()
		}
		return "", newNOBSError(NOBSErrSyncUnavailable, message)
	}
	if stdout.Len() > 0 {
		return stdout.String(), nil
	}
	return stderr.String(), nil
}
