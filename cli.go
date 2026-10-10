package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

type searchRequest struct {
	Query string `json:"query"`
}

type notePathRequest struct {
	Path string `json:"path"`
}

type noteWriteRequest struct {
	Path string `json:"path"`
	Body string `json:"body"`
}

type patchRequest struct {
	Patch string `json:"patch"`
}

type noteMoveRequest struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

type dailyRequest struct {
	Date string `json:"date,omitempty"`
}

// parseSearchCLI keeps multi-word queries intact.
func parseSearchCLI(args []string) (searchRequest, error) {
	query := strings.TrimSpace(strings.Join(args, " "))
	if query == "" {
		return searchRequest{}, newNOBSError(NOBSErrInvalidArgs, "search query is required")
	}
	return searchRequest{Query: query}, nil
}

func parsePathCLI(command string, args []string) (notePathRequest, error) {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return notePathRequest{}, newNOBSError(NOBSErrInvalidArgs, "%s requires exactly one path", command)
	}
	return notePathRequest{Path: strings.TrimSpace(args[0])}, nil
}

// parseWriteCLI reads note bodies from stdin so shell wrappers stay minimal.
func parseWriteCLI(command string, args []string, stdin io.Reader) (noteWriteRequest, error) {
	pathReq, err := parsePathCLI(command, args)
	if err != nil {
		return noteWriteRequest{}, err
	}
	body, err := io.ReadAll(stdin)
	if err != nil {
		return noteWriteRequest{}, newNOBSError(NOBSErrUnexpected, err.Error())
	}
	if strings.TrimSpace(string(body)) == "" {
		return noteWriteRequest{}, newNOBSError(NOBSErrInvalidArgs, "%s body is required on stdin", command)
	}
	return noteWriteRequest{Path: pathReq.Path, Body: string(body)}, nil
}

func parsePatchCLI(args []string, stdin io.Reader) (patchRequest, error) {
	if len(args) != 0 {
		return patchRequest{}, newNOBSError(NOBSErrInvalidArgs, "apply-patch reads unified diff content from stdin")
	}
	body, err := io.ReadAll(stdin)
	if err != nil {
		return patchRequest{}, newNOBSError(NOBSErrUnexpected, err.Error())
	}
	if strings.TrimSpace(string(body)) == "" {
		return patchRequest{}, newNOBSError(NOBSErrInvalidArgs, "apply-patch diff is required on stdin")
	}
	return patchRequest{Patch: string(body)}, nil
}

func parseMoveCLI(args []string) (noteMoveRequest, error) {
	if len(args) != 2 {
		return noteMoveRequest{}, newNOBSError(NOBSErrInvalidArgs, "move requires source and destination paths")
	}
	source := strings.TrimSpace(args[0])
	destination := strings.TrimSpace(args[1])
	if source == "" || destination == "" {
		return noteMoveRequest{}, newNOBSError(NOBSErrInvalidArgs, "move requires source and destination paths")
	}
	return noteMoveRequest{Source: source, Destination: destination}, nil
}

func parseDailyCLI(args []string) (dailyRequest, error) {
	if len(args) > 1 {
		return dailyRequest{}, newNOBSError(NOBSErrInvalidArgs, "daily accepts zero or one date")
	}
	if len(args) == 0 {
		return dailyRequest{}, nil
	}
	date := strings.TrimSpace(args[0])
	if date == "" {
		return dailyRequest{}, newNOBSError(NOBSErrInvalidArgs, "daily date must not be empty")
	}
	return dailyRequest{Date: date}, nil
}

func printJSON(value any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return printCLIError(newNOBSError(NOBSErrUnexpected, err.Error()))
	}
	return 0
}

func printCLIError(err error) int {
	var nobsErr *NOBSError
	if errors.As(err, &nobsErr) {
		fmt.Fprintln(os.Stderr, nobsErr.Error())
		return int(nobsErr.Code)
	}
	fmt.Fprintln(os.Stderr, err)
	return int(NOBSErrUnexpected)
}
