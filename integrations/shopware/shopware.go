// Package shopware exposes a Shopware 6 store through its Admin API.
package shopware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sync/errgroup"

	"github.com/cainydev/tine/integrations"
	"github.com/cainydev/tine/internal/credential"
)

// Integration serves Shopware 6 admin tools.
type Integration struct{}

// New returns the Shopware integration.
func New() *Integration { return &Integration{} }

// Slug is the stable identifier used in endpoint paths.
func (*Integration) Slug() string { return "shopware" }

// Name is the human-readable title.
func (*Integration) Name() string { return "Shopware 6" }

// Version identifies the tool surface.
func (*Integration) Version() string { return "1.0.0" }

// Params describes the instance-level settings this integration accepts.
func (*Integration) Params() []integrations.ParamSpec {
	return []integrations.ParamSpec{{
		Key:         "base_url",
		Description: "Store URL, for example https://shop.example.com. The Admin API lives under /api.",
		Required:    true,
	}, {
		Key:         "language",
		Description: "Language id for translated fields, as a 32 character Shopware id. Leave empty for the store default.",
	}}
}

// isShopwareID reports whether s is a Shopware entity id.
//
// Shopware ids are 32 character lowercase hex. The sw-language-id header
// rejects anything else with 412 Precondition Failed, including locale codes
// such as de-DE.
func isShopwareID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// TokenURL returns the Admin API token endpoint for a store.
func TokenURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + "/api/oauth/token"
}

// client holds the per-instance settings for one bound endpoint.
type client struct {
	baseURL  string
	language string
	cred     credential.Credential
	http     *http.Client
}

// Credentials reports that the Admin API accepts only an OAuth2 client
// credentials grant, using an integration's access key id and secret.
func (*Integration) Credentials() []credential.Kind {
	return []credential.Kind{credential.KindOAuth2}
}

// Bind produces the tool set for one configured instance.
func (*Integration) Bind(_ context.Context, b *integrations.Binding) ([]integrations.Tool, error) {
	base := strings.TrimRight(b.Params["base_url"], "/")
	if base == "" {
		return nil, errors.New("base_url is required")
	}

	c := &client{
		baseURL:  base,
		language: b.Params["language"],
		cred:     b.Credential,
		http:     b.HTTP,
	}

	if oauth, ok := c.cred.(*credential.ClientCredentials); ok {
		if oauth.TokenURL == "" {
			oauth.TokenURL = TokenURL(base)
		}
		oauth.WithClient(b.HTTP)
	}

	return []integrations.Tool{
		{Name: "search_products", Register: c.registerSearchProducts},
		{Name: "get_product", Register: c.registerGetProduct},
		{Name: "list_orders", Register: c.registerListOrders},
		{Name: "get_order", Register: c.registerGetOrder},
		{Name: "store_summary", Register: c.registerStoreSummary},
	}, nil
}

// SearchProductsIn is the input schema for search_products.
type SearchProductsIn struct {
	Query string `json:"query,omitempty" jsonschema:"Free text to match against name and product number. Omit to list recent products."`
	Limit int    `json:"limit,omitempty" jsonschema:"Maximum results (default 20, max 100)"`
	Page  int    `json:"page,omitempty" jsonschema:"Page number, starting at 1"`
}

// Product is a product as returned by the Admin API.
type Product struct {
	ID             string  `json:"id"`
	ProductNumber  string  `json:"productNumber"`
	Name           string  `json:"name"`
	Active         bool    `json:"active"`
	Stock          int     `json:"stock"`
	AvailableStock int     `json:"availableStock"`
	Price          []Price `json:"price,omitempty"`
	TaxID          string  `json:"taxId,omitempty"`
}

// Price is one currency's price for a product.
type Price struct {
	CurrencyID string  `json:"currencyId"`
	Net        float64 `json:"net"`
	Gross      float64 `json:"gross"`
}

