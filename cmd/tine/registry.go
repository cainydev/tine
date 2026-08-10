package main

import (
	"github.com/cainydev/tine/integrations"
	dbint "github.com/cainydev/tine/integrations/db"
)

// registry returns every integration compiled into this binary.
//
// One function so the server, the seed command and the dev command can never
// disagree about which integrations exist.
func registry() *integrations.Registry {
	r := integrations.NewRegistry()
	for _, in := range []integrations.Integration{
		dbint.New(),
	} {
		// Register only fails on a duplicate or empty slug, both of which are
		// mistakes in this list rather than runtime conditions.
		if err := r.Register(in); err != nil {
			panic(err)
		}
	}
	return r
}
