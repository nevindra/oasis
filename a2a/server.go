package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/nevindra/oasis/core"
)

// Server exposes a core.Agent over the A2A protocol. It implements
// http.Handler; the application owns the listener, TLS, and middleware
// (auth verification is middleware — Oasis does not build an auth framework).
type Server struct {
	agent core.Agent
	card  AgentCard
	store TaskStore
	opts  serverOptions
	// pushClient delivers webhook notifications. It is the injected
	// WithPushHTTPClient client, or a bounded default built in NewServer —
	// never a shared package global, so concurrent servers (and tests) get
	// independent transports.
	pushClient *http.Client
	baseCtx    context.Context
	stop       context.CancelFunc
}

type serverOptions struct {
	url             string
	version         string
	skills          []AgentSkill
	securitySchemes map[string]SecurityScheme
	cardOverride    *AgentCard
	store           TaskStore
	pushEnabled     bool
	pushClient      *http.Client
	memoryCap       int
}

// ServerOption configures NewServer.
type ServerOption func(*serverOptions)

// WithURL sets the public endpoint URL advertised on the agent card.
// The card lists both JSONRPC and HTTP+JSON interfaces at this URL, since
// one Server mount speaks both.
func WithURL(u string) ServerOption { return func(o *serverOptions) { o.url = u } }

// WithVersion sets the agent version advertised on the card.
func WithVersion(v string) ServerOption { return func(o *serverOptions) { o.version = v } }

// WithSkill appends a skill to the agent card.
func WithSkill(s AgentSkill) ServerOption {
	return func(o *serverOptions) { o.skills = append(o.skills, s) }
}

// WithSecurityScheme declares an accepted auth scheme on the card.
// Verification itself is the application's HTTP middleware.
func WithSecurityScheme(name string, s SecurityScheme) ServerOption {
	return func(o *serverOptions) {
		if o.securitySchemes == nil {
			o.securitySchemes = make(map[string]SecurityScheme)
		}
		o.securitySchemes[name] = s
	}
}

// WithCard replaces the generated agent card entirely (power users).
func WithCard(c AgentCard) ServerOption { return func(o *serverOptions) { o.cardOverride = &c } }

// WithTaskStore replaces the bounded in-memory default.
func WithTaskStore(s TaskStore) ServerOption { return func(o *serverOptions) { o.store = s } }

// WithPushNotifications enables webhook delivery of task updates.
func WithPushNotifications() ServerOption { return func(o *serverOptions) { o.pushEnabled = true } }

// WithPushHTTPClient overrides the *http.Client used to POST webhook
// notifications. Inject one to control the transport, proxy, or timeout (the
// default client has a 10s timeout). A nil client is ignored — NewServer falls
// back to the bounded default. Tests pass a client wired to an httptest server.
func WithPushHTTPClient(c *http.Client) ServerOption {
	return func(o *serverOptions) {
		if c != nil {
			o.pushClient = c
		}
	}
}

// WithTaskCapacity bounds the in-memory task store (default 1024 tasks).
func WithTaskCapacity(n int) ServerOption { return func(o *serverOptions) { o.memoryCap = n } }

// NewServer wraps agent as an A2A server. The zero-config default serves
// JSON-RPC + SSE + REST with a bounded in-memory task store.
//
// Long-running servers should defer srv.Close() to cancel any background task
// runs that were started by non-blocking push requests.
func NewServer(agent core.Agent, opts ...ServerOption) *Server {
	var o serverOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.store == nil {
		o.store = newMemoryStore(o.memoryCap)
	}
	if o.pushClient == nil {
		o.pushClient = newDefaultPushClient()
	}
	baseCtx, stop := context.WithCancel(context.Background())
	return &Server{
		agent:      agent,
		card:       buildCard(agent.Name(), agent.Description(), &o),
		store:      o.store,
		opts:       o,
		pushClient: o.pushClient,
		baseCtx:    baseCtx,
		stop:       stop,
	}
}

// Close cancels all background task runs. In-flight blocking requests are
// unaffected (they ride their own HTTP request contexts). Safe to call
// multiple times.
func (s *Server) Close() { s.stop() }

// Card returns the agent card this server publishes.
func (s *Server) Card() AgentCard { return s.card }