// ProductsOut wraps a product list. MCP requires object output schemas, so a
// bare slice cannot be returned.
type ProductsOut struct {
	Products []Product `json:"products"`
	Total    int       `json:"total"`
}

func (c *client) registerSearchProducts(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "search_products",
		Description: "Search products by name or product number. Returns id, productNumber, name, " +
			"active flag and stock. Use the id with get_product for full detail.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SearchProductsIn) (
		*mcp.CallToolResult, ProductsOut, error,
	) {
		limit := clamp(in.Limit, 20, 100)
		page := max(in.Page, 1)

		body := map[string]any{
			"limit": limit,
			"page":  page,
		}
		if in.Query != "" {
			body["term"] = in.Query
		}

		var resp struct {
			Total int       `json:"total"`
			Data  []Product `json:"data"`
		}
		if err := c.post(ctx, "/api/search/product", body, &resp); err != nil {
			return nil, ProductsOut{}, err
		}
		return nil, ProductsOut{Products: resp.Data, Total: resp.Total}, nil
	})
}

// GetProductIn is the input schema for get_product.
type GetProductIn struct {
	ID string `json:"id" jsonschema:"Product id from search_products"`
}

// ProductOut wraps a single product.
type ProductOut struct {
	Product Product `json:"product"`
}

func (c *client) registerGetProduct(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_product",
		Description: "Retrieve one product by id, including price and stock.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetProductIn) (
		*mcp.CallToolResult, ProductOut, error,
	) {
		if in.ID == "" {
			return nil, ProductOut{}, errors.New("id is required")
		}

		var resp struct {
			Data Product `json:"data"`
		}
		if err := c.get(ctx, "/api/product/"+in.ID, &resp); err != nil {
			return nil, ProductOut{}, err
		}
		return nil, ProductOut{Product: resp.Data}, nil
	})
}

// ListOrdersIn is the input schema for list_orders.
type ListOrdersIn struct {
	Limit int    `json:"limit,omitempty" jsonschema:"Maximum results (default 20, max 100)"`
	Page  int    `json:"page,omitempty" jsonschema:"Page number, starting at 1"`
	State string `json:"state,omitempty" jsonschema:"Filter by order state technical name, for example open, in_progress, completed, cancelled"`
}

// Order is an order as returned by the Admin API.
type Order struct {
	ID           string  `json:"id"`
	OrderNumber  string  `json:"orderNumber"`
	AmountTotal  float64 `json:"amountTotal"`
	AmountNet    float64 `json:"amountNet"`
	OrderDate    string  `json:"orderDateTime"`
	StateMachine *struct {
		Name          string `json:"name"`
		TechnicalName string `json:"technicalName"`
	} `json:"stateMachineState,omitempty"`
}

// OrdersOut wraps an order list.
type OrdersOut struct {
	Orders []Order `json:"orders"`
	Total  int     `json:"total"`
}

func (c *client) registerListOrders(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_orders",
		Description: "List orders, newest first. `amountTotal` is gross. Filter by state with the " +
			"technical name, not the label.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListOrdersIn) (
		*mcp.CallToolResult, OrdersOut, error,
	) {
		body := map[string]any{
			"limit":        clamp(in.Limit, 20, 100),
			"page":         max(in.Page, 1),
			"associations": map[string]any{"stateMachineState": map[string]any{}},
			"sort":         []map[string]any{{"field": "orderDateTime", "order": "DESC"}},
		}
		if in.State != "" {
			body["filter"] = []map[string]any{{
				"type":  "equals",
				"field": "stateMachineState.technicalName",
				"value": in.State,
			}}
		}

		var resp struct {
			Total int     `json:"total"`
			Data  []Order `json:"data"`
		}
		if err := c.post(ctx, "/api/search/order", body, &resp); err != nil {
			return nil, OrdersOut{}, err
		}
		return nil, OrdersOut{Orders: resp.Data, Total: resp.Total}, nil
	})
}

