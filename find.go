package main

import (
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

type findRequest struct {
	Ref string `json:"ref"`
}

// Printed by nobs find. Agent prompts outside this repository parse these
// field names, so renaming one breaks them.
type findResponse struct {
	Status  string   `json:"status"` // found, ambiguous, or not_found
	Path    string   `json:"path,omitempty"`
	Matches []string `json:"matches,omitempty"`
}

// Returns the full path of the note an agent means by issue code, file name,
// vault path or title. Three rules run in order, and the first that matches
// anything decides:
//  1. exact file name or vault-relative path, with or without .md
//  2. case-insensitive prefix ending at a space, hyphen, or underscore,
//     so ABC-04 does not match ABC-042
//  3. title match that ignores case, punctuation, and a leading issue code
//
// Rules 2 and 3 skip .backup- notes. Workstation vaults are git repositories,
// so the vault is checked with checkVaultMounted, which writes nothing, instead
// of readyHealth.
func handleFind(req findRequest) (findResponse, error) {
	ref := cleanFindRef(req.Ref)
	if ref == "" {
		return findResponse{}, newNOBSError(NOBSErrInvalidArgs, "find reference is required")
	}
	if err := checkVaultMounted(); err != nil {
		return findResponse{}, err
	}
	refTitle := titleKey(ref)
	var exactMatches, prefixMatches, titleMatches []string
	err := walkVaultNotes(func(fullPath, relPath string) error {
		base := path.Base(relPath)
		stem := strings.TrimSuffix(base, ".md")
		switch {
		case ref == base || ref == stem || ref == relPath || ref == strings.TrimSuffix(relPath, ".md"):
			exactMatches = append(exactMatches, fullPath)
		case isBackupNote(base):
			// Backup copies share the note's name; matching them by prefix or
			// title would make most lookups ambiguous.
		case hasWordPrefix(ref, base):
			prefixMatches = append(prefixMatches, fullPath)
		case refTitle != "" && titleKey(stem) == refTitle:
			titleMatches = append(titleMatches, fullPath)
		}
		return nil
	})
	if err != nil {
		return findResponse{}, newNOBSError(NOBSErrUnexpected, err.Error())
	}
	for _, matches := range [][]string{exactMatches, prefixMatches, titleMatches} {
		switch len(matches) {
		case 0:
			continue
		case 1:
			return findResponse{Status: "found", Path: matches[0]}, nil
		default:
			sort.Strings(matches)
			return findResponse{Status: "ambiguous", Matches: matches}, nil
		}
	}
	return findResponse{Status: "not_found"}, nil
}

func cleanFindRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(ref))
}

func isBackupNote(name string) bool {
	return strings.Contains(strings.ToLower(name), ".backup-")
}

// True when name starts with ref followed by a space, hyphen, or underscore,
// ignoring case and each value's extension (the text after its last dot).
func hasWordPrefix(ref, name string) bool {
	ref = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(ref, filepath.Ext(ref))))
	name = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(name, filepath.Ext(name))))
	if ref == "" || !strings.HasPrefix(name, ref) || len(name) <= len(ref) {
		return false
	}
	next := name[len(ref)]
	return next == ' ' || next == '-' || next == '_'
}

// Drops a first word like "ABC-113" that has letters, digits, and a hyphen.
func stripLeadingIssueCode(value string) string {
	value = strings.TrimSpace(value)
	parts := strings.SplitN(value, " ", 2)
	if len(parts) == 2 {
		head := parts[0]
		hasLetter := false
		hasDigit := false
		for _, r := range head {
			switch {
			case unicode.IsLetter(r):
				hasLetter = true
			case unicode.IsDigit(r):
				hasDigit = true
			case r == '-':
			default:
				return value
			}
		}
		if hasLetter && hasDigit && strings.ContainsRune(head, '-') {
			return parts[1]
		}
	}
	return value
}

// Lowercases value, drops its extension, and keeps only letters and digits,
// with single spaces between runs.
func wordsOnly(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(value, filepath.Ext(value))))
	if value == "" {
		return ""
	}

	var b strings.Builder
	lastSpace := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

// The form rule 3 compares: words only, without a leading issue code.
func titleKey(value string) string {
	return wordsOnly(stripLeadingIssueCode(value))
}
