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
	ctx, cancel := context.WithTimeout(context.Background(), syncRequestTimeout)
	defer cancel()
	return syncStatusContext(ctx)
}

// Takes the caller's deadline so syncNow can give both of its ob calls one
// shared deadline.
func syncStatusContext(ctx context.Context) (syncStatusResponse, error) {
	if err := liveHealth(); err != nil {
		return syncStatusResponse{}, err
	}
	output, err := runOb(ctx, "sync-status", "--path", vaultDir())
	if err != nil {
		message := err.Error()
		if looksUnconfigured(message) {
			return newSyncStatusResponse(false, "not-configured", message), nil
		}
		return syncStatusResponse{}, err
	}
	return newSyncStatusResponse(true, "ok", output), nil
}

// Blocks until ob has uploaded and downloaded vault changes. The status check
// and the sync share one syncRequestTimeout deadline.
func syncNow() (syncStatusResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), syncRequestTimeout)
	defer cancel()
	status, err := syncStatusContext(ctx)
	if err != nil {
		return syncStatusResponse{}, err
	}
	if !status.Configured {
		return syncStatusResponse{}, newNOBSError(
			NOBSErrSyncUnavailable,
			"obsidian sync is not configured",
		)
	}
	output, err := runOb(ctx, "sync", "--path", vaultDir())
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

// A whole sync-status or sync-now request, across all of its ob calls, must
// finish within this time or ob is stopped.
const syncRequestTimeout = 2 * time.Minute

// A process that ob starts can keep ob's output open after ob exits or is
// stopped. After this delay runOb stops waiting for the output and returns an
// error.
const obWaitDelay = 5 * time.Second

// Stops ob when ctx ends, so every ob call obeys its caller's deadline.
func runOb(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "ob", args...)
	cmd.WaitDelay = obWaitDelay
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
