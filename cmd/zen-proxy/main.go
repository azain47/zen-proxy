package main

import "github.com/azain47/zen-proxy/internal/proxy"

var version = "dev"

func main() {
	proxy.Run(version)
}
