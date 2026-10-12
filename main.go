package main

import (
	"fmt"
	"os"
)

// The --vault value for the current command. runMain sets it on every call, to
// "" without the flag, so one call's flag never reaches the next.
var cliVault string

// Help text for nobs with no command, help, -h, and --help.
func usage() string {
	return `usage:
  nobs service
  nobs vault set NAME PATH
  nobs vault rm NAME
  nobs vault list
  nobs vault path [NAME]
  nobs vault default [NAME]
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

Commands other than service and vault take --vault NAME first, as in
nobs --vault work find REF. They use the broker for that vault, else
NOBS_BROKER_URL, else the default vault.
`
}

// Returns the exit code for one nobs command. Except for service, commands
// parse arguments here and send the vault work to the broker.
func runMain(args []string) int {
	cliVault = ""
	if len(args) > 0 && args[0] == "--vault" {
		if len(args) < 2 || args[1] == "" {
			return printCLIError(newNOBSError(NOBSErrInvalidArgs, "--vault requires a vault name"))
		}
		cliVault, args = args[1], args[2:]
	}
	if cliVault != "" && len(args) > 0 && (args[0] == "service" || args[0] == "vault") {
		return printCLIError(newNOBSError(NOBSErrInvalidArgs, "--vault does not apply to nobs %s", args[0]))
	}
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
	case "vault":
		return runVault(args[1:])
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
