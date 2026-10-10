package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// nowFunc keeps time-dependent vault behavior deterministic in tests.
var nowFunc = time.Now

type searchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type searchResponse struct {
	Matches []searchMatch `json:"matches"`
}

type noteResponse struct {
	Path           string `json:"path"`
	Body           string `json:"body,omitempty"`
	Operation      string `json:"operation,omitempty"`
	Bytes          int    `json:"bytes,omitempty"`
	Destination    string `json:"destination,omitempty"`
	TrashPath      string `json:"trash_path,omitempty"`
	AlreadyPresent *bool  `json:"already_present,omitempty"`
}

var errSearchLimit = errors.New("search limit reached")

func vaultDir() string {
	if dir := strings.TrimSpace(os.Getenv("OBSIDIAN_VAULT_DIR")); dir != "" {
		return dir
	}
	return "/vault"
}

func vaultLocation() *time.Location {
	name := strings.TrimSpace(os.Getenv("TZ"))
	if name == "" {
		name = "America/Los_Angeles"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// readyHealth proves the mounted vault exists and can accept mutations.
func readyHealth() error {
	info, err := os.Stat(vaultDir())
	if err != nil || !info.IsDir() {
		return newNOBSError(NOBSErrBrokerUnavailable, "vault directory unavailable")
	}
	mounted := strings.TrimSpace(os.Getenv("NOBS_VAULT_IS_MOUNTED"))
	if mounted != "" {
		required, err := strconv.ParseBool(mounted)
		if err != nil {
			return newNOBSError(NOBSErrBrokerUnavailable, "NOBS_VAULT_IS_MOUNTED must be true or false")
		}
		if required {
			// A missing network mount can look like an empty folder.
			if _, err := os.Stat(filepath.Join(vaultDir(), ".obsidian")); err != nil {
				return newNOBSError(NOBSErrBrokerUnavailable, "vault is not mounted: .obsidian missing")
			}
		}
	}
	probe := filepath.Join(vaultDir(), ".nobs-write-probe")
	if err := os.WriteFile(probe, []byte("ok\n"), 0o644); err != nil {
		return newNOBSError(NOBSErrBrokerUnavailable, "vault directory is not writable")
	}
	_ = os.Remove(probe)
	return nil
}

func handleSearch(req searchRequest) (searchResponse, error) {
	if err := readyHealth(); err != nil {
		return searchResponse{}, err
	}
	query := strings.ToLower(strings.TrimSpace(req.Query))
	if query == "" {
		return searchResponse{}, newNOBSError(NOBSErrInvalidArgs, "search query is required")
	}
	resp := searchResponse{Matches: make([]searchMatch, 0, 16)}
	err := filepath.WalkDir(vaultDir(), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() && strings.HasPrefix(name, ".") {
			if path == vaultDir() {
				return nil
			}
			return filepath.SkipDir
		}
		if d.Type()&os.ModeSymlink != 0 || d.IsDir() || filepath.Ext(name) != ".md" {
			return nil
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || !withinVault(resolved) {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(vaultDir(), path)
		if err != nil {
			_ = file.Close()
			return err
		}
		scanner := bufio.NewScanner(file)
		line := 0
		for scanner.Scan() {
			line++
			text := scanner.Text()
			if strings.Contains(strings.ToLower(text), query) {
				resp.Matches = append(resp.Matches, searchMatch{Path: filepath.ToSlash(rel), Line: line, Text: text})
				if len(resp.Matches) >= 50 {
					break
				}
			}
		}
		closeErr := file.Close()
		if err := scanner.Err(); err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if len(resp.Matches) >= 50 {
			return errSearchLimit
		}
		return nil
	})
	if err != nil && !errors.Is(err, errSearchLimit) {
		return searchResponse{}, newNOBSError(NOBSErrUnexpected, err.Error())
	}
	if len(resp.Matches) > 50 {
		resp.Matches = resp.Matches[:50]
	}
	return resp, nil
}

func handleRead(req notePathRequest) (noteResponse, error) {
	fullPath, relPath, err := resolveExistingMarkdown(req.Path)
	if err != nil {
		return noteResponse{}, err
	}
	body, err := os.ReadFile(fullPath)
	if err != nil {
		return noteResponse{}, newNOBSError(NOBSErrUnexpected, err.Error())
	}
	return noteResponse{Path: relPath, Body: string(body)}, nil
}

func handleToday() (noteResponse, error) {
	return handleDaily(dailyRequest{Date: nowFunc().In(vaultLocation()).Format("2006-01-02")})
}

func handleDaily(req dailyRequest) (noteResponse, error) {
	date := strings.TrimSpace(req.Date)
	if date == "" {
		date = nowFunc().In(vaultLocation()).Format("2006-01-02")
	}
	path, rel, err := findDailyNote(date)
	if err != nil {
		return noteResponse{}, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return noteResponse{}, newNOBSError(NOBSErrUnexpected, err.Error())
	}
	return noteResponse{Path: rel, Body: string(body)}, nil
}

// handleWrite owns generic note creation and replacement.
func handleWrite(req noteWriteRequest, appendMode bool) (noteResponse, error) {
	fullPath, relPath, err := resolveUserWritableMarkdown(req.Path)
	if err != nil {
		return noteResponse{}, err
	}
	if info, err := os.Lstat(fullPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return noteResponse{}, newNOBSError(NOBSErrForbidden, "note path is not a regular vault file")
		}
	}
	flag := os.O_CREATE | os.O_WRONLY
	op := "write"
	if appendMode {
		flag |= os.O_APPEND
		op = "append"
	} else {
		flag |= os.O_TRUNC
	}
	file, err := os.OpenFile(fullPath, flag, 0o644)
	if err != nil {
		return noteResponse{}, newNOBSError(NOBSErrUnexpected, err.Error())
	}
	defer file.Close()
	if _, err := file.WriteString(req.Body); err != nil {
		return noteResponse{}, newNOBSError(NOBSErrUnexpected, err.Error())
	}
	return noteResponse{Path: relPath, Operation: op, Bytes: len(req.Body)}, nil
}

func handleAppendNote(req noteWriteRequest) (noteResponse, error) {
	alreadyPresent := false
	requestBody := strings.ReplaceAll(req.Body, "\r\n", "\n")
	if strings.Contains(requestBody, "\r") {
		return noteResponse{}, newNOBSError(NOBSErrInvalidArgs, "append-note body contains an unsupported line ending")
	}
	requestBody = strings.TrimRight(requestBody, "\n")
	if strings.TrimSpace(requestBody) == "" {
		return noteResponse{}, newNOBSError(NOBSErrInvalidArgs, "append-note body is required")
	}
	for _, line := range strings.Split(requestBody, "\n") {
		if line == "# Notes" {
			return noteResponse{}, newNOBSError(NOBSErrInvalidArgs, "append-note body may not contain a literal # Notes line")
		}
	}

	fullPath, relPath, err := resolveExistingMarkdown(req.Path)
	if err != nil {
		return noteResponse{}, err
	}
	body, err := os.ReadFile(fullPath)
	if err != nil {
		return noteResponse{}, newNOBSError(NOBSErrUnexpected, err.Error())
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return noteResponse{}, newNOBSError(NOBSErrUnexpected, err.Error())
	}

	note := string(body)
	headingOffset := -1
	headingCount := 0
	offset := 0
	for _, line := range strings.SplitAfter(note, "\n") {
		content := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if content == "# Notes" {
			headingCount++
			if headingOffset == -1 {
				headingOffset = offset
			}
		}
		offset += len(line)
	}
	if headingCount != 1 {
		return noteResponse{}, newNOBSError(NOBSErrInvalidArgs, "note must contain exactly one literal # Notes line")
	}

	insertAt := headingOffset + len("# Notes")
	remainder := note[insertAt:]
	lineEnd := "\n"
	if strings.HasPrefix(remainder, "\r\n") {
		lineEnd = "\r\n"
	}
	divider := lineEnd + "---"
	if remainder == divider || strings.HasPrefix(remainder, divider+lineEnd) {
		insertAt += len(divider)
	}
	normalizedBody := strings.ReplaceAll(requestBody, "\n", lineEnd)
	separator := lineEnd + lineEnd
	remainder = note[insertAt:]
	wantPrefix := separator + normalizedBody
	if strings.HasPrefix(remainder, wantPrefix) &&
		(len(remainder) == len(wantPrefix) || strings.HasPrefix(remainder[len(wantPrefix):], lineEnd)) {
		alreadyPresent = true
		return noteResponse{Path: relPath, Operation: "append-note", AlreadyPresent: &alreadyPresent}, nil
	}
	updated := note[:insertAt] + wantPrefix + note[insertAt:]
	if err := atomicWriteFile(fullPath, []byte(updated), info.Mode().Perm()); err != nil {
		return noteResponse{}, newNOBSError(NOBSErrUnexpected, err.Error())
	}
	return noteResponse{Path: relPath, Operation: "append-note", Bytes: len(normalizedBody), AlreadyPresent: &alreadyPresent}, nil
}

func atomicWriteFile(path string, body []byte, mode fs.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".nobs-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer func() {
		_ = file.Close()
		_ = os.Remove(tempPath)
	}()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.Write(body); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func handleMove(req noteMoveRequest) (noteResponse, error) {
	sourceFull, sourceRel, err := resolveExistingMarkdown(req.Source)
	if err != nil {
		return noteResponse{}, err
	}
	destinationFull, destinationRel, err := resolveUserWritableMarkdown(req.Destination)
	if err != nil {
		return noteResponse{}, err
	}
	if _, err := os.Lstat(destinationFull); err == nil {
		return noteResponse{}, newNOBSError(NOBSErrForbidden, "move destination already exists")
	}
	if err := os.Rename(sourceFull, destinationFull); err != nil {
		return noteResponse{}, newNOBSError(NOBSErrUnexpected, err.Error())
	}
	return noteResponse{Path: sourceRel, Destination: destinationRel, Operation: "move"}, nil
}

// handleDelete archives notes into .trash instead of removing them permanently.
func handleDelete(req notePathRequest) (noteResponse, error) {
	sourceFull, sourceRel, err := resolveExistingMarkdown(req.Path)
	if err != nil {
		return noteResponse{}, err
	}
	trashRel, err := uniqueTrashPath(sourceRel)
	if err != nil {
		return noteResponse{}, err
	}
	trashFull, err := resolveInternalTrashMarkdown(trashRel)
	if err != nil {
		return noteResponse{}, err
	}
	if err := os.Rename(sourceFull, trashFull); err != nil {
		return noteResponse{}, newNOBSError(NOBSErrUnexpected, err.Error())
	}
	return noteResponse{Path: sourceRel, TrashPath: trashRel, Operation: "delete"}, nil
}

func findDailyNote(date string) (string, string, error) {
	var fullPath string
	err := filepath.WalkDir(vaultDir(), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != vaultDir() && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if name == date+".md" || strings.HasPrefix(name, date) && filepath.Ext(name) == ".md" {
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			fullPath = path
			return fs.SkipAll
		}
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return "", "", newNOBSError(NOBSErrUnexpected, err.Error())
	}
	if fullPath == "" {
		return "", "", newNOBSError(NOBSErrNotFound, "no daily note found for %s", date)
	}
	rel, err := filepath.Rel(vaultDir(), fullPath)
	if err != nil {
		return "", "", newNOBSError(NOBSErrUnexpected, err.Error())
	}
	return fullPath, filepath.ToSlash(rel), nil
}

func resolveExistingMarkdown(path string) (string, string, error) {
	relPath, err := cleanUserPath(path)
	if err != nil {
		return "", "", err
	}
	fullPath := filepath.Join(vaultDir(), filepath.FromSlash(relPath))
	info, err := os.Lstat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", newNOBSError(NOBSErrNotFound, "note not found: %s", relPath)
		}
		return "", "", newNOBSError(NOBSErrUnexpected, err.Error())
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", "", newNOBSError(NOBSErrForbidden, "note path is not a regular vault file")
	}
	resolved, err := filepath.EvalSymlinks(fullPath)
	if err != nil || !withinVault(resolved) {
		return "", "", newNOBSError(NOBSErrForbidden, "note escapes vault root")
	}
	return fullPath, relPath, nil
}

