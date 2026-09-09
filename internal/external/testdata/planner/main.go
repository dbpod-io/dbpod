// Planner fixture: writes a canned download plan pointing at
// https://plugin-resolver.test/x.tgz regardless of the request.
package main

import (
	"io"
	"os"
)

const plan = `{"plan":{"version":"0.0.0","main":{"url":"https://plugin-resolver.test/x.tgz","kind":"tar.gz"}}}`

func main() {
	_, _ = io.ReadAll(os.Stdin)
	os.Stdout.WriteString(plan)
}
