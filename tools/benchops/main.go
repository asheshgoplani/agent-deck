// Command benchops turns BenchmarkOp_* output into results files and the
// tables in docs/performance.md (ASK-106).
//
//	go run ./tools/benchops parse -in raw.txt -label 1.16.31 -ref release/1.16.31 -sha 65e6a550 \
//	    -catalogue bench/ops/catalogue.json -out bench/ops/results/1.16.31.json
//	go run ./tools/benchops render -results bench/ops/results -catalogue bench/ops/catalogue.json \
//	    -doc docs/performance.md
//
// parse keeps the median over the -count runs of each benchmark. render
// rewrites only the text between the BENCH and BACKLOG markers of the doc.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "parse":
		err = runParse(os.Args[2:])
	case "render":
		err = runRender(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "benchops:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: benchops parse|render [flags] (see -h of each)")
	os.Exit(2)
}

func runParse(args []string) error {
	fs := flag.NewFlagSet("parse", flag.ExitOnError)
	in := fs.String("in", "", "raw go test -bench output")
	out := fs.String("out", "", "results JSON to write")
	cat := fs.String("catalogue", "bench/ops/catalogue.json", "operation catalogue")
	label := fs.String("label", "", "column label, e.g. 1.16.31")
	ref := fs.String("ref", "", "git ref benchmarked")
	sha := fs.String("sha", "", "commit sha benchmarked")
	commitDate := fs.String("commit-date", "", "commit date (RFC 3339), orders the columns")
	date := fs.String("date", "", "run date (RFC 3339, default now)")
	host := fs.String("host", "", "where it ran")
	_ = fs.Parse(args)
	if *in == "" || *out == "" || *label == "" {
		return fmt.Errorf("parse needs -in, -out and -label")
	}
	c, err := loadCatalogue(*cat)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	if *date == "" {
		*date = time.Now().Format(time.RFC3339)
	}
	res := Parse(string(raw), c)
	res.Label, res.Ref, res.SHA, res.Date, res.CommitDate, res.Host = *label, *ref, *sha, *date, *commitDate, *host
	if len(res.Results) == 0 {
		return fmt.Errorf("no BenchmarkOp_ results in %s", *in)
	}
	return writeJSON(*out, res)
}

func runRender(args []string) error {
	fs := flag.NewFlagSet("render", flag.ExitOnError)
	dir := fs.String("results", "bench/ops/results", "directory of <label>.json results")
	cat := fs.String("catalogue", "bench/ops/catalogue.json", "operation catalogue")
	doc := fs.String("doc", "docs/performance.md", "markdown file with BENCH and BACKLOG markers")
	_ = fs.Parse(args)
	c, err := loadCatalogue(*cat)
	if err != nil {
		return err
	}
	runs, err := loadResults(*dir)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(*doc)
	if err != nil {
		return err
	}
	outText, err := Render(string(body), c, runs)
	if err != nil {
		return err
	}
	return os.WriteFile(*doc, []byte(outText), 0o644)
}
