package a2a

import (
	"encoding/json"
	"time"
)

// WellKnownCardPath is the RFC 8615 discovery path for the agent card.
// A2A v1.0 serves the AgentCard at this well-known URI.
const WellKnownCardPath = "/.well-known/agent-card.json"

// JSON-RPC method strings, pinned from A2A v1.0 §5.3 (Method Mapping
// Reference) and §9.4 (Core Methods). The JSON-RPC binding uses PascalCase
// method names matching the gRPC service methods — NOT the slash-delimited
// "message/send" form used by pre-1.0 drafts.
const (
	methodSendMessage          = "SendMessage"
	methodSendStreamingMessage = "SendStreamingMessage"
	methodGetTask              = "GetTask"
	methodListTasks            = "ListTasks"
	methodCancelTask           = "CancelTask"
	methodSubscribeToTask      = "SubscribeToTask"
	methodCreatePushConfig     = "CreateTaskPushNotificationConfig"
	methodGetPushConfig        = "GetTaskPushNotificationConfig"
	methodListPushConfigs      = "ListTaskPushNotificationConfigs"
	methodDeletePushConfig     = "DeleteTaskPushNotificationConfig"
	methodGetExtendedCard      = "GetExtendedAgentCard"
)

// REST (HTTP+JSON) URL patterns, pinned from A2A v1.0 §11.3. The "{id}"
// placeholders are filled with task IDs; the ":verb" suffix is the AIP-136
// custom-method convention the spec adopts.
const (
	restMessageSend   = "/message:send"                                  // POST
	restMessageStream = "/message:stream"                                // POST, SSE response
	restTaskGet       = "/tasks/{id}"                                    // GET
	restTaskList      = "/tasks"                                         // GET
	restTaskCancel    = "/tasks/{id}:cancel"                             // POST
	restTaskSubscribe = "/tasks/{id}:subscribe"                          // POST, SSE response. Why: the proto http annotation binds GET, but §5.3+§11.3 (the REST binding tables) bind POST — JSON binding wins.
	restPushConfigs   = "/tasks/{id}/pushNotificationConfigs"            // POST (create), GET (list)
	restPushConfig    = "/tasks/{id}/pushNotificationConfigs/{configId}" // GET, DELETE
	restExtendedCard  = "/extendedAgentCard"                             // GET
)

// TaskState is the lifecycle state of an A2A task. The JSON wire format uses
// the ProtoJSON enum representation: the SCREAMING_SNAKE_CASE proto constant
// name (A2A v1.0 §4.1.3), e.g. "TASK_STATE_WORKING" — not a lowercase alias.
type TaskState string

const (
	// TaskStateUnspecified is the zero/unknown state; never sent deliberately.
	TaskStateUnspecified TaskState = "TASK_STATE_UNSPECIFIED"
	// TaskStateSubmitted means the task was acknowledged but not yet started.
	TaskStateSubmitted TaskState = "TASK_STATE_SUBMITTED"
	// TaskStateWorking means the agent is actively processing the task.
	TaskStateWorking TaskState = "TASK_STATE_WORKING"
	// TaskStateCompleted means the task finished successfully. Terminal.
	TaskStateCompleted TaskState = "TASK_STATE_COMPLETED"
	// TaskStateFailed means the task ended in error. Terminal.
	TaskStateFailed TaskState = "TASK_STATE_FAILED"
	// TaskStateCanceled means the task was canceled before completion. Terminal.
	TaskStateCanceled TaskState = "TASK_STATE_CANCELED"
	// TaskStateInputRequired means the agent needs more input to proceed.
	// Interrupted, not terminal — the caller resumes with a follow-up message.
	TaskStateInputRequired TaskState = "TASK_STATE_INPUT_REQUIRED"
	// TaskStateRejected means the agent declined to perform the task. Terminal.
	TaskStateRejected TaskState = "TASK_STATE_REJECTED"
	// TaskStateAuthRequired means authentication is required to proceed.
	// Interrupted, not terminal.
	TaskStateAuthRequired TaskState = "TASK_STATE_AUTH_REQUIRED"
)

