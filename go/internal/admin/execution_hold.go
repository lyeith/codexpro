package admin

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lyeith/codexpro/go/internal/jobs"
	"github.com/lyeith/codexpro/go/internal/wire"
)

const (
	maxExecutionHoldBody      = 4 << 10
	executionObserveTimeout   = 10 * time.Second
	executionHoldLocalScope   = "this_codexpro_instance_owned_execution"
	executionHoldEndpointPath = "/admin/execution-hold"
)

// ExecutionHoldController is the existing jobs owner's control boundary. The
// HTTP layer neither starts work nor manufactures owner/quiescence evidence.
type ExecutionHoldController interface {
	BeginExecutionHold(string) (jobs.ExecutionHoldStatus, error)
	HoldExecutionDispatch(string) (jobs.ExecutionHoldStatus, error)
	ResumeExecution(string) (jobs.ExecutionHoldStatus, error)
	ExecutionStatus() jobs.ExecutionHoldStatus
	ObserveExecutionHold(context.Context) (jobs.ExecutionHoldObservation, error)
}

// RegisterExecutionHoldRoutes must be mounted behind transport's current owner
// authorization. wire.Identity alone cannot establish the credential audience.
// GET is a pure status read; only an explicit observe POST inspects live owners.
func RegisterExecutionHoldRoutes(mux *http.ServeMux, owner ExecutionHoldController, options *HTTPOptions) {
	opts := HTTPOptions{}
	if options != nil {
		opts = *options
	}
	if opts.SameOrigin == nil {
		opts.SameOrigin = SameOrigin
	}
	mux.Handle(executionHoldEndpointPath, opts.SameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.URL.Path != executionHoldEndpointPath || r.URL.EscapedPath() != executionHoldEndpointPath {
			jsonError(w, http.StatusForbidden, "forbidden", "Access denied.", nil)
			return
		}
		id := wire.IdentityFrom(r.Context())
		if id.Source != "legacy" && (id.Source != "named" || id.PrincipalType != "person" ||
			id.Role != "owner" || id.AgentKind != "concierge" ||
			id.PrincipalID == "" || id.AgentID == "" || id.CredentialID == "") {
			jsonError(w, http.StatusForbidden, "forbidden", "Access denied.", nil)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			jsonError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET or POST for execution hold control.", nil)
			return
		}
		if r.Context().Err() != nil {
			jsonError(w, http.StatusRequestTimeout, "request_cancelled", "The request ended before execution control.", nil)
			return
		}
		if owner == nil {
			jsonError(w, http.StatusServiceUnavailable, "execution_control_unavailable", "Execution control is unavailable.", nil)
			return
		}
		if r.Method == http.MethodGet {
			writeExecutionHold(w, opts, owner.ExecutionStatus(), nil, nil)
			return
		}
		action, value, ok := readExecutionHoldRequest(w, r)
		if !ok {
			return
		}
		var status jobs.ExecutionHoldStatus
		var err error
		switch action {
		case "begin":
			status, err = owner.BeginExecutionHold(value)
		case "dispatch":
			status, err = owner.HoldExecutionDispatch(value)
		case "resume":
			status, err = owner.ResumeExecution(value)
		case "observe":
			status = owner.ExecutionStatus()
			if status.HoldID != value {
				writeExecutionHold(w, opts, status, nil, executionHoldConflict())
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), executionObserveTimeout)
			defer cancel()
			observation, observationErr := owner.ObserveExecutionHold(ctx)
			// The hold can change while a bounded live observation is in flight.
			// Never return a different hold's observation as this request's result.
			if observation.Status.HoldID != value {
				writeExecutionHold(w, opts, owner.ExecutionStatus(), nil, executionHoldConflict())
				return
			}
			writeExecutionHold(w, opts, observation.Status, &observation, observationErr)
			return
		}
		writeExecutionHold(w, opts, status, nil, err)
	})))
}

func readExecutionHoldRequest(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	invalid := func() (string, string, bool) {
		jsonError(w, http.StatusBadRequest, "invalid_request", "Use one execution action with its required request_key or hold_id.", nil)
		return "", "", false
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return invalid()
	}
	if r.ContentLength > maxExecutionHoldBody {
		jsonError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "Execution control request is too large.", nil)
		return "", "", false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxExecutionHoldBody+1))
	if err != nil {
		return invalid()
	}
	if len(body) > maxExecutionHoldBody {
		jsonError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "Execution control request is too large.", nil)
		return "", "", false
	}
	fields, ok := peopleObject(body, "action", "request_key", "hold_id")
	if !ok || len(fields) != 2 {
		return invalid()
	}
	var action, value string
	if json.Unmarshal(fields["action"], &action) != nil {
		return invalid()
	}
	key := "hold_id"
	switch action {
	case "begin":
		key = "request_key"
	case "dispatch", "resume", "observe":
	default:
		return invalid()
	}
	if json.Unmarshal(fields[key], &value) != nil || !utf8.ValidString(value) {
		return invalid()
	}
	if key == "request_key" {
		if strings.TrimSpace(value) == "" || strings.IndexByte(value, 0) >= 0 || utf8.RuneCountInString(value) > 160 {
			return invalid()
		}
	} else if decoded, err := hex.DecodeString(value); err != nil || len(value) != 32 || len(decoded) != 16 {
		return invalid()
	}
	return action, value, true
}

func executionHoldConflict() error {
	return wire.Errorf("execution_hold_conflict", "The original maintenance hold does not match.")
}

func writeExecutionHold(w http.ResponseWriter, opts HTTPOptions, status jobs.ExecutionHoldStatus, observation *jobs.ExecutionHoldObservation, err error) {
	code := http.StatusOK
	result := map[string]any{
		"ok":                 err == nil,
		"status":             status,
		"scope":              executionHoldLocalScope,
		"whole_host_barrier": false,
		"root_barrier":       false,
	}
	if observation != nil {
		result["observation"] = observation
	}
	if err != nil {
		errorCode, message := "execution_control_unavailable", "Execution control could not be confirmed; inspect the current status before deciding what to do."
		code = http.StatusServiceUnavailable
		var problem *wire.Error
		if errors.As(err, &problem) {
			switch problem.Code {
			case "execution_hold_conflict":
				code, errorCode, message = http.StatusConflict, problem.Code, "The original maintenance hold does not match."
			case "execution_hold_incomplete":
				code, errorCode, message = http.StatusConflict, problem.Code, "Accepted execution has not reached verified local quiescence."
			case "server_restarting":
				errorCode, message = problem.Code, "The server is restarting; inspect its current status before deciding what to do."
			}
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			errorCode, message = "execution_observation_incomplete", "The bounded owner observation did not complete."
		}
		result["error"] = map[string]any{"code": errorCode, "message": message}
	}
	sendJSON(w, code, publicValue(result, opts))
}
