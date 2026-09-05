package main

import (
	"github.com/shapled/dbpod/cmd"

	// register supported engines, providers and their catalogs
	_ "github.com/shapled/dbpod/internal/engine/mysql"
	_ "github.com/shapled/dbpod/internal/engine/postgres"
	_ "github.com/shapled/dbpod/internal/providers/mysql"
	_ "github.com/shapled/dbpod/internal/providers/postgres"
)

func main() {
	cmd.Execute()
}
