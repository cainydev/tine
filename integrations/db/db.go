// Package db exposes Deutsche Bahn timetable data as MCP tools.
//
// It talks to a db-rest instance, the community REST wrapper around DB's public
// transport APIs. No authentication is required.
package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sync/errgroup"

	"github.com/cainydev/tine/integrations"
	"github.com/cainydev/tine/internal/credential"
)

const defaultBaseURL = "https://v6.db.transport.rest"

// Integration serves Deutsche Bahn timetable tools.
type Integration struct{}

// New returns the Deutsche Bahn integration.
func New() *Integration { return &Integration{} }

// Slug is the stable identifier used in endpoint paths.
func (*Integration) Slug() string { return "deutsche-bahn" }

// Name is the human-readable title.
func (*Integration) Name() string { return "Deutsche Bahn Fahrplan" }

// Version identifies the tool surface.
func (*Integration) Version() string { return "1.0.0" }

// Params describes the instance-level settings this integration accepts.
func (*Integration) Params() []integrations.ParamSpec {
	return []integrations.ParamSpec{{
		Key:         "base_url",
		Description: "db-rest instance to query. Override to use a self-hosted one.",
		Default:     defaultBaseURL,
	}, {
		Key:         "language",
		Description: "Language for station names and service messages.",
		Default:     "de",
		Enum:        []string{"de", "en"},
	}}
}

// client holds the per-instance settings for one bound endpoint.
type client struct {
	baseURL  string
	language string
	http     *http.Client
}

// Credentials reports that this integration reaches a public API.
func (*Integration) Credentials() []credential.Kind {
	return []credential.Kind{credential.KindNone}
}

// Bind produces the tool set for one configured instance.
func (*Integration) Bind(_ context.Context, b *integrations.Binding) ([]integrations.Tool, error) {
	c := &client{
		baseURL:  b.Params["base_url"],
		language: b.Params["language"],
		http:     b.HTTP,
	}
	if c.baseURL == "" {
		c.baseURL = defaultBaseURL
	}

	return []integrations.Tool{
		{Name: "search_locations", Register: c.registerSearchLocations},
		{Name: "get_departures", Register: c.registerDepartures},
		{Name: "get_board", Register: c.registerBoard},
	}, nil
}

// SearchLocationsIn is the input schema for search_locations. The SDK infers
// JSON Schema from these types, so the schema cannot drift from the code.
type SearchLocationsIn struct {
	Query string `json:"query" jsonschema:"Station name, city or abbreviation, e.g. 'Aachen Hbf'"`
	Limit int    `json:"limit,omitempty" jsonschema:"Maximum results to return (default 10)"`
}

// LocationsOut wraps the search result.
//
// MCP requires a tool's output schema to be an object, so a bare slice cannot
// be returned directly.
type LocationsOut struct {
	Stops []Stop `json:"stops"`
}

// DeparturesOut wraps a departure board.
type DeparturesOut struct {
	Departures []Departure `json:"departures"`
}

