package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gochain/internal/bftdemo"
)

func main() {
	out := flag.String("out", filepath.Join("reports", "consensus", time.Now().UTC().Format("20060102T150405.000000000Z")), "new output directory")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(out string) error {
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return err
	}
	if err := os.Mkdir(out, 0755); err != nil {
		return err
	}
	r, err := bftdemo.Run(out)
	for _, c := range r.Checks {
		fmt.Printf("passed=%v  %s: %s\n", c.Passed, c.Name, c.Details)
	}
	path, _ := filepath.Abs(filepath.Join(out, "report.json"))
	fmt.Println("Report:", path)
	return err
}
