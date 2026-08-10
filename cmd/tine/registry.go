package main

import (
	"github.com/cainydev/tine/integrations"
	dbint "github.com/cainydev/tine/integrations/db"
	"github.com/cainydev/tine/integrations/shopware"
)

// registry returns every integration compiled into this binary.
func registry() *integrations.Registry {
	r := integrations.NewRegistry()
	for _, in := range []integrations.Integration{
		dbint.New(),
		shopware.New(),
	} {
		if err := r.Register(in); err != nil {
			panic(err)
		}
	}
	return r
}