func resolveUserWritableMarkdown(path string) (string, string, error) {
	relPath, err := cleanUserPath(path)
	if err != nil {
		return "", "", err
	}
	fullPath := filepath.Join(vaultDir(), filepath.FromSlash(relPath))
	if err := ensureSafeParentDirs(fullPath); err != nil {
		return "", "", err
	}
	return fullPath, relPath, nil
}

func resolveInternalTrashMarkdown(path string) (string, error) {
	fullPath := filepath.Join(vaultDir(), filepath.FromSlash(path))
	if err := ensureSafeParentDirs(fullPath); err != nil {
		return "", err
	}
	return fullPath, nil
}

// cleanUserPath enforces the user-visible note boundary under the vault root.
func cleanUserPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", newNOBSError(NOBSErrInvalidPath, "note path is required")
	}
	if strings.HasPrefix(trimmed, "/") {
		return "", newNOBSError(NOBSErrInvalidPath, "absolute paths are not allowed")
	}
	cleaned := filepath.ToSlash(filepath.Clean(trimmed))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", newNOBSError(NOBSErrInvalidPath, "path escapes vault root")
	}
	if filepath.Ext(cleaned) != ".md" {
		return "", newNOBSError(NOBSErrForbidden, "only markdown notes are allowed")
	}
	for _, segment := range strings.Split(cleaned, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", newNOBSError(NOBSErrInvalidPath, "invalid note path")
		}
		if strings.HasPrefix(segment, ".") {
			return "", newNOBSError(NOBSErrForbidden, "system directories are not allowed")
		}
	}
	return cleaned, nil
}

func ensureSafeParentDirs(fullPath string) error {
	current := vaultDir()
	rel, err := filepath.Rel(vaultDir(), filepath.Dir(fullPath))
	if err != nil {
		return newNOBSError(NOBSErrUnexpected, err.Error())
	}
	for _, segment := range strings.Split(rel, string(filepath.Separator)) {
		if segment == "." || segment == "" {
			continue
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o755); err != nil {
				return newNOBSError(NOBSErrUnexpected, err.Error())
			}
			continue
		}
		if err != nil {
			return newNOBSError(NOBSErrUnexpected, err.Error())
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return newNOBSError(NOBSErrForbidden, "parent path is not a safe directory")
		}
	}
	return nil
}

func uniqueTrashPath(relPath string) (string, error) {
	base := filepath.ToSlash(filepath.Join(".trash", nowFunc().In(vaultLocation()).Format("2006-01-02"), filepath.FromSlash(relPath)))
	if _, err := os.Lstat(filepath.Join(vaultDir(), filepath.FromSlash(base))); os.IsNotExist(err) {
		return base, nil
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("%s.%d%s", stem, i, ext)
		if _, err := os.Lstat(filepath.Join(vaultDir(), filepath.FromSlash(candidate))); os.IsNotExist(err) {
			return candidate, nil
		}
	}
	return "", newNOBSError(NOBSErrUnexpected, "unable to allocate unique trash path")
}

func withinVault(path string) bool {
	root := filepath.Clean(vaultDir()) + string(filepath.Separator)
	cleaned := filepath.Clean(path)
	return cleaned == filepath.Clean(vaultDir()) || strings.HasPrefix(cleaned, root)
}
