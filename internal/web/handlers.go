package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/a-h/templ"

	"github.com/cainydev/tine/integrations"
	"github.com/cainydev/tine/internal/credential"
	"github.com/cainydev/tine/internal/slug"
	"github.com/cainydev/tine/internal/web/views"
)

func (s *Server) render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := c.Render(r.Context(), w); err != nil {
		s.fail(w, r, err)
	}
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request, u *User) error {
	s.render(w, r, views.Settings(s.nav(u), s.baseURL))
	return nil
}

// claimUsername creates the account for a signed-in user who has none yet.
func (s *Server) claimUsername(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	session, err := s.auth.Authenticate(ctx, r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	wanted := strings.TrimSpace(r.FormValue("username"))
	if invalid := slug.ValidateUser(wanted); invalid != nil {
		s.chooseUsername(w, r, session, invalid.Error())
		return
	}

	taken, err := s.store.SlugTaken(ctx, wanted)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if taken {
		s.chooseUsername(w, r, session, fmt.Sprintf("username %q is taken", wanted))
		return
	}

	if _, err := s.store.CreateUser(ctx, session.Subject, wanted, session.Email); err != nil {
		s.fail(w, r, err)
		return
	}

	http.Redirect(w, r, "/"+wanted, http.StatusSeeOther)
}

func (s *Server) integrationList(w http.ResponseWriter, r *http.Request, u *User) error {
	instances, err := s.store.InstancesForUser(r.Context(), u.Subject)
	if err != nil {
		return err
	}

	counts := make(map[string]int, len(instances))
	for _, in := range instances {
		counts[in.IntegrationSlug]++
	}

	all := s.registry.All()
	out := make([]views.Integration, 0, len(all))
	for _, g := range all {
		out = append(out, views.Integration{
			Slug:      g.Slug(),
			Name:      g.Name(),
			Version:   g.Version(),
			Instances: counts[g.Slug()],
		})
	}

	s.render(w, r, views.IntegrationList(s.nav(u), out))
	return nil
}

func (s *Server) userInstances(w http.ResponseWriter, r *http.Request, u *User) error {
	instances, err := s.store.InstancesForUser(r.Context(), u.Subject)
	if err != nil {
		return err
	}
	s.render(w, r, views.InstanceList(s.nav(u), s.viewInstances(u, instances)))
	return nil
}

func (s *Server) integrationInstances(w http.ResponseWriter, r *http.Request, u *User) error {
	integrationSlug := r.PathValue("integration")

	in, ok := s.registry.Get(integrationSlug)
	if !ok {
		http.NotFound(w, r)
		return nil
	}

	instances, err := s.store.InstancesForIntegration(r.Context(), u.Subject, integrationSlug)
	if err != nil {
		return err
	}

	s.render(w, r, views.IntegrationInstances(s.nav(u), views.Integration{
		Slug:    in.Slug(),
		Name:    in.Name(),
		Version: in.Version(),
	}, s.viewInstances(u, instances)))
	return nil
}

func (s *Server) newInstanceForm(w http.ResponseWriter, r *http.Request, u *User) error {
	in, ok := s.registry.Get(r.PathValue("integration"))
	if !ok {
		http.NotFound(w, r)
		return nil
	}

	s.render(w, r, views.NewInstance(s.nav(u), views.Integration{
		Slug:    in.Slug(),
		Name:    in.Name(),
		Version: in.Version(),
		Params:  viewParams(in, nil),
	}, ""))
	return nil
}

func (s *Server) createInstance(w http.ResponseWriter, r *http.Request, u *User) error {
	in, ok := s.registry.Get(r.PathValue("integration"))
	if !ok {
		http.NotFound(w, r)
		return nil
	}

	params := formParams(r, in)
	validated, err := integrations.ValidateParams(in, params)
	if err != nil {
		s.render(w, r, views.NewInstance(s.nav(u), views.Integration{
			Slug: in.Slug(), Name: in.Name(), Version: in.Version(),
			Params: viewParams(in, params),
		}, err.Error()))
		return nil
	}

	displayName := strings.TrimSpace(r.FormValue("display_name"))
	if displayName == "" {
		displayName = in.Name()
	}

	id, err := s.store.CreateInstance(r.Context(), NewInstance{
		Subject:         u.Subject,
		IntegrationSlug: in.Slug(),
		IntegrationName: in.Name(),
		Version:         in.Version(),
		DisplayName:     displayName,
		Params:          validated,
	})
	if err != nil {
		return err
	}

	http.Redirect(w, r, fmt.Sprintf("/%s/%s/%s", u.Slug, in.Slug(), id), http.StatusSeeOther)
	return nil
}

func (s *Server) instanceDetail(w http.ResponseWriter, r *http.Request, u *User) error {
	instance, integration, err := s.lookup(r, u)
	if err != nil {
		return err
	}
	if instance == nil {
		http.NotFound(w, r)
		return nil
	}

	s.render(w, r, views.InstanceDetail(s.nav(u),
		s.viewInstance(u, *instance),
		viewParams(integration, instance.Params),
		""))
	return nil
}

func (s *Server) saveParams(w http.ResponseWriter, r *http.Request, u *User) error {
	instance, integration, err := s.lookup(r, u)
	if err != nil {
		return err
	}
	if instance == nil {
		http.NotFound(w, r)
		return nil
	}

	params := formParams(r, integration)
	validated, err := integrations.ValidateParams(integration, params)
	if err != nil {
		s.render(w, r, views.InstanceDetail(s.nav(u),
			s.viewInstance(u, *instance),
			viewParams(integration, params),
			err.Error()))
		return nil
	}

	if err := s.store.UpdateParams(r.Context(), u.Subject, instance.ID, validated); err != nil {
		return err
	}

	http.Redirect(w, r, s.instancePath(u, *instance), http.StatusSeeOther)
	return nil
}

func (s *Server) saveCredential(w http.ResponseWriter, r *http.Request, u *User) error {
	instance, integration, err := s.lookup(r, u)
	if err != nil {
		return err
	}
	if instance == nil {
		http.NotFound(w, r)
		return nil
	}

	input := CredentialInput{
		Kind:         r.FormValue("kind"),
		Token:        r.FormValue("token"),
		HeaderName:   r.FormValue("header_name"),
		Value:        r.FormValue("header_value"),
		Username:     r.FormValue("username"),
		Password:     r.FormValue("password"),
		ClientID:     r.FormValue("oauth_client_id"),
		ClientSecret: r.FormValue("oauth_client_secret"),
		TokenURL:     strings.TrimSpace(r.FormValue("oauth_token_url")),
		BaseURL:      instance.Params["base_url"],
	}

	if !integrations.AcceptsCredential(integration, credential.Kind(input.Kind)) {
		s.render(w, r, views.InstanceDetail(s.nav(u),
			s.viewInstance(u, *instance),
			viewParams(integration, instance.Params),
			fmt.Sprintf("%s does not accept %q credentials", integration.Name(), input.Kind)))
		return nil
	}

	if err := s.store.SetCredential(r.Context(), u.Subject, instance.ID, input); err != nil {
		s.render(w, r, views.InstanceDetail(s.nav(u),
			s.viewInstance(u, *instance),
			viewParams(integration, instance.Params),
			err.Error()))
		return nil
	}

	http.Redirect(w, r, s.instancePath(u, *instance), http.StatusSeeOther)
	return nil
}

// credentialFields serves the inputs for one credential kind, swapped in by
// htmx when the kind changes.
func (s *Server) credentialFields(w http.ResponseWriter, r *http.Request, _ *User) error {
	s.render(w, r, views.CredentialFields(r.URL.Query().Get("kind")))
	return nil
}

func (s *Server) deleteInstance(w http.ResponseWriter, r *http.Request, u *User) error {
	if err := s.store.DeleteInstance(r.Context(), u.Subject, r.PathValue("id")); err != nil {
		return err
	}
	http.Redirect(w, r, "/"+u.Slug, http.StatusSeeOther)
	return nil
}

// lookup resolves the instance named in the path along with its integration.
func (s *Server) lookup(r *http.Request, u *User) (*Instance, integrations.Integration, error) {
	instance, err := s.store.InstanceForUser(r.Context(), u.Subject, r.PathValue("id"))
	if errors.Is(err, ErrNoUser) || instance == nil {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}

	if instance.IntegrationSlug != r.PathValue("integration") {
		return nil, nil, nil
	}

	integration, ok := s.registry.Get(instance.IntegrationSlug)
	if !ok {
		return nil, nil, fmt.Errorf("integration %q is not registered", instance.IntegrationSlug)
	}
	return instance, integration, nil
}

func (s *Server) instancePath(u *User, in Instance) string {
	return fmt.Sprintf("/%s/%s/%s", u.Slug, in.IntegrationSlug, in.ID)
}

func (s *Server) viewInstance(u *User, in Instance) views.Instance {
	var kinds []string
	if integration, ok := s.registry.Get(in.IntegrationSlug); ok {
		for _, k := range integration.Credentials() {
			kinds = append(kinds, string(k))
		}
	}

	return views.Instance{
		CredentialKinds: kinds,
		ID:              in.ID,
		DisplayName:     in.DisplayName,
		IntegrationSlug: in.IntegrationSlug,
		IntegrationName: in.IntegrationName,
		Version:         in.Version,
		Enabled:         in.Enabled,
		CredentialKind:  in.CredentialKind,
		NeedsReauth:     in.NeedsReauth,
		Endpoint:        s.endpoint(u.Slug, in.IntegrationSlug, in.ID),
	}
}

func (s *Server) viewInstances(u *User, instances []Instance) []views.Instance {
	out := make([]views.Instance, 0, len(instances))
	for _, in := range instances {
		out = append(out, s.viewInstance(u, in))
	}
	return out
}

// viewParams pairs an integration's parameter specs with current values.
func viewParams(in integrations.Integration, values map[string]string) []views.Param {
	specs := in.Params()
	out := make([]views.Param, 0, len(specs))

	for _, spec := range specs {
		out = append(out, views.Param{
			Key:         spec.Key,
			Description: spec.Description,
			Required:    spec.Required,
			Default:     spec.Default,
			Enum:        spec.Enum,
			Value:       values[spec.Key],
		})
	}
	return out
}

// formParams reads an integration's parameters from a submitted form.
func formParams(r *http.Request, in integrations.Integration) map[string]string {
	out := make(map[string]string)
	for _, spec := range in.Params() {
		if v := strings.TrimSpace(r.FormValue("param_" + spec.Key)); v != "" {
			out[spec.Key] = v
		}
	}
	return out
}
