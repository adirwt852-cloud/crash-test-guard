package protocol

// ProtocolVersion is the current agent protocol version.
const ProtocolVersion = "1.0"

// Status values shared across protocol responses.
const (
	StatusHealthy   = "healthy"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusResumed   = "resumed"
)

// HealthRequest represents a request to the GET /health endpoint.
type HealthRequest struct{}

// HealthResponse represents the response from GET /health.
type HealthResponse struct {
	ProtocolVersion string `json:"protocol_version"`
	AgentVersion    string `json:"agent_version"`
	Status          string `json:"status"`
}

// StartRequest represents the request body for POST /workflow/start.
type StartRequest struct {
	Input map[string]any `json:"input"`
}

// StartResponse represents the response from POST /workflow/start.
type StartResponse struct {
	WorkflowID string `json:"workflow_id"`
	Status     string `json:"status"`
}

// StateResponse represents the response from GET /workflow/{id}/state.
type StateResponse struct {
	WorkflowID     string         `json:"workflow_id"`
	Status         string         `json:"status"`
	CurrentStep    string         `json:"current_step"`
	CompletedSteps []string       `json:"completed_steps"`
	StepOutputs    map[string]any `json:"step_outputs"`
}

// CheckpointPayload is the opaque payload inside a checkpoint envelope.
// The payload is never interpreted by Crash-Test Guard.
type CheckpointPayload struct {
	Format string `json:"format"`
	Data   string `json:"data"` // base64-encoded opaque checkpoint bytes
}

// CheckpointEnvelope represents the response from GET /workflow/{id}/checkpoint.
type CheckpointEnvelope struct {
	ProtocolVersion string            `json:"protocol_version"`
	WorkflowID      string            `json:"workflow_id"`
	CheckpointID    string            `json:"checkpoint_id"`
	Sequence        int64             `json:"sequence"`
	CreatedAt       string            `json:"created_at"`
	Payload         CheckpointPayload `json:"payload"`
}

// Event represents a single execution event from GET /workflow/{id}/events.
type Event struct {
	Sequence  int64  `json:"sequence"`
	Type      string `json:"type"`
	StepID    string `json:"step_id"`
	Timestamp string `json:"timestamp"`
	Data      any    `json:"data"`
}

// ResumeRequest represents the request body for POST /workflow/{id}/resume.
type ResumeRequest struct {
	Checkpoint string `json:"checkpoint"` // base64-encoded checkpoint
}

// ResumeResponse represents the response from POST /workflow/{id}/resume.
type ResumeResponse struct {
	Status     string `json:"status"`
	WorkflowID string `json:"workflow_id"`
}

// ProblemDetails is an RFC 7807 problem details object used for error responses.
type ProblemDetails struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail"`
	Instance string `json:"instance"`
}
