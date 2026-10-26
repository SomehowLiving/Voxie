package main

import (
	"fmt"
	"io"
	"os"

	"github.com/streamcoreai/streamcore-server/internal/callreport"
)

// runReport is `voxie report [dir or file ...]`: how calls went, from the
// call reports (default: ./reports).
func runReport(args []string, out io.Writer) int {
	if len(args) == 0 {
		args = []string{"reports"}
	}
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Fprintln(out, "usage: voxie report [dir or calls-*.jsonl ...]   (default: reports)")
			return 0
		}
	}
	turns, calls, err := callreport.Load(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "voxie report: %v\n", err)
		return 1
	}
	callreport.Summarize(out, turns, calls)
	return 0
}