// Terminal reports whether the state is final — no further transitions are
// possible. The four terminal states (A2A v1.0) are completed, failed,
// canceled, and rejected. Interrupted states (input-required, auth-required)
// are NOT terminal: the task can still progress once the caller responds.
func (s TaskState) Terminal() bool {
	switch s {
	case TaskStateCompleted, TaskStateFailed, TaskStateCanceled, TaskStateRejected:
		return true
	}
	return false
}

// Role is the originator of an A2A message. Wire values are the ProtoJSON enum
// names (A2A v1.0 §4.1.5): "ROLE_USER" for client-to-agent, "ROLE_AGENT" for
// agent-to-client.
type Role string

const (
	// RoleUnspecified is the zero value; symmetry with TaskStateUnspecified.
	RoleUnspecified Role = "ROLE_UNSPECIFIED"
	// RoleUser marks a message from the client to the agent.
	RoleUser Role = "ROLE_USER"
	// RoleAgent marks a message from the agent to the client.
	RoleAgent Role = "ROLE_AGENT"
)

// Part is one content unit inside a Message or Artifact. Exactly one of Text,
// Raw, URL, Data is set — this mirrors the spec's "content" oneof. Data stays
// json.RawMessage so large structured payloads pass through without
// re-encoding (zero-copy passthrough is a framework performance requirement).
//
// On the wire Raw is base64-encoded (the proto "bytes" type); Go's encoding/json
// handles that automatically for the []byte field.
type Part struct {
	Text string          `json:"text,omitempty"`
	Raw  []byte          `json:"raw,omitempty"` // base64 on the wire
	URL  string          `json:"url,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`

	// MediaType is the MIME type of the part content (e.g. "image/png"); it
	// applies to any part type. Filename names a file part. Metadata carries
	// optional per-part extension data, passed through verbatim.
	MediaType string          `json:"mediaType,omitempty"`
	Filename  string          `json:"filename,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

// TextPart returns a Part holding plain text. Convenience constructor for the
// common case.
func TextPart(s string) Part { return Part{Text: s} }

// Message is a single communication turn between client and agent. MessageID
// is required by the spec and is set by whoever creates the message. ContextID
// and TaskID associate the message with a conversation and a task; for client
// messages both are optional, with the caveat that if both are provided they
// must be consistent.
type Message struct {
	MessageID        string          `json:"messageId"`
	ContextID        string          `json:"contextId,omitempty"`
	TaskID           string          `json:"taskId,omitempty"`
	Role             Role            `json:"role"`
	Parts            []Part          `json:"parts"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	Extensions       []string        `json:"extensions,omitempty"`
	ReferenceTaskIDs []string        `json:"referenceTaskIds,omitempty"`
}

