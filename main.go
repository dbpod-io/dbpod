package main

import (
	"github.com/dbpod-io/dbpod/cmd"

	// register the builtin engine providers (versions + download + lifecycle)
	_ "github.com/dbpod-io/dbpod/internal/providers/mysql"
	_ "github.com/dbpod-io/dbpod/internal/providers/postgres"
)

func main() {
	cmd.Execute()
}
