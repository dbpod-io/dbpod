package main

import (
	"github.com/dbpod-io/dbpod/cmd"

	// register supported engines, providers and their catalogs
	_ "github.com/dbpod-io/dbpod/internal/engine/mysql"
	_ "github.com/dbpod-io/dbpod/internal/engine/postgres"
	_ "github.com/dbpod-io/dbpod/internal/providers/mysql"
	_ "github.com/dbpod-io/dbpod/internal/providers/postgres"
)

func main() {
	cmd.Execute()
}
