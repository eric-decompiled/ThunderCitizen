package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"thundercitizen/internal/fetch"
)

// printSources renders a numbered list of upcoming fetches.
func printSources(sources []fetch.Source) {
	if len(sources) == 0 {
		fmt.Println("(no sources to fetch)")
		return
	}
	for i, s := range sources {
		fmt.Printf("  %3d. %-44s %s\n", i+1, truncate(s.Label, 44), s.URL)
	}
	fmt.Printf("\n%d source(s) total.\n", len(sources))
}

// confirm asks "Proceed? [y/N]" on a TTY. If stdin isn't a terminal, errors
// out — this CLI is manual-use only.
func confirm() bool {
	if !isatty(os.Stdin) {
		fmt.Fprintln(os.Stderr, "error: fetcher is a manual tool — run it from a terminal, not a pipe or cron")
		os.Exit(2)
	}
	fmt.Print("Proceed? [y/N]: ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes"
}

// runInteractive shows the source picker when fetcher is invoked with no
// subcommand. Selecting a source dispatches to the regular subcommand path,
// which then runs its own discovery → confirm → fetch flow.
func runInteractive() {
	if !isatty(os.Stdin) {
		printUsage()
		os.Exit(2)
	}
	fmt.Println("Available sources:")
	fmt.Println("  1) gtfs     Thunder Bay GTFS static schedule")
	fmt.Println("  2) votes    eSCRIBE council meetings")
	fmt.Println("  3) wards    Open North ward boundaries")
	fmt.Println("  4) chunks  Rebuild transit metric chunks")
	fmt.Println("  q) quit")
	fmt.Print("\nSelect [1-4]: ")

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return
	}
	line = strings.TrimSpace(strings.ToLower(line))

	switch line {
	case "1", "gtfs":
		runGTFS()
	case "2", "votes":
		runVotes()
	case "3", "wards":
		runWards()
	case "4", "chunks":
		runChunks()
	case "q", "quit", "exit":
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown selection: %s\n", line)
		os.Exit(2)
	}
}

// truncate returns s clipped to n runes with an ellipsis if it overflowed.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// isatty returns true if f is a real terminal (not /dev/null, not a pipe).
// Uses TIOCGWINSZ via golang.org/x/term — the canonical check.
func isatty(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}