// Stop is a station as returned by db-rest.
type Stop struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Location *struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"location,omitempty"`
}

func (c *client) registerSearchLocations(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "search_locations",
		Description: "Search Deutsche Bahn stops by name. Returns matches with a stable `id` " +
			"(IBNR) to pass to the other tools.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SearchLocationsIn) (
		*mcp.CallToolResult, LocationsOut, error,
	) {
		limit := in.Limit
		if limit <= 0 {
			limit = 10
		}

		q := url.Values{
			"query":     {in.Query},
			"results":   {strconv.Itoa(limit)},
			"addresses": {"false"},
			"poi":       {"false"},
		}

		stops := []Stop{}
		if err := c.get(ctx, "/locations", q, &stops); err != nil {
			return nil, LocationsOut{}, err
		}
		return nil, LocationsOut{Stops: stops}, nil
	})
}

// DeparturesIn is the input schema for get_departures.
type DeparturesIn struct {
	ID       string `json:"id" jsonschema:"Stop id (IBNR) from search_locations"`
	Duration int    `json:"duration,omitempty" jsonschema:"Window in minutes (default 30)"`
}

// Departure is one departure or arrival.
type Departure struct {
	TripID      string `json:"tripId"`
	When        string `json:"when"`
	PlannedWhen string `json:"plannedWhen"`
	Delay       *int   `json:"delay"`
	Platform    string `json:"platform,omitempty"`
	Direction   string `json:"direction,omitempty"`
	Provenance  string `json:"provenance,omitempty"`
	Line        *struct {
		Name    string `json:"name"`
		Product string `json:"product"`
	} `json:"line,omitempty"`
}

func (c *client) registerDepartures(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_departures",
		Description: "Real-time departures from a stop. `delay` is in seconds; 0 means on time " +
			"and null means unknown. Times are ISO 8601 with a timezone offset.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in DeparturesIn) (
		*mcp.CallToolResult, DeparturesOut, error,
	) {
		out, err := c.board(ctx, "/departures", in.ID, in.Duration)
		if err != nil {
			return nil, DeparturesOut{}, err
		}
		return nil, DeparturesOut{Departures: out}, nil
	})
}

// BoardIn is the input schema for get_board.
type BoardIn struct {
	ID       string `json:"id" jsonschema:"Stop id (IBNR) from search_locations"`
	Duration int    `json:"duration,omitempty" jsonschema:"Window in minutes (default 30)"`
	OnlyLate bool   `json:"only_late,omitempty" jsonschema:"Return only delayed services"`
}

// Board combines departures and arrivals for one stop.
type Board struct {
	Departures []Departure `json:"departures"`
	Arrivals   []Departure `json:"arrivals"`
}

// registerBoard is the reason integrations are Go rather than configuration:
// it calls two endpoints concurrently, combines the results, and applies a
// filter that no declarative request mapping could express.
func (c *client) registerBoard(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_board",
		Description: "Departures and arrivals for a stop in one call, optionally filtered to " +
			"delayed services only. Use this instead of two separate calls when you want both.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in BoardIn) (
		*mcp.CallToolResult, Board, error,
	) {
		board := Board{Departures: []Departure{}, Arrivals: []Departure{}}

		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() error {
			out, err := c.board(gctx, "/departures", in.ID, in.Duration)
			board.Departures = out
			return err
		})
		g.Go(func() error {
			out, err := c.board(gctx, "/arrivals", in.ID, in.Duration)
			board.Arrivals = out
			return err
		})
		if err := g.Wait(); err != nil {
			return nil, Board{}, err
		}

		if in.OnlyLate {
			board.Departures = onlyLate(board.Departures)
			board.Arrivals = onlyLate(board.Arrivals)
		}
		return nil, board, nil
	})
}

func onlyLate(in []Departure) []Departure {
	out := make([]Departure, 0, len(in))
	for _, d := range in {
		if d.Delay != nil && *d.Delay > 0 {
			out = append(out, d)
		}
	}
	return out
}

func (c *client) board(ctx context.Context, path, id string, duration int) ([]Departure, error) {
	if id == "" {
		return nil, errors.New("stop id is required")
	}
	if duration <= 0 {
		duration = 30
	}

	q := url.Values{
		"duration": {strconv.Itoa(duration)},
	}

	var envelope struct {
		Departures []Departure `json:"departures"`
		Arrivals   []Departure `json:"arrivals"`
	}
	if err := c.get(ctx, path+"/"+url.PathEscape(id), q, &envelope); err != nil {
		return nil, err
	}
	if envelope.Departures != nil {
		return envelope.Departures, nil
	}
	if envelope.Arrivals != nil {
		return envelope.Arrivals, nil
	}
	return []Departure{}, nil
}

// get performs one upstream request and decodes its JSON body.
func (c *client) get(ctx context.Context, path string, q url.Values, out any) error {
	endpoint := c.baseURL + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.language != "" {
		req.Header.Set("Accept-Language", c.language)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call db-rest: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			_ = err
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("db-rest returned %s for %s", resp.Status, path)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
