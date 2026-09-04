package main

import (
	"github.com/shapled/dbpod/cmd"

	// register supported engines and their catalogs
	_ "github.com/shapled/dbpod/internal/dist/postgres"
	_ "github.com/shapled/dbpod/internal/engine/mysql"
	_ "github.com/shapled/dbpod/internal/engine/postgres"
)

func main() {
	cmd.Execute()
}
