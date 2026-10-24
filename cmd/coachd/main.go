// Command coachd is a gateway between Garmin data and AI agents.
package main

import (
	"fmt"
	"os"

	_ "time/tzdata" // zone database for distroless images
)

var version = "dev"

const usage = "usage: coachd <serve|import|token|device|enroll|backup|restore|healthcheck|version>\n"

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Println("coachd " + version)
		return 0
	case "token":
		return tokenMain(args[1:])
	case "device":
		return deviceMain(args[1:])
	case "enroll":
		return enrollMain()
	case "backup":
		return backupMain(args[1:])
	case "restore":
		return restoreMain(args[1:])
	case "serve":
		return serveMain()
	case "healthcheck":
		return healthcheckMain()
	case "import":
		fmt.Fprintf(os.Stderr, "coachd %s: not implemented\n", args[0])
		return 1
	default:
		fmt.Fprintf(os.Stderr, "coachd: unknown command %q\n%s", args[0], usage)
		return 2
	}
}
