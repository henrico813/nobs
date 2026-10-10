package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Reads and /sync-status share the vault. Edits and /sync-now run one at a
// time after running reads finish, and new reads wait while one of them is
// running or queued.
var vaultMu sync.RWMutex

func brokerWriteTimeout() time.Duration {
	return 3 * time.Minute
}

func brokerListenAddress() string {
	if address := strings.TrimSpace(os.Getenv("NOBS_LISTEN")); address != "" {
		return address
	}
	return ":8080"
}

func runService() error {
	mux := newBrokerMux()
	server := &http.Server{Addr: brokerListenAddress(), Handler: mux, ReadTimeout: 5 * time.Second, WriteTimeout: brokerWriteTimeout(), IdleTimeout: brokerWriteTimeout()}
	return server.ListenAndServe()
}

func newBrokerMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", handleLivez)
	mux.HandleFunc("/configz", handleConfigz)
	mux.HandleFunc("/readyz", handleReadyz)
	mux.HandleFunc("/find", withReadLock(handleFindHTTP))
	mux.HandleFunc("/search", withReadLock(handleSearchHTTP))
	mux.HandleFunc("/rg", withReadLock(handleRGHTTP))
	mux.HandleFunc("/read", withReadLock(handleReadHTTP))
	mux.HandleFunc("/today", withReadLock(handleTodayHTTP))
	mux.HandleFunc("/daily", withReadLock(handleDailyHTTP))
	mux.HandleFunc("/sync-status", withReadLock(handleSyncStatusHTTP))
	mux.HandleFunc("/write", withWriteLock(handleWriteHTTP))
	mux.HandleFunc("/append", withWriteLock(handleAppendHTTP))
	mux.HandleFunc("/append-note", withWriteLock(handleAppendNoteHTTP))
	mux.HandleFunc("/apply-patch", withWriteLock(handleApplyPatchHTTP))
	mux.HandleFunc("/move", withWriteLock(handleMoveHTTP))
	mux.HandleFunc("/delete", withWriteLock(handleDeleteHTTP))
	mux.HandleFunc("/sync-now", withWriteLock(handleSyncNowHTTP))
	return mux
}

func withReadLock(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vaultMu.RLock()
		defer vaultMu.RUnlock()
		next(w, r)
	}
}

func withWriteLock(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vaultMu.Lock()
		defer vaultMu.Unlock()
		next(w, r)
	}
}

func handleLivez(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeBrokerError(w, newNOBSError(NOBSErrInvalidArgs, "method must be GET"))
		return
	}
	if err := liveHealth(); err != nil {
		writeBrokerError(w, err)
		return
	}
	writeBrokerJSON(w, map[string]any{"status": "ok"})
}

func handleConfigz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeBrokerError(w, newNOBSError(NOBSErrInvalidArgs, "method must be GET"))
		return
	}
	resp, err := syncStatus()
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	if !resp.Configured {
		w.WriteHeader(http.StatusConflict)
		writeBrokerJSON(w, resp)
		return
	}
	writeBrokerJSON(w, resp)
}

func handleReadyz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeBrokerError(w, newNOBSError(NOBSErrInvalidArgs, "method must be GET"))
		return
	}
	if err := readyHealth(); err != nil {
		writeBrokerError(w, err)
		return
	}
	writeBrokerJSON(w, map[string]any{"status": "ok"})
}

func handleFindHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBrokerError(w, newNOBSError(NOBSErrInvalidArgs, "method must be POST"))
		return
	}
	req, err := decodeBrokerJSON[findRequest](r.Body)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	resp, err := handleFind(req)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	writeBrokerJSON(w, resp)
}

func handleSearchHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBrokerError(w, newNOBSError(NOBSErrInvalidArgs, "method must be POST"))
		return
	}
	req, err := decodeBrokerJSON[searchRequest](r.Body)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	resp, err := handleSearch(req)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	writeBrokerJSON(w, resp)
}

func handleRGHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBrokerError(w, newNOBSError(NOBSErrInvalidArgs, "method must be POST"))
		return
	}
	req, err := decodeBrokerJSON[searchRequest](r.Body)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	resp, err := handleRG(req)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	writeBrokerJSON(w, resp)
}

func handleReadHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBrokerError(w, newNOBSError(NOBSErrInvalidArgs, "method must be POST"))
		return
	}
	req, err := decodeBrokerJSON[notePathRequest](r.Body)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	resp, err := handleRead(req)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	writeBrokerJSON(w, resp)
}

func handleTodayHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBrokerError(w, newNOBSError(NOBSErrInvalidArgs, "method must be POST"))
		return
	}
	if _, err := decodeBrokerJSON[struct{}](r.Body); err != nil {
		writeBrokerError(w, err)
		return
	}
	resp, err := handleToday()
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	writeBrokerJSON(w, resp)
}

func handleDailyHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBrokerError(w, newNOBSError(NOBSErrInvalidArgs, "method must be POST"))
		return
	}
	req, err := decodeBrokerJSON[dailyRequest](r.Body)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	resp, err := handleDaily(req)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	writeBrokerJSON(w, resp)
}

func handleWriteHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBrokerError(w, newNOBSError(NOBSErrInvalidArgs, "method must be POST"))
		return
	}
	req, err := decodeBrokerJSON[noteWriteRequest](r.Body)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	resp, err := handleWrite(req, false)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	writeBrokerJSON(w, resp)
}

func handleAppendHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBrokerError(w, newNOBSError(NOBSErrInvalidArgs, "method must be POST"))
		return
	}
	req, err := decodeBrokerJSON[noteWriteRequest](r.Body)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	resp, err := handleWrite(req, true)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	writeBrokerJSON(w, resp)
}

func handleAppendNoteHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBrokerError(w, newNOBSError(NOBSErrInvalidArgs, "method must be POST"))
		return
	}
	req, err := decodeBrokerJSON[noteWriteRequest](r.Body)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	resp, err := handleAppendNote(req)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	writeBrokerJSON(w, resp)
}

func handleApplyPatchHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBrokerError(w, newNOBSError(NOBSErrInvalidArgs, "method must be POST"))
		return
	}
	req, err := decodeBrokerJSON[patchRequest](r.Body)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	resp, err := handleApplyPatch(req)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	writeBrokerJSON(w, resp)
}

func handleMoveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBrokerError(w, newNOBSError(NOBSErrInvalidArgs, "method must be POST"))
		return
	}
	req, err := decodeBrokerJSON[noteMoveRequest](r.Body)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	resp, err := handleMove(req)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	writeBrokerJSON(w, resp)
}

func handleDeleteHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBrokerError(w, newNOBSError(NOBSErrInvalidArgs, "method must be POST"))
		return
	}
	req, err := decodeBrokerJSON[notePathRequest](r.Body)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	resp, err := handleDelete(req)
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	writeBrokerJSON(w, resp)
}

func handleSyncStatusHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBrokerError(w, newNOBSError(NOBSErrInvalidArgs, "method must be POST"))
		return
	}
	if _, err := decodeBrokerJSON[struct{}](r.Body); err != nil {
		writeBrokerError(w, err)
		return
	}
	resp, err := syncStatus()
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	writeBrokerJSON(w, resp)
}

func handleSyncNowHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBrokerError(w, newNOBSError(NOBSErrInvalidArgs, "method must be POST"))
		return
	}
	if _, err := decodeBrokerJSON[struct{}](r.Body); err != nil {
		writeBrokerError(w, err)
		return
	}
	resp, err := syncNow()
	if err != nil {
		writeBrokerError(w, err)
		return
	}
	writeBrokerJSON(w, resp)
}

func brokerHealth() (map[string]any, error) {
	live, err := brokerGET("/livez")
	if err != nil {
		return nil, err
	}
	configured, err := brokerGETAllowConflict("/configz")
	if err != nil {
		return nil, err
	}
	ready, err := brokerGET("/readyz")
	if err != nil {
		return nil, err
	}
	return map[string]any{"live": live, "configured": configured, "ready": ready}, nil
}

