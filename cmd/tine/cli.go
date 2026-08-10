package main

import (
	"fmt"
	"strings"

	"github.com/alecthomas/kong"
)

// cli is the complete command surface.
type cli struct {
	Serve   serveCmd   `cmd:"" default:"withargs" help:"Serve every configured instance. Reads its configuration from the environment."`
	Dev     devCmd     `cmd:"" help:"Serve one integration locally with authentication disabled."`
	Connect connectCmd `cmd:"" help:"Launch an agent against an instance on a running server."`
	Seed    seedCmd    `cmd:"" help:"Create a user and an integration instance."`
	Genkey  genkeyCmd  `cmd:"" help:"Print a new master key for TINE_MASTER_KEY."`
	Secret  secretCmd  `cmd:"" help:"Print a new session secret for TINE_SESSION_SECRET."`
	Env     envCmd     `cmd:"" help:"Print a configuration template with fresh secrets."`
}

// parse builds the kong context for tine's command surface.
func parse(args []string) (*kong.Context, error) {
	var root cli

	parser, err := kong.New(&root,
		kong.Name("tine"),
		kong.Description("api to mcp proxy. each integration instance is served at its own endpoint."),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
		kong.Vars{"integrations": integrationList()},
	)
	if err != nil {
		return nil, err
	}
	return parser.Parse(args)
}

// integrationList renders the compiled-in integrations for help text.
func integrationList() string {
	var b strings.Builder
	for _, in := range registry().All() {
		fmt.Fprintf(&b, "\n  %-16s %s", in.Slug(), in.Name())

		specs := in.Params()
		if len(specs) == 0 {
			continue
		}
		keys := make([]string, 0, len(specs))
		for _, spec := range specs {
			keys = append(keys, spec.Key)
		}
		fmt.Fprintf(&b, "\n  %-16s params: %s", "", strings.Join(keys, ", "))
	}
	return b.String()
}
