// Command t4 runs a T4 node and exposes it as an etcd v3 gRPC endpoint.
package main

import (
	"os"

	"github.com/t4db/t4/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
