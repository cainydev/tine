package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cainydev/tine/integrations"
	"github.com/cainydev/tine/internal/credential"
	"github.com/cainydev/tine/internal/gateway"
	"github.com/cainydev/tine/internal/store"
)

const (
	devUser    = "dev"
	devSubject = "dev-user"

	// devInstanceID is fixed so the endpoint survives restarts and a client
	// configured once keeps working.
	devInstanceID = "dev"
)

// devCmd serves one integration locally with authentication disabled.
type devCmd struct {
	Integration string            `arg:"" help:"Integration to serve. Compiled in:${integrations}"`
	Param       map[string]string `short:"p" placeholder:"KEY=VALUE" help:"Instance parameter. Repeatable."`
	Addr        string            `short:"a" default:":8377" help:"Listen address."`
	Verbose     bool              `short:"v" help:"Log at debug level."`
	Launch      string            `short:"l" enum:"none,claude" default:"none" help:"Agent to launch against this endpoint. With none, tine serves and logs to this terminal."`
	NoSplit     bool              `help:"Give the agent the whole terminal instead of splitting it with the request log."`
	LogPercent  int               `default:"20" help:"Percentage of the terminal given to the log pane."`
	PrintConfig bool              `help:"Print the MCP client configuration and exit."`
}

func (c *devCmd) Run() error {
	reg := registry()

	in, ok := reg.Get(c.Integration)
	if !ok {
		return fmt.Errorf("unknown integration %q, run `tine dev --help` to list them", c.Integration)
	}

	params, err := integrations.ValidateParams(in, c.Param)
	if err != nil {
		return err
	}

	level := slog.LevelInfo
	if c.Verbose {
		level = slog.LevelDebug
	}

	ctx, stop := signalContext()
	defer stop()

	var split *splitSession
	if c.splitting() {
		split, err = newSplitSession(strings.TrimPrefix(portOf(c.Addr), ":"))
		if err != nil {
			return err
		}
		defer func() {
			if closeErr := split.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "tine: %v\n", closeErr)
			}
		}()
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	db, err := store.Open(ctx, ":memory:")
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer closeStore(db, log)

	encoded, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encode params: %w", err)
	}

	if _, err := db.SeedInstance(ctx, store.SeedRequest{
		Subject:            devSubject,
		UserSlug:           devUser,
		Email:              "dev@localhost",
		IntegrationSlug:    in.Slug(),
		IntegrationName:    in.Name(),
		IntegrationVersion: in.Version(),
		DisplayName:        in.Name(),
		Params:             string(encoded),
		Now:                time.Now().Unix(),
		NewID:              fixedIDs(devInstanceID),
	}); err != nil {
		return fmt.Errorf("create dev instance: %w", err)
	}

	publicURL := "http://localhost" + portOf(c.Addr)
	url := publicURL + fmt.Sprintf("/%s/%s/%s", devUser, in.Slug(), devInstanceID)

	if c.PrintConfig {
		config, err := clientConfig(in.Slug(), url)
		if err != nil {
			return err
		}
		fmt.Println(string(config))
		return nil
	}

	upstream := &http.Client{Timeout: 30 * time.Second}

	gw := gateway.New(
		db,
		gateway.NewIntegrationBuilder(reg, db, db, upstream),
		gateway.NewDevAuthenticator(devSubject, publicURL),
		log,
	)

	fmt.Printf("\n  %s\n  %s\n\n  auth disabled\n\n", in.Name(), url)
	for _, spec := range in.Params() {
		fmt.Printf("  %-12s %s\n", spec.Key, params[spec.Key])
	}
	fmt.Println()

	if c.Launch == launchNone {
		return serveHTTP(ctx, c.Addr, requestLog(gw.Handler(), log, os.Stderr), 5*time.Second)
	}

	agentCtx, stopServing := context.WithCancel(ctx)
	defer stopServing()

	if c.splitting() {
		names, err := toolNames(ctx, in, params, upstream)
		if err != nil {
			return err
		}

		return c.runSplit(ctx, splitArgs{
			session:     split,
			agent:       c.Launch,
			name:        in.Slug(),
			integration: in.Name(),
			version:     in.Version(),
			tools:       names,
			url:         url,
			publicURL:   publicURL,
			handler:     gw.Handler(),
			serveCtx:    agentCtx,
			stopServing: stopServing,
		})
	}

	served := make(chan error, 1)
	go func() { served <- serveHTTP(agentCtx, c.Addr, requestLog(gw.Handler(), log, os.Stderr), 5*time.Second) }()

	if err := waitForHealth(ctx, publicURL); err != nil {
		return err
	}

	launchErr := launchAgent(ctx, c.Launch, in.Slug(), url)
	stopServing()

	if serveErr := <-served; serveErr != nil {
		return serveErr
	}
	return launchErr
}