// GetOrderIn is the input schema for get_order.
type GetOrderIn struct {
	ID string `json:"id" jsonschema:"Order id from list_orders"`
}

// OrderOut wraps a single order.
type OrderOut struct {
	Order Order `json:"order"`
}

func (c *client) registerGetOrder(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_order",
		Description: "Retrieve one order by id, including its current state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetOrderIn) (
		*mcp.CallToolResult, OrderOut, error,
	) {
		if in.ID == "" {
			return nil, OrderOut{}, errors.New("id is required")
		}

		body := map[string]any{
			"filter":       []map[string]any{{"type": "equals", "field": "id", "value": in.ID}},
			"associations": map[string]any{"stateMachineState": map[string]any{}},
			"limit":        1,
		}

		var resp struct {
			Data []Order `json:"data"`
		}
		if err := c.post(ctx, "/api/search/order", body, &resp); err != nil {
			return nil, OrderOut{}, err
		}
		if len(resp.Data) == 0 {
			return nil, OrderOut{}, fmt.Errorf("no order with id %s", in.ID)
		}
		return nil, OrderOut{Order: resp.Data[0]}, nil
	})
}

// SummaryOut is a snapshot of the store.
type SummaryOut struct {
	Products  int `json:"products"`
	Orders    int `json:"orders"`
	Customers int `json:"customers"`
}

// registerStoreSummary counts three entities concurrently. Doing it in one tool
// keeps the agent from making three round trips for a question it asks often.
func (c *client) registerStoreSummary(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "store_summary",
		Description: "Total counts of products, orders and customers in one call.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (
		*mcp.CallToolResult, SummaryOut, error,
	) {
		var out SummaryOut

		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() error {
			n, err := c.count(gctx, "product")
			out.Products = n
			return err
		})
		g.Go(func() error {
			n, err := c.count(gctx, "order")
			out.Orders = n
			return err
		})
		g.Go(func() error {
			n, err := c.count(gctx, "customer")
			out.Customers = n
			return err
		})
		if err := g.Wait(); err != nil {
			return nil, SummaryOut{}, err
		}
		return nil, out, nil
	})
}

// count returns how many of an entity exist, without fetching any.
func (c *client) count(ctx context.Context, entity string) (int, error) {
	var resp struct {
		Total int `json:"total"`
	}
	err := c.post(ctx, "/api/search/"+entity, map[string]any{
		"limit":            1,
		"total-count-mode": 1,
	}, &resp)
	return resp.Total, err
}

func (c *client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *client) post(ctx context.Context, path string, body, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	return c.do(ctx, http.MethodPost, path, encoded, out)
}

// do performs one Admin API request, refreshing the credential once if the
// store rejects it.
func (c *client) do(ctx context.Context, method, path string, body []byte, out any) error {
	resp, err := c.send(ctx, method, path, body)
	if err != nil {
		return err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close() //nolint:errcheck // discarded, about to retry

		if refreshErr := c.cred.Refresh(ctx); refreshErr != nil {
			return fmt.Errorf("shopware rejected the credential and it could not be refreshed: %w", refreshErr)
		}
		if resp, err = c.send(ctx, method, path, body); err != nil {
			return err
		}
	}

	defer func() {
		_ = resp.Body.Close() //nolint:errcheck // response fully read
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("shopware returned %s for %s", resp.Status, path)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (c *client) send(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	var req *http.Request
	var err error
	if reader != nil {
		req, err = http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if isShopwareID(c.language) {
		req.Header.Set("sw-language-id", c.language)
	}

	if applyErr := c.cred.Apply(ctx, req); applyErr != nil {
		return nil, fmt.Errorf("apply credential: %w", applyErr)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call shopware: %w", err)
	}
	return resp, nil
}

// clamp returns value bounded by a default and a maximum.
func clamp(value, fallback, maximum int) int {
	if value <= 0 {
		return fallback
	}
	return min(value, maximum)
}
