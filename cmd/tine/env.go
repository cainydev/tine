package main

import (
	"fmt"

	"github.com/cainydev/tine/internal/credential"
	"github.com/cainydev/tine/internal/web"
)

// secretCmd prints a new session secret.
type secretCmd struct{}

func (*secretCmd) Run() error {
	secret, err := web.GenerateSecret()
	if err != nil {
		return err
	}
	fmt.Println(secret)
	return nil
}

// envCmd prints a configuration template with fresh secrets.
type envCmd struct{}

func (*envCmd) Run() error {
	masterKey, err := credential.GenerateMasterKey()
	if err != nil {
		return err
	}
	sessionSecret, err := web.GenerateSecret()
	if err != nil {
		return err
	}

	fmt.Printf(`# tine configuration. keep this file out of version control.

# where tine is reachable. must match what clients dial: it is published as the
# oauth protected resource identifier.
TINE_PUBLIC_URL=http:

# identity provider. any oidc issuer works.
#
# workos:    https:
# zitadel:   https:
# authentik: https:
#
# check it with: curl <issuer>/.well-known/openid-configuration
TINE_OIDC_ISSUER=
TINE_OIDC_AUDIENCE=

# web sign in. the redirect uri registered with the provider must be
# <TINE_PUBLIC_URL>/callback
TINE_OIDC_CLIENT_ID=
TINE_OIDC_CLIENT_SECRET=

# generated, keep these
TINE_MASTER_KEY=%s
TINE_SESSION_SECRET=%s

# optional
# TINE_ADDR=:8080
# TINE_DATABASE_PATH=tine.db
# TINE_LOG_LEVEL=info
`, masterKey, sessionSecret)
	return nil
}
