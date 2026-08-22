package main

import (
	"os"
	"runtime/debug"

	"github.com/azain47/zen-proxy/internal/proxy"
)

var version = "dev"

func main() {
	os.Exit(proxy.Run(effectiveVersion()))
}

func effectiveVersion() string {
	if version != "" && version != "dev" {
		return version
	}
	if buildInfo, ok := debug.ReadBuildInfo(); ok && buildInfo.Main.Version != "" && buildInfo.Main.Version != "(devel)" {
		return buildInfo.Main.Version
	}
	return version
}
