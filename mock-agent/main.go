package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

var (
	mu        sync.Mutex
	workflows = map[string]*workflow{}
	counter   int
)

type workflow struct {
	id           string
	status       string
	currentStep  string
	completed    []string
	stepOutputs  map[string]any
	checkpointID string
	sequence     int64
	events       []event
	sideEffects  map[string]bool
	idempotency  map[string]string
	startedAt    time.Time
}

type event struct {
	Sequence  int64  `json:"sequence"`
	Type      string `json:"type"`
	StepID    string `json:"step_id"`
	Timestamp string `json:"timestamp"`
	Data      any    `json:"data"`
}

var steps = []string{"read_email", "generate_invoice", "process_payment", "send_confirmation", "complete_task"}

func newWorkflow() *workflow {
	mu.Lock()
	defer mu.Unlock()
	counter++
	id := fmt.Sprintf("wf-%d", counter)
	w := &workflow{
		id:          id,
		status:      "running",
		currentStep: steps[0],
		completed:   []string{},
		stepOutputs: map[string]any{},
		sideEffects: map[string]bool{},
		idempotency: map[string]string{},
		startedAt:   time.Now(),
	}
	workflows[id] = w
	go runWorkflow(w)
	return w
}

func runWorkflow(w *workflow) {
	for i, step := range steps {
		time.Sleep(200 * time.Millisecond)
		mu.Lock()
		w.currentStep = step
		// side effect tracking
		key := step + "-key"
		if _, ok := w.idempotency[key]; !ok {
			w.idempotency[key] = fmt.Sprintf("effect-%s-%d", step, i)
		}
		w.sideEffects[step] = true
		w.completed = append(w.completed, step)
		w.stepOutputs[step] = map[string]any{"done": true}
		w.sequence++
		w.events = append(w.events, event{
			Sequence:  w.sequence,
			Type:      "step_completed",
			StepID:    step,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Data:      map[string]any{"step": step},
		})
		// checkpoint after each step
		w.checkpointID = fmt.Sprintf("cp-%d", w.sequence)
		w.status = "running"
		if i == len(steps)-1 {
			w.status = "completed"
			w.currentStep = ""
		}
		mu.Unlock()
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"protocol_version": "1.0",
		"agent_version":    "test-1.0",
		"status":           "healthy",
	})
}

func handleStart(w http.ResponseWriter, r *http.Request) {
	wf := newWorkflow()
	writeJSON(w, map[string]any{
		"workflow_id": wf.id,
		"status":      "running",
	})
}

func getWorkflow(id string) *workflow {
	mu.Lock()
	defer mu.Unlock()
	return workflows[id]
}

func handleState(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wf := getWorkflow(id)
	if wf == nil {
		http.Error(w, "not found", 404)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	writeJSON(w, map[string]any{
		"workflow_id":     wf.id,
		"status":          wf.status,
		"current_step":    wf.currentStep,
		"completed_steps": wf.completed,
		"step_outputs":    wf.stepOutputs,
	})
}

func handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wf := getWorkflow(id)
	if wf == nil {
		http.Error(w, "not found", 404)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	payload := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("checkpoint:%s:seq=%d", wf.id, wf.sequence)))
	writeJSON(w, map[string]any{
		"protocol_version": "1.0",
		"workflow_id":      wf.id,
		"checkpoint_id":    wf.checkpointID,
		"sequence":         wf.sequence,
		"created_at":       time.Now().UTC().Format(time.RFC3339),
		"payload": map[string]any{
			"format": "json",
			"data":   payload,
		},
	})
}

func handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wf := getWorkflow(id)
	if wf == nil {
		http.Error(w, "not found", 404)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	writeJSON(w, wf.events)
}

func handleResume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wf := getWorkflow(id)
	if wf == nil {
		http.Error(w, "not found", 404)
		return
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	mu.Lock()
	wf.status = "resumed"
	mu.Unlock()
	writeJSON(w, map[string]any{
		"status":      "resumed",
		"workflow_id": wf.id,
	})
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("POST /workflow/start", handleStart)
	mux.HandleFunc("GET /workflow/{id}/state", handleState)
	mux.HandleFunc("GET /workflow/{id}/checkpoint", handleCheckpoint)
	mux.HandleFunc("GET /workflow/{id}/events", handleEvents)
	mux.HandleFunc("POST /workflow/{id}/resume", handleResume)
	fmt.Println("mock-agent listening on :8080")
	http.ListenAndServe(":8080", mux)
}