// ServeHTTP routes A2A traffic. The dispatch order is:
//  1. GET /.well-known/agent-card.json → agent card (discovery).
//  2. REST binding (A2A v1.0 §11.3) — four wired routes reuse the JSON-RPC
//     handlers directly; only the four v1 routes are wired (list/subscribe/
//     push/extendedCard are deliberate YAGNI — documented in docs/a2a/).
//  3. Any other POST falls through to the JSON-RPC dispatcher — callers that
//     POST to "/" or any unknown path still reach serveJSONRPC, preserving
//     backward compatibility with existing JSON-RPC tests and deployments.
//
// Wrong-method hits on known REST paths return 405 before reaching the JSON-RPC
// fallback so clients get an actionable status, not a parse error.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// Well-known card: GET only.
	if path == WellKnownCardPath {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, s.card)
		return
	}

	// POST /message:send
	if path == restMessageSend {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		raw, rerr := readRESTBody(r)
		if rerr != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, rerr)
			return
		}
		result, hErr := s.handleMessageSend(r.Context(), raw)
		writeREST(w, result, hErr)
		return
	}

	// POST /message:stream (SSE — handleMessageStream owns the response writer)
	if path == restMessageStream {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		raw, rerr := readRESTBody(r)
		if rerr != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, rerr)
			return
		}
		// Why: pass raw params in an rpcRequest shell so handleMessageStream can
		// unmarshal sendParams without knowing the transport — the SSE writer and
		// frame format are shared between the JSON-RPC and REST bindings.
		s.handleMessageStream(w, r, rpcRequest{Params: raw})
		return
	}

	// /tasks/{id} and /tasks/{id}:cancel — extract the id from the path prefix.
	if strings.HasPrefix(path, "/tasks/") {
		id := strings.TrimPrefix(path, "/tasks/")

		// POST /tasks/{id}:cancel
		if strings.HasSuffix(id, ":cancel") {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			id = strings.TrimSuffix(id, ":cancel")
			if id == "" {
				writeREST(w, nil, &rpcError{Code: codeInvalidParams, Message: "invalid params: id required"})
				return
			}
			p, _ := json.Marshal(taskIDParams{ID: id})
			result, hErr := s.handleTasksCancel(r.Context(), p)
			writeREST(w, result, hErr)
			return
		}

		// GET /tasks/{id}
		// Any other suffix (e.g. ":subscribe", sub-resources) is not wired in v1.
		if !strings.Contains(id, "/") && !strings.Contains(id, ":") {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if id == "" {
				writeREST(w, nil, &rpcError{Code: codeInvalidParams, Message: "invalid params: id required"})
				return
			}
			p, _ := json.Marshal(taskIDParams{ID: id})
			result, hErr := s.handleTasksGet(r.Context(), p)
			writeREST(w, result, hErr)
			return
		}
	}

	// All other traffic: fall through to the JSON-RPC dispatcher so existing
	// JSON-RPC clients that POST to "/" continue to work without any change.
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.serveJSONRPC(w, r)
}

// serveJSONRPC decodes one JSON-RPC request and dispatches it. Streaming
// methods (SendStreamingMessage, SubscribeToTask) own the response writer for
// SSE; every other method returns a (result, *rpcError) pair wrapped in the
// JSON-RPC envelope. Agent failures are failed TASKs, not RPC errors, so the
// only RPC-level errors here are malformed requests and unsupported methods.
func (s *Server) serveJSONRPC(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	req, rerr := decodeRPCRequest(r.Body)
	if rerr != nil {
		writeJSON(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rerr})
		return
	}

	ctx := r.Context()
	switch req.Method {
	case methodSendStreamingMessage:
		s.handleMessageStream(w, r, req)
		return
	case methodSubscribeToTask:
		s.handleResubscribe(w, r, req)
		return
	}

	var (
		result any
		hErr   *rpcError
	)
	switch req.Method {
	case methodSendMessage:
		result, hErr = s.handleMessageSend(ctx, req.Params)
	case methodGetTask:
		result, hErr = s.handleTasksGet(ctx, req.Params)
	case methodCancelTask:
		result, hErr = s.handleTasksCancel(ctx, req.Params)
	case methodCreatePushConfig, methodGetPushConfig, methodListPushConfigs, methodDeletePushConfig:
		result, hErr = s.handlePushConfig(ctx, req.Method, req.Params)
	case methodListTasks, methodGetExtendedCard:
		result, hErr = s.handleUnsupportedVersion(ctx, req.Params) // documented YAGNI
	default:
		hErr = &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}
	}

	if hErr != nil {
		writeJSON(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: hErr})
		return
	}
	// Why: writeRPCResult marshals result once and writes the envelope with
	// raw w.Write calls, avoiding the second compact/copy pass that
	// json.NewEncoder(w).Encode(rpcResponse{Result: raw}) would perform.
	// For large artifact payloads (e.g. 1 MB base64 → ~1.33 MB JSON) this
	// eliminates ~1.33 MB of allocation per response.
	writeRPCResult(w, req.ID, result)
}

// newID generates a task/context/message identifier.
func newID() string { return uuid.NewString() }

// errorsAs is errors.As; aliased for brevity at translation call sites.
func errorsAs(err error, target any) bool { return errors.As(err, target) }

// writeJSON encodes v to w; encode failures are reported as 500 because
// there is nothing else observable to do mid-body.
func writeJSON(w http.ResponseWriter, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, "encode response: "+err.Error(), http.StatusInternalServerError)
	}
}

// restStatus maps an A2A/JSON-RPC error code to an HTTP status for the REST
// binding (A2A v1.0 §11.3, HTTP status recommendations).
func restStatus(code int) int {
	switch code {
	case codeTaskNotFound:
		return http.StatusNotFound
	case codeInvalidParams, codeParseError, codeInvalidRequest:
		return http.StatusBadRequest
	case codeUnsupportedOp, codeTaskNotCancelable, codePushNotSupported:
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// writeREST writes the REST response: on error, WriteHeader(restStatus) then
// the rpcError body; on success, 200 with the result. Content-Type is always
// application/json. The envelope is the bare result (no JSON-RPC wrapper) so
// clients parse the object directly.
func writeREST(w http.ResponseWriter, result any, rpcErr *rpcError) {
	w.Header().Set("Content-Type", "application/json")
	if rpcErr != nil {
		w.WriteHeader(restStatus(rpcErr.Code))
		writeJSON(w, rpcErr)
		return
	}
	writeJSON(w, result)
}

// readRESTBody reads the raw request body for a REST endpoint. It returns
// the bytes as json.RawMessage (ready for handler unmarshal) or an rpcError
// on decode failure. The body may be nil for POST routes that accept no body
// (e.g. :cancel); nil body yields an empty object so handlers can unmarshal
// without special-casing.
func readRESTBody(r *http.Request) (json.RawMessage, *rpcError) {
	if r.Body == nil || r.ContentLength == 0 {
		return json.RawMessage("{}"), nil
	}
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		return nil, &rpcError{Code: codeParseError, Message: "parse error: " + err.Error()}
	}
	return raw, nil
}
