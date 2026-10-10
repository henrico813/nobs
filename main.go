package main

import (
	"fmt"
	"os"
)

// Help text for nobs with no command, help, -h, and --help.
func usage() string {
	return `usage:
  nobs service
  nobs health
  nobs sync-status
  nobs sync-now
  nobs find REF [--json]
  nobs search QUERY
  nobs rg QUERY
  nobs read PATH
  nobs write PATH < BODY
  nobs append PATH < BODY
  nobs append-note PATH < BODY
  nobs apply-patch < PATCH
  nobs move SOURCE_PATH DESTINATION_PATH
  nobs delete PATH
  nobs today
  nobs daily [DATE]
`
}

// Returns the exit code for one nobs command. Except for service, commands
// parse arguments here and send the vault work to the broker.
func runMain(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stdout, usage())
		return 0
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(os.Stdout, usage())
		return 0
	case "service":
		if err := runService(); err != nil {
			return printCLIError(err)
		}
		return 0
	case "health":
		resp, err := brokerHealth()
		if err != nil {
			return printCLIError(err)
		}
		return printJSON(resp)
	case "sync-status":
		resp, err := brokerJSONCall[struct{}, syncStatusResponse]("sync-status", struct{}{})
		if err != nil {
			return printCLIError(err)
		}
		return printJSON(resp)
	case "sync-now":
		resp, err := brokerJSONCall[struct{}, syncStatusResponse]("sync-now", struct{}{})
		if err != nil {
			return printCLIError(err)
		}
		return printJSON(resp)
	case "find":
		req, err := parseFindCLI(args[1:])
		if err != nil {
			return printCLIError(err)
		}
		resp, err := brokerJSONCall[findRequest, findResponse]("find", req)
		if err != nil {
			return printCLIError(err)
		}
		return printJSON(resp)
	case "search":
		req, err := parseSearchCLI(args[1:])
		if err != nil {
			return printCLIError(err)
		}
		resp, err := brokerJSONCall[searchRequest, searchResponse]("search", req)
		if err != nil {
			return printCLIError(err)
		}
		return printJSON(resp)
	case "rg":
		req, err := parseSearchCLI(args[1:])
		if err != nil {
			return printCLIError(err)
		}
		resp, err := brokerJSONCall[searchRequest, searchResponse]("rg", req)
		if err != nil {
			return printCLIError(err)
		}
		return printJSON(resp)
	case "read":
		req, err := parsePathCLI("read", args[1:])
		if err != nil {
			return printCLIError(err)
		}
		resp, err := brokerJSONCall[notePathRequest, noteResponse]("read", req)
		if err != nil {
			return printCLIError(err)
		}
		return printJSON(resp)
	case "write":
		req, err := parseWriteCLI("write", args[1:], os.Stdin)
		if err != nil {
			return printCLIError(err)
		}
		resp, err := brokerJSONCall[noteWriteRequest, noteResponse]("write", req)
		if err != nil {
			return printCLIError(err)
		}
		return printJSON(resp)
	case "append":
		req, err := parseWriteCLI("append", args[1:], os.Stdin)
		if err != nil {
			return printCLIError(err)
		}
		resp, err := brokerJSONCall[noteWriteRequest, noteResponse]("append", req)
		if err != nil {
			return printCLIError(err)
		}
		return printJSON(resp)
	case "append-note":
		req, err := parseWriteCLI("append-note", args[1:], os.Stdin)
		if err != nil {
			return printCLIError(err)
		}
		resp, err := brokerJSONCall[noteWriteRequest, noteResponse]("append-note", req)
		if err != nil {
			return printCLIError(err)
		}
		return printJSON(resp)
	case "apply-patch":
		req, err := parsePatchCLI(args[1:], os.Stdin)
		if err != nil {
			return printCLIError(err)
		}
		resp, err := brokerJSONCall[patchRequest, patchResponse]("apply-patch", req)
		if err != nil {
			return printCLIError(err)
		}
		return printJSON(resp)
	case "move":
		req, err := parseMoveCLI(args[1:])
		if err != nil {
			return printCLIError(err)
		}
		resp, err := brokerJSONCall[noteMoveRequest, noteResponse]("move", req)
		if err != nil {
			return printCLIError(err)
		}
		return printJSON(resp)
	case "delete":
		req, err := parsePathCLI("delete", args[1:])
		if err != nil {
			return printCLIError(err)
		}
		resp, err := brokerJSONCall[notePathRequest, noteResponse]("delete", req)
		if err != nil {
			return printCLIError(err)
		}
		return printJSON(resp)
	case "today":
		resp, err := brokerJSONCall[struct{}, noteResponse]("today", struct{}{})
		if err != nil {
			return printCLIError(err)
		}
		return printJSON(resp)
	case "daily":
		req, err := parseDailyCLI(args[1:])
		if err != nil {
			return printCLIError(err)
		}
		resp, err := brokerJSONCall[dailyRequest, noteResponse]("daily", req)
		if err != nil {
			return printCLIError(err)
		}
		return printJSON(resp)
	default:
		return printCLIError(newNOBSError(NOBSErrInvalidArgs, "unknown command: %s", args[0]))
	}
}

func main() {
	os.Exit(runMain(os.Args[1:]))
}
