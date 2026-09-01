package main

import (
	"github.com/shapled/dbpod/cmd"

	// register supported engines
	_ "github.com/shapled/dbpod/internal/engine/mysql"
)

func main() {
	cmd.Execute()
}
