// Package web serves the interface for managing integration instances and
// their credentials.
package web

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/cainydev/tine/integrations"
	"github.com/cainydev/tine/internal/slug"
	"github.com/cainydev/tine/internal/web/views"
)

//go:embed static
var staticFS embed.FS

// staticSub serves the embedded directory without its "static/" prefix.
func staticSub() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// The directory is embedded at build time, so this cannot fail at run
		// time; a panic here would mean the binary was built wrong.
		panic(err)
	}
	return sub
}

// Store is the persistence the interface needs.
//
// Defined here rather than in the store package because the interface is the
// consumer, and it needs only this much.
type Store interface {
	UserBySubject(ctx context.Context, subject string) (*User, error)
	CreateUser(ctx context.Context, subject, userSlug, email string) (*User, error)
	SlugTaken(ctx context.Context, userSlug string) (bool, error)

	InstancesForUser(ctx context.Context, subject string) ([]Instance, error)
	InstancesForIntegration(ctx context.Context, subject, integrationSlug string) ([]Instance, error)
	InstanceForUser(ctx context.Context, subject, id string) (*Instance, error)

	CreateInstance(ctx context.Context, in NewInstance) (string, error)
	UpdateParams(ctx context.Context, subject, id string, params map[string]string) error
	DeleteInstance(ctx context.Context, subject, id string) error

	SetCredential(ctx context.Context, subject, id string, cred CredentialInput) error
}

// User is an account in the interface.
type User struct {
	ID      string
	Subject string
	Slug    string
	Email   string
}

// Instance is one configured endpoint.
type Instance struct {
	ID              string
	DisplayName     string
	IntegrationSlug string
	IntegrationName string
	Version         string
	Params          map[string]string
	Enabled         bool
	CredentialKind  string
	NeedsReauth     bool
}

// NewInstance describes an instance to create.
type NewInstance struct {
	Subject         string
	IntegrationSlug string
	IntegrationName string
	Version         string
	DisplayName     string
	Params          map[string]string
}

// CredentialInput is a credential submitted through a form.
type CredentialInput struct {
	Kind       string
	Token      string
	HeaderName string
	Value      string
	Username   string
	Password   string
}

// Server serves the interface.
type Server struct {
	store    Store
	registry *integrations.Registry
	auth     *Authenticator
	baseURL  string
	log      *slog.Logger
}

// NewServer returns a Server.
func NewServer(store Store, registry *integrations.Registry, auth *Authenticator, baseURL string, log *slog.Logger) *Server {
	return &Server{
		store:    store,
		registry: registry,
		auth:     auth,
		baseURL:  strings.TrimRight(baseURL, "/"),
		log:      log,
	}
}

// Routes registers the interface on a mux.
//
// The MCP endpoint lives at /{user}/{integration}/{id}. Literal patterns are
// more specific than wildcards in net/http, so /{user}/{integration}/new is
// matched before an instance id could shadow it. Instance ids are hex, so one
// can never be the literal "new".
func (s *Server) Routes(mux *http.ServeMux) {
	// Assets are served from a two-segment pattern with a named final segment.
	// ServeMux compares patterns structurally and cannot tell that a slug never
	// equals "static", so /static/... would be ambiguous against
	// /{user}/{integration}. Matching exactly one file name is unambiguous, and
	// the interface serves only a handful of assets.
	mux.Handle("GET /static/{file}", http.StripPrefix("/static/", http.FileServerFS(staticSub())))

	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /login", s.login)
	mux.HandleFunc("GET /callback", s.callback)
	mux.HandleFunc("POST /logout", s.logout)

	mux.HandleFunc("GET /settings", s.page(s.settings))
	// Not wrapped in page: this is what creates the account, so requiring an
	// existing one would make it unreachable.
	mux.HandleFunc("POST /settings/username", s.claimUsername)
	mux.HandleFunc("GET /integrations", s.page(s.integrationList))

	mux.HandleFunc("GET /{user}", s.page(s.userInstances))
	mux.HandleFunc("GET /{user}/{integration}", s.page(s.integrationInstances))
	mux.HandleFunc("GET /{user}/{integration}/new", s.page(s.newInstanceForm))
	mux.HandleFunc("POST /{user}/{integration}/new", s.page(s.createInstance))

	mux.HandleFunc("GET /{user}/{integration}/{id}", s.page(s.instanceDetail))
	mux.HandleFunc("POST /{user}/{integration}/{id}/params", s.page(s.saveParams))
	mux.HandleFunc("POST /{user}/{integration}/{id}/credential", s.page(s.saveCredential))
	mux.HandleFunc("GET /{user}/{integration}/{id}/credential/fields", s.page(s.credentialFields))
	mux.HandleFunc("POST /{user}/{integration}/{id}/delete", s.page(s.deleteInstance))
}

// handler is a page that needs a signed-in user.
type handler func(w http.ResponseWriter, r *http.Request, u *User) error

// page wraps a handler with authentication and error reporting.
func (s *Server) page(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		session, err := s.auth.Authenticate(ctx, r)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		user, err := s.store.UserBySubject(ctx, session.Subject)
		if errors.Is(err, ErrNoUser) {
			// Signed in but with no account yet: the username has to be chosen
			// before anything can be created under it.
			s.chooseUsername(w, r, session, "")
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}

		// A user may only act under their own path segment.
		if pathUser := r.PathValue("user"); pathUser != "" && pathUser != user.Slug {
			http.NotFound(w, r)
			return
		}

		if err := h(w, r, user); err != nil {
			s.fail(w, r, err)
		}
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.log.ErrorContext(r.Context(), "web request failed",
		slog.String("path", r.URL.Path),
		slog.Any("error", err))
	http.Error(w, "something went wrong", http.StatusInternalServerError)
}

func (s *Server) nav(u *User) views.Nav {
	if u == nil {
		return views.Nav{}
	}
	return views.Nav{UserSlug: u.Slug, Email: u.Email, SignedIn: true}
}

// endpoint returns the public MCP URL for an instance.
func (s *Server) endpoint(userSlug, integrationSlug, id string) string {
	return s.baseURL + "/" + userSlug + "/" + integrationSlug + "/" + id
}

// ErrNoUser is returned by a Store when a subject has no account yet.
var ErrNoUser = errors.New("no user")

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	session, err := s.auth.Authenticate(ctx, r)
	if err != nil {
		s.render(w, r, views.Landing(views.Nav{}))
		return
	}

	user, err := s.store.UserBySubject(ctx, session.Subject)
	if errors.Is(err, ErrNoUser) {
		s.chooseUsername(w, r, session, "")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}

	http.Redirect(w, r, "/"+user.Slug, http.StatusSeeOther)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	url, err := s.auth.Start(w, r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	if _, err := s.auth.Complete(r.Context(), w, r); err != nil {
		// Not a redirect back to /login: that would start the flow again and
		// loop silently on a persistent failure. The reason is safe to show,
		// since it describes the exchange rather than any secret.
		s.log.WarnContext(r.Context(), "login failed", slog.Any("error", err))
		s.render(w, r, views.LoginFailed(err.Error()))
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.auth.Logout(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) chooseUsername(w http.ResponseWriter, r *http.Request, session *Session, errMsg string) {
	s.render(w, r, views.ChooseUsername(views.Nav{Email: session.Email, SignedIn: true},
		slug.Suggest(session.Email), errMsg))
}