// splitArgs carries what runSplit needs, so its signature stays readable.
type splitArgs struct {
	session     *splitSession
	agent       string
	name        string
	integration string
	version     string
	tools       []string
	url         string
	publicURL   string
	handler     http.Handler

	serveCtx    context.Context
	stopServing context.CancelFunc
}

// runSplit starts the tmux session, points the log at its second pane, and
// serves until the session ends.
func (c *devCmd) runSplit(ctx context.Context, a splitArgs) error {
	agent, err := agentCommandFor(a.agent, a.name, a.url)
	if err != nil {
		return err
	}

	logFile, err := a.session.Start(ctx, agent, c.LogPercent)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := logFile.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "tine: close log: %v\n", closeErr)
		}
	}()
	defer a.session.Kill(context.WithoutCancel(ctx))

	go func() {
		<-ctx.Done()
		a.session.Kill(context.WithoutCancel(ctx))
	}()

	paneLog := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))

	var header strings.Builder
	fmt.Fprintf(&header, "tine  %s %s\n%s\nauth disabled\n\n%d tools\n",
		a.integration, a.version, a.url, len(a.tools))
	for _, name := range a.tools {
		header.WriteString("  " + name + "\n")
	}
	header.WriteString("\nwaiting for requests\n\n")

	if _, err := io.WriteString(logFile, header.String()); err != nil {
		return fmt.Errorf("write to log pane: %w", err)
	}

	served := make(chan error, 1)
	//nolint:contextcheck // serveCtx is cancelled by stopServing when tmux exits
	go func() {
		served <- serveHTTP(a.serveCtx, c.Addr, requestLog(a.handler, paneLog, logFile), 5*time.Second)
	}()

	if err := waitForHealth(ctx, a.publicURL); err != nil {
		return err
	}

	attachErr := a.session.Attach(ctx)
	a.stopServing()

	if serveErr := <-served; serveErr != nil {
		return serveErr
	}
	return attachErr
}

// toolNames lists the tools an integration exposes, for the log pane header.
func toolNames(
	ctx context.Context,
	in integrations.Integration,
	params map[string]string,
	client *http.Client,
) ([]string, error) {
	tools, err := in.Bind(ctx, &integrations.Binding{
		InstanceID: devInstanceID,
		Params:     params,
		Credential: credential.None{},
		HTTP:       client,
	})
	if err != nil {
		return nil, fmt.Errorf("bind integration: %w", err)
	}

	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	return names, nil
}

// launchNone is the --launch value meaning "serve, launch nothing".
const launchNone = "none"

// splitting reports whether the agent shares the terminal with the log pane.
//
// Splitting is the default because seeing requests as the agent makes them is
// the point of a dev run; --no-split exists for when tmux is unavailable or the
// agent should have the full terminal.
func (c *devCmd) splitting() bool {
	return c.Launch != launchNone && !c.NoSplit
}

// requestLog reports each MCP call: the tool, its arguments, and a truncated
// view of what came back.
func requestLog(next http.Handler, log *slog.Logger, out io.Writer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		call := describeCall(r)

		rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		took := time.Since(start).Round(time.Millisecond)

		if call.tool == "" {
			label := call.method
			if label == "" {
				label = r.Method + " " + r.URL.Path
			}
			log.Info(label, slog.Duration("took", took))
			return
		}

		result, failed := decodeResult(rec.body.Bytes())
		status := "ok"
		if failed {
			status = "error"
		}

		var entry strings.Builder
		fmt.Fprintf(&entry, "\n%s  %s\n", call.tool, took)
		if args := formatJSON(call.args, "  "); args != "" {
			entry.WriteString(args + "\n")
		}
		fmt.Fprintf(&entry, "  -> %s %s\n", status, truncate(result, maxLoggedResult))

		if _, err := io.WriteString(out, entry.String()); err != nil {
			log.Warn("write to log pane", slog.Any("error", err))
		}
	})
}

