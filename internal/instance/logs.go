package instance

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"time"
)

// Tail returns the last n lines of a file (all lines when n <= 0).
func Tail(path string, n int) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	lines := splitLines(string(data))
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out, nil
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			end := i
			if end > start && s[end-1] == '\r' {
				end--
			}
			lines = append(lines, s[start:end])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// Logs prints the service log, optionally following (poll-based tail -f).
func Logs(r *Record, follow bool, stdout io.Writer) error {
	f, err := os.Open(r.LogPath)
	if os.IsNotExist(err) {
		return fmt.Errorf("no log file for instance %q (never started?)", r.Name)
	}
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(stdout, f); err != nil {
		return err
	}
	if !follow {
		return nil
	}
	// poll for appended data until interrupted
	for {
		time.Sleep(500 * time.Millisecond)
		if !r.Running() {
			fmt.Fprintln(stdout, "[server stopped, log follow ended]")
			return nil
		}
		if _, err := f.Seek(0, io.SeekEnd); err != nil {
			return err
		}
		reader := bufio.NewReader(f)
		if _, err := io.Copy(stdout, reader); err != nil && err != io.EOF {
			return err
		}
	}
}
