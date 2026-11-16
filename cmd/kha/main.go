// Command kha is the CLI for the Kimi Hosted Agents API.
// The command tree lives in internal/cli/commands.
package main

import (
	"os"

	"github.com/MoonshotAI/kha/internal/cli/commands"
)

func main() {
	os.Exit(commands.Execute())
}