const (
	// maxLoggedBody bounds how much of a request body is buffered to identify a
	// call. Tool arguments can be large and none of that is needed here.
	maxLoggedBody = 64 << 10

	// maxLoggedResult bounds how much of a result is shown. The log pane is a
	// few lines tall, so a full response would push everything else out.
	maxLoggedResult = 400
)

// mcpCall is what could be read from a request body.
type mcpCall struct {
	method string
	tool   string
	args   json.RawMessage
}

// describeCall reads the JSON-RPC method, tool name and arguments from a
// request, leaving the body readable by the handler.
func describeCall(r *http.Request) mcpCall {
	if r.Body == nil || r.Method != http.MethodPost {
		return mcpCall{}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxLoggedBody))
	if err != nil {
		return mcpCall{}
	}
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))

	var parsed struct {
		Method string `json:"method"`
		Params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return mcpCall{}
	}
	return mcpCall{
		method: parsed.Method,
		tool:   parsed.Params.Name,
		args:   parsed.Params.Arguments,
	}
}

// decodeResult extracts a tool result from a response and reports whether the
// tool failed.
func decodeResult(body []byte) (string, bool) {
	payload := body
	if line, ok := sseData(body); ok {
		payload = line
	}

	var envelope struct {
		Result struct {
			IsError           bool            `json:"isError"`
			StructuredContent json.RawMessage `json:"structuredContent"`
			Content           []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return strings.TrimSpace(string(payload)), false
	}

	if envelope.Error != nil {
		return envelope.Error.Message, true
	}

	if len(envelope.Result.StructuredContent) > 0 {
		return formatJSON(envelope.Result.StructuredContent, ""), envelope.Result.IsError
	}
	if len(envelope.Result.Content) > 0 {
		return envelope.Result.Content[0].Text, envelope.Result.IsError
	}
	return strings.TrimSpace(string(payload)), envelope.Result.IsError
}

// sseData returns the payload of the first data: line in an SSE response.
func sseData(body []byte) ([]byte, bool) {
	for line := range strings.SplitSeq(string(body), "\n") {
		if after, ok := strings.CutPrefix(line, "data: "); ok {
			return []byte(after), true
		}
	}
	return nil, false
}

// formatJSON indents JSON for reading, returning the original on failure.
func formatJSON(raw []byte, indent string) string {
	if len(raw) == 0 {
		return ""
	}

	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, indent, "  "); err != nil {
		return strings.TrimSpace(string(raw))
	}
	return indent + buf.String()
}

// truncate shortens text to n characters, noting how much was dropped.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("... (%d more)", len(s)-n)
}

// responseRecorder captures a handler's status and body so a tool result can be
// reported.
type responseRecorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if r.body.Len() < maxLoggedBody {
		r.body.Write(b)
	}
	return r.ResponseWriter.Write(b) //nolint:wrapcheck // passing through the underlying writer
}

// Flush keeps streaming responses working: the MCP handler streams events, and
// a wrapper hiding http.Flusher would buffer them until the response ended.
func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// waitForHealth blocks until the server answers, so an agent never starts
// against a socket that is not listening yet.
func waitForHealth(ctx context.Context, baseURL string) error {
	client := &http.Client{Timeout: time.Second}

	for range 50 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/healthz", nil)
		if err != nil {
			return fmt.Errorf("build health request: %w", err)
		}

		resp, err := client.Do(req)
		if err == nil {
			if closeErr := resp.Body.Close(); closeErr != nil {
				return fmt.Errorf("close health response: %w", closeErr)
			}
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return errors.New("server did not become healthy")
}

// fixedIDs returns identifiers that are stable across runs. Dev state is
// recreated on every start, so uniqueness only has to hold within one process.
func fixedIDs(id string) func() (string, error) {
	n := 0
	return func() (string, error) {
		n++
		switch n {
		case 1:
			return id + "-user", nil
		case 2:
			return id + "-integration", nil
		default:
			return id, nil
		}
	}
}

// portOf extracts ":8377" from an address like "127.0.0.1:8377".
func portOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ":" + addr
}

// closeStore logs a close failure, which is not actionable but is worth seeing.
func closeStore(db *store.Store, log *slog.Logger) {
	if err := db.Close(); err != nil {
		log.Error("close store", slog.Any("error", err))
	}
}
