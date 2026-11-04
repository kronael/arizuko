package main

import (
	"log/slog"
	"os"

	"github.com/kronael/arizuko/obs"
	_ "github.com/kronael/arizuko/resreg/resources"
)

func main() {
	defer obs.Setup("timed", os.Getenv("ARIZUKO_INSTANCE"))()

	routerURL := os.Getenv("ROUTER_URL")
	if routerURL == "" {
		slog.Error("ROUTER_URL env required")
		os.Exit(1)
	}
	runSplit(routerURL)
}
