package main

import (
	"os"

	"github.com/oremus-labs/ol-model-manager/internal/mllmcli"
)

func main() {
	if err := mllmcli.Execute(); err != nil {
		os.Exit(1)
	}
}