// Artifact is a tangible output of a task — a file, structured data, or text.
// ArtifactID is unique within a task. Parts holds the artifact content (at
// least one). Extensions lists the extension URIs that contributed to this
// artifact; Metadata carries the extension payloads, passed through verbatim.
type Artifact struct {
	ArtifactID  string          `json:"artifactId,omitempty"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parts       []Part          `json:"parts"`
	Extensions  []string        `json:"extensions,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

// TaskStatus is the current state of a task plus an optional agent message
// (e.g. the prompt accompanying an input-required state) and the RFC 3339
// timestamp when the status was recorded.
type TaskStatus struct {
	State     TaskState `json:"state"`
	Message   *Message  `json:"message,omitempty"`
	Timestamp string    `json:"timestamp,omitempty"` // RFC 3339, UTC
}

// Task is a stateful unit of delegated work. It carries its current Status,
// the Artifacts produced so far, and the Message History. Metadata is custom
// per-task data, passed through verbatim.
type Task struct {
	ID        string          `json:"id"`
	ContextID string          `json:"contextId,omitempty"`
	Status    TaskStatus      `json:"status"`
	Artifacts []Artifact      `json:"artifacts,omitempty"`
	History   []Message       `json:"history,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

// TaskStatusUpdateEvent streams a task state change. TaskID and ContextID
// identify the task; Status carries the new state. Final is set on the last
// status event of a stream.
//
// ContextID is REQUIRED by proto field_behavior — omitempty is intentionally
// absent so a missing value round-trips faithfully.
type TaskStatusUpdateEvent struct {
	TaskID    string          `json:"taskId"`
	ContextID string          `json:"contextId"`
	Status    TaskStatus      `json:"status"`
	Final     bool            `json:"final,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

// TaskArtifactUpdateEvent streams an artifact chunk. Append signals that the
// artifact content should be appended to a previously sent artifact with the
// same ID; LastChunk marks the final chunk of that artifact.
type TaskArtifactUpdateEvent struct {
	TaskID    string          `json:"taskId"`
	ContextID string          `json:"contextId,omitempty"`
	Artifact  Artifact        `json:"artifact"`
	Append    bool            `json:"append,omitempty"`
	LastChunk bool            `json:"lastChunk,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

// StreamResponse wraps exactly one event payload per SSE frame (A2A v1.0
// "StreamResponse" oneof). It is the bare event object; on the JSON-RPC
// transport the server further wraps it in a JSON-RPC result envelope, while
// the REST transport and push-notification webhooks send it bare.
type StreamResponse struct {
	Task           *Task                    `json:"task,omitempty"`
	Message        *Message                 `json:"message,omitempty"`
	StatusUpdate   *TaskStatusUpdateEvent   `json:"statusUpdate,omitempty"`
	ArtifactUpdate *TaskArtifactUpdateEvent `json:"artifactUpdate,omitempty"`
}

// SendConfiguration carries per-request send options (A2A v1.0
// SendMessageConfiguration). It is accepted by Client.SendMessage and the
// underlying JSON-RPC envelope for both SendMessage and SendStreamingMessage.
//
// AcceptedOutputModes lists the MIME types the caller is willing to receive;
// the server filters artifacts to these types (omit for no restriction).
//
// Blocking controls server-side scheduling and is a tri-state pointer so the
// Go zero value (nil) means "use the protocol default", which is blocking —
// a zero-value SendConfiguration{} therefore blocks, matching the A2A spec.
// Set it explicitly with BlockingPtr / NonBlockingPtr:
//
//   - nil (the default): blocking. The server runs the task inline and returns
//     the settled result in the same HTTP response.
//   - *true: blocking, stated explicitly. Same behavior as nil.
//   - *false: non-blocking. The server starts the task in the background and
//     returns immediately with a working task — the caller must supply a
//     PushNotificationConfig to receive the eventual outcome, or poll GetTask.
//     The server rejects non-blocking sends without a push config.
//
// The wire key is "blocking" (A2A v1.0 SendMessageConfiguration.blocking); the
// pointer is omitted entirely when nil so the on-the-wire shape is unchanged
// from a plain bool field that defaults to blocking.
//
// PushNotificationConfig registers the webhook that receives the terminal
// StreamResponse when the task settles on the non-blocking path. The Token
// field is echoed back in the X-A2A-Notification-Token header so the receiver
// can authenticate the server.
//
// HistoryLength limits the number of history messages included in the returned
// Task; zero means unbounded.
type SendConfiguration struct {
	AcceptedOutputModes    []string                `json:"acceptedOutputModes,omitempty"`
	Blocking               *bool                   `json:"blocking,omitempty"`
	PushNotificationConfig *PushNotificationConfig `json:"pushNotificationConfig,omitempty"`
	HistoryLength          int                     `json:"historyLength,omitempty"`
}

// BlockingPtr returns a *bool set to true, for SendConfiguration.Blocking. It
// is equivalent to leaving the field nil (blocking is the default) but states
// the intent explicitly.
func BlockingPtr() *bool { b := true; return &b }

// NonBlockingPtr returns a *bool set to false, for SendConfiguration.Blocking.
// A non-blocking send requires a PushNotificationConfig (or polling GetTask)
// to retrieve the eventual result.
func NonBlockingPtr() *bool { b := false; return &b }

// isNonBlocking reports whether cfg explicitly opted out of blocking. nil cfg
// or nil/true Blocking means blocking (the default); only an explicit *false
// selects the background path.
func (cfg *SendConfiguration) isNonBlocking() bool {
	return cfg != nil && cfg.Blocking != nil && !*cfg.Blocking
}

// PushNotificationConfig registers a webhook for asynchronous task updates.
// The server POSTs StreamResponse payloads to URL when the task's state
// changes. ID identifies the config within a task; Token is an opaque value
// the client uses to validate incoming notifications.
type PushNotificationConfig struct {
	ID             string              `json:"id,omitempty"`
	URL            string              `json:"url"`
	Token          string              `json:"token,omitempty"`
	Authentication *AuthenticationInfo `json:"authentication,omitempty"`
}

// AuthenticationInfo describes webhook authentication. Scheme is an HTTP auth
// scheme name (e.g. "Bearer"); Credentials is the scheme-specific value.
type AuthenticationInfo struct {
	Scheme      string `json:"scheme,omitempty"`
	Credentials string `json:"credentials,omitempty"`
}

// Protocol binding strings for AgentInterface.ProtocolBinding (A2A v1.0 §4.4.6,
// §12.1). The field is an open string so custom bindings can declare their own
// values; these three are the officially supported core bindings.
const (
	// BindingJSONRPC selects the JSON-RPC 2.0 transport (§9).
	BindingJSONRPC = "JSONRPC"
	// BindingGRPC selects the gRPC transport (§10).
	BindingGRPC = "GRPC"
	// BindingHTTPJSON selects the HTTP+JSON/REST transport (§11).
	BindingHTTPJSON = "HTTP+JSON"
)

// AgentInterface declares a single endpoint at which the agent is reachable
// (A2A v1.0 §4.4.6, proto message AgentInterface ~line 336). An AgentCard
// carries an ordered list; the first entry is preferred. Exactly one of
// BindingJSONRPC, BindingGRPC, or BindingHTTPJSON (or a custom URI) must be
// set in ProtocolBinding.
type AgentInterface struct {
	// URL is the absolute HTTPS URL where this interface is available.
	// Required.
	URL string `json:"url"`
	// ProtocolBinding names the transport; use the Binding* constants or a
	// custom URI for extension bindings. Required.
	ProtocolBinding string `json:"protocolBinding"`
	// Tenant is an optional opaque routing identifier. When set, clients MUST
	// include this value in the tenant field of every request message.
	Tenant string `json:"tenant,omitempty"`
	// ProtocolVersion is the A2A protocol version exposed at this interface,
	// e.g. "1.0". Required.
	ProtocolVersion string `json:"protocolVersion"`
}

// AgentCard is the discovery document an A2A server publishes at
// WellKnownCardPath. It is a curated subset of the full v1.0 AgentCard
// (A2A v1.0 §4.4.1). Name, SupportedInterfaces, and Capabilities are the
// load-bearing fields; the rest describe the agent's skills and accepted
// I/O modes.
type AgentCard struct {
	Name                string                    `json:"name"`
	Description         string                    `json:"description,omitempty"`
	SupportedInterfaces []AgentInterface          `json:"supportedInterfaces,omitempty"`
	Version             string                    `json:"version,omitempty"`
	Capabilities        AgentCapabilities         `json:"capabilities"`
	SecuritySchemes     map[string]SecurityScheme `json:"securitySchemes,omitempty"`
	Skills              []AgentSkill              `json:"skills,omitempty"`
	DefaultInputModes   []string                  `json:"defaultInputModes,omitempty"`
	DefaultOutputModes  []string                  `json:"defaultOutputModes,omitempty"`
}

// AgentCapabilities declares optional protocol features the agent supports.
type AgentCapabilities struct {
	Streaming         bool `json:"streaming,omitempty"`
	PushNotifications bool `json:"pushNotifications,omitempty"`
	ExtendedAgentCard bool `json:"extendedAgentCard,omitempty"`
}

// SecurityScheme is a discriminated union of the five auth mechanisms defined
// in A2A v1.0 §4.5.1 (proto message SecurityScheme ~line 503). Exactly one
// pointer field must be set; the JSON key is the ProtoJSON oneof field name.
type SecurityScheme struct {
	APIKey        *APIKeySecurityScheme        `json:"apiKeySecurityScheme,omitempty"`
	HTTPAuth      *HTTPAuthSecurityScheme      `json:"httpAuthSecurityScheme,omitempty"`
	OAuth2        *OAuth2SecurityScheme        `json:"oauth2SecurityScheme,omitempty"`
	OpenIDConnect *OpenIDConnectSecurityScheme `json:"openIdConnectSecurityScheme,omitempty"`
	MTLS          *MTLSSecurityScheme          `json:"mtlsSecurityScheme,omitempty"`
}

// APIKeySecurityScheme defines API-key authentication (A2A v1.0 §4.5.2).
// Location is the parameter location: "query", "header", or "cookie".
// Name is the header/query/cookie parameter name.
type APIKeySecurityScheme struct {
	Description string `json:"description,omitempty"`
	Location    string `json:"location"`
	Name        string `json:"name"`
}

// HTTPAuthSecurityScheme defines HTTP authentication (A2A v1.0 §4.5.3),
// e.g. Basic or Bearer. Scheme is the IANA-registered auth scheme name
// (e.g. "Bearer"). BearerFormat is an optional hint (e.g. "JWT").
type HTTPAuthSecurityScheme struct {
	Description  string `json:"description,omitempty"`
	Scheme       string `json:"scheme"`
	BearerFormat string `json:"bearerFormat,omitempty"`
}

// OAuth2SecurityScheme defines OAuth 2.0 authentication (A2A v1.0 §4.5.4).
// Flows holds the flow configuration; it is modeled as json.RawMessage to
// pass through the nested OAuthFlows object without re-encoding.
// OAuth2MetadataURL is the RFC 8414 authorization server metadata URL.
type OAuth2SecurityScheme struct {
	Description       string          `json:"description,omitempty"`
	Flows             json.RawMessage `json:"flows"`
	OAuth2MetadataURL string          `json:"oauth2MetadataUrl,omitempty"`
}

// OpenIDConnectSecurityScheme defines OpenID Connect authentication
// (A2A v1.0 §4.5.5). OpenIDConnectURL is the OIDC Discovery URL.
type OpenIDConnectSecurityScheme struct {
	Description      string `json:"description,omitempty"`
	OpenIDConnectURL string `json:"openIdConnectUrl"`
}

// MTLSSecurityScheme defines mutual TLS authentication (A2A v1.0 §4.5.6).
type MTLSSecurityScheme struct {
	Description string `json:"description,omitempty"`
}

// AgentSkill describes one capability advertised on the card. ID and Name are
// required; Tags, Examples, and the per-skill I/O modes refine it.
type AgentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
	Examples    []string `json:"examples,omitempty"`
}

// nowRFC3339 stamps task status transitions with the current UTC time in
// RFC 3339 with nanosecond precision. Sub-second granularity is required
// because multiple status transitions within the same second (e.g. submitted
// → working → completed on a fast agent) must remain distinguishable in
// logs and streams.
func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }
