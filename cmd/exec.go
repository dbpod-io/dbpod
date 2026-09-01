package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
)

// runClient executes an engine client binary synchronously, returning
// combined output for error reporting.
func runClient(bin string, args []string) (string, error) {
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runClientWithStdin executes the client feeding r into its stdin — how SQL
// files are executed (the client treats positional args as database names).
func runClientWithStdin(bin string, args []string, r io.Reader) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Stdin = r
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// importViaClient pipes a SQL file into the engine client.
func importViaClient(clientPath string, args []string, sqlFile string) error {
	f, err := os.Open(sqlFile)
	if err != nil {
		return err
	}
	defer f.Close()
	out, err := runClientWithStdin(clientPath, args, f)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}