func brokerGET(path string) (map[string]any, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("NOBS_BROKER_URL")), "/")
	if baseURL == "" {
		return nil, newNOBSError(NOBSErrBrokerUnavailable, "NOBS_BROKER_URL is empty")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(baseURL + path)
	if err != nil {
		return nil, newNOBSError(NOBSErrBrokerUnavailable, err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, newNOBSError(NOBSErrBrokerUnavailable, resp.Status)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, newNOBSError(NOBSErrUnexpected, err.Error())
	}
	return out, nil
}

func brokerGETAllowConflict(path string) (map[string]any, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("NOBS_BROKER_URL")), "/")
	if baseURL == "" {
		return nil, newNOBSError(NOBSErrBrokerUnavailable, "NOBS_BROKER_URL is empty")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(baseURL + path)
	if err != nil {
		return nil, newNOBSError(NOBSErrBrokerUnavailable, err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusConflict {
		return nil, newNOBSError(NOBSErrBrokerUnavailable, resp.Status)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, newNOBSError(NOBSErrUnexpected, err.Error())
	}
	return out, nil
}

func brokerCommandTimeout(command string) time.Duration {
	switch command {
	case "sync-now":
		// Once it holds the vault lock, the broker stops a sync after
		// syncRequestTimeout plus obWaitDelay. Waiting longer lets the CLI show
		// the broker's timeout error instead of its own, unless the sync first
		// waited a long time for the lock.
		return 3 * time.Minute
	case "apply-patch":
		return 30 * time.Second
	case "find", "search", "rg", "read", "today", "daily":
		// Reads wait behind a running sync-now, which lasts up to
		// syncRequestTimeout plus obWaitDelay, and may then scan a large vault.
		// A queued sync-now or edit ahead of them adds to that wait.
		return 150 * time.Second
	default:
		return 15 * time.Second
	}
}

// brokerJSONCall keeps the CLI and broker transport generic across note operations.
func brokerJSONCall[Req any, Resp any](command string, req Req) (Resp, error) {
	var zero Resp
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("NOBS_BROKER_URL")), "/")
	if baseURL == "" {
		return zero, newNOBSError(NOBSErrBrokerUnavailable, "NOBS_BROKER_URL is empty")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return zero, newNOBSError(NOBSErrUnexpected, err.Error())
	}
	client := &http.Client{Timeout: brokerCommandTimeout(command)}
	resp, err := client.Post(baseURL+"/"+command, "application/json", bytes.NewReader(body))
	if err != nil {
		return zero, newNOBSError(NOBSErrBrokerUnavailable, err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var payload NOBSError
		if err := json.NewDecoder(resp.Body).Decode(&payload); err == nil {
			return zero, &payload
		}
		return zero, newNOBSError(NOBSErrBrokerUnavailable, resp.Status)
	}
	var out Resp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return zero, newNOBSError(NOBSErrUnexpected, err.Error())
	}
	return out, nil
}

func decodeBrokerJSON[T any](body io.Reader) (T, error) {
	var req T
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		return req, newNOBSError(NOBSErrInvalidArgs, "invalid broker json")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return req, newNOBSError(NOBSErrInvalidArgs, "invalid broker json")
	}
	return req, nil
}

func writeBrokerJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeBrokerError(w http.ResponseWriter, err error) {
	var nobsErr *NOBSError
	if errors.As(err, &nobsErr) {
		status := http.StatusBadRequest
		switch nobsErr.Code {
		case NOBSErrBrokerUnavailable, NOBSErrSyncUnavailable:
			status = http.StatusBadGateway
		case NOBSErrForbidden:
			status = http.StatusForbidden
		case NOBSErrNotFound:
			status = http.StatusNotFound
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(nobsErr)
		return
	}
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(&NOBSError{Code: NOBSErrUnexpected, Message: err.Error()})
}
