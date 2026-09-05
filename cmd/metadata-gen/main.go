// Command metadata-gen regenerates the engine metadata file shipped inside
// the dbpod repository (internal/metadata/data/mysql.json) and embedded into
// the binary at build time.
//
// This is a maintainer tool: it is the only component that crawls the MySQL
// web pages. Updates are incremental — versions already present in the file
// are treated as immutable, only new versions are crawled.
//
// Usage:
//
//	go run ./cmd/metadata-gen [-out internal/metadata/data] [-c 8]
//
// Commit the updated file afterwards; CI or a release pushes it, and CLI
// installations pick it up via the repository CDN within their 24h refresh.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/dbpod-io/dbpod/internal/metadata"
)

func main() {
	out := flag.String("out", "internal/metadata/data", "output directory inside the repository")
	concurrency := flag.Int("c", 8, "concurrent crawl workers")
	flag.Parse()

	dir := *out
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		fmt.Fprintf(os.Stderr, "error: output directory %q not found (run from the repository root)\n", dir)
		os.Exit(1)
	}
	if err := metadata.Generate(dir, "mysql", *concurrency, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stdout, "updated %s/%s\n", dir, "mysql.json")
}
