// Command pgpilot is a transparent, LSN-fencing PostgreSQL connection router.
//
// This entrypoint is a skeleton: the wire protocol, connection pooling, query
// classification, and routing are implemented in later phases. See the roadmap
// in README.md.
package main

import (
	"fmt"
	"os"
)

// version is the build version, overridden via -ldflags "-X main.version=...".
var version = "0.0.0-dev"

func main() {
	fmt.Printf("pgpilot %s\n", version)
	fmt.Fprintln(os.Stderr, "not yet implemented; see the roadmap in README.md")
}
