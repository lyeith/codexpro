package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"unicode/utf8"

	"github.com/lyeith/codexpro/go/internal/wire"
)

const maxPeopleBody = 32 << 10

// RegisterPeopleRoutes receives a fixed local owner, never a client-selected
// executable, policy path, actor, or generic process runner.
func RegisterPeopleRoutes(mux *http.ServeMux, owner PeopleOwner, sameOrigin func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("/people", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/people" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			peopleMethodError(w, "GET, HEAD")
			return
		}
		if _, ok := peopleHTTPActor(r); !ok {
			SendPeoplePage(w, r, http.StatusUnauthorized)
			return
		}
		SendPeoplePage(w, r, http.StatusOK)
	})
	mux.HandleFunc("/people/data", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			peopleMethodError(w, "GET")
			return
		}
		actor, ok := peopleHTTPActor(r)
		if !ok {
			peopleHTTPFailure(w, "forbidden")
			return
		}
		if owner == nil {
			peopleHTTPFailure(w, "unavailable")
			return
		}
		response := owner.Execute(r.Context(), PeopleRequest{Version: 1, Actor: actor, Operation: "read"})
		writePeopleResponse(w, response)
	})
	apply := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			peopleMethodError(w, "POST")
			return
		}
		actor, ok := peopleHTTPActor(r)
		if !ok {
			peopleHTTPFailure(w, "forbidden")
			return
		}
		if sameOrigin != nil && !sameOrigin(w, r) {
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			peopleHTTPFailure(w, "invalid_request")
			return
		}
		if r.ContentLength > maxPeopleBody {
			jsonError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "People request is too large.", nil)
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, maxPeopleBody+1))
		if err != nil {
			peopleHTTPFailure(w, "invalid_request")
			return
		}
		if len(data) > maxPeopleBody {
			jsonError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "People request is too large.", nil)
			return
		}
		if !validPeopleAction(data) {
			peopleHTTPFailure(w, "invalid_request")
			return
		}
		if owner == nil {
			peopleHTTPFailure(w, "unavailable")
			return
		}
		response := owner.Execute(r.Context(), PeopleRequest{Version: 1, Actor: actor, Operation: "apply", Action: data})
		writePeopleResponse(w, response)
	})
	if sameOrigin == nil {
		mux.Handle("/people/actions", SameOrigin(apply))
	} else {
		mux.Handle("/people/actions", apply)
	}
}

func peopleHTTPActor(r *http.Request) (PeopleActor, bool) {
	id := wire.IdentityFrom(r.Context())
	if id.Source != "named" || id.PrincipalType != "person" || id.Role != "owner" || id.AgentKind != "concierge" ||
		id.PrincipalID == "" || id.AgentID == "" || id.CredentialID == "" {
		return PeopleActor{}, false
	}
	return PeopleActor{PrincipalID: id.PrincipalID, AgentID: id.AgentID, CredentialID: id.CredentialID}, true
}

func peopleMethodError(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	jsonError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.", nil)
}

func peopleHTTPFailure(w http.ResponseWriter, code string) {
	writePeopleResponse(w, peopleFailure(code))
}

func writePeopleResponse(w http.ResponseWriter, response PeopleResponse) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if response.OK {
		sendJSON(w, http.StatusOK, response)
		return
	}
	code := "unavailable"
	if response.Error != nil {
		code = response.Error.Code
	}
	status, message := http.StatusServiceUnavailable, "People administration is unavailable."
	switch code {
	case "invalid_request":
		status, message = http.StatusBadRequest, "Check the requested action and its fields."
	case "forbidden":
		status, message = http.StatusForbidden, "Your current access does not allow this action."
	case "not_found":
		status, message = http.StatusNotFound, "The requested record is no longer available."
	case "conflict":
		status, message = http.StatusConflict, "This record changed. Refresh before submitting again."
	case "uncertain":
		message = "The result could not be confirmed. Refresh to inspect the current state before deciding what to do."
	case "execution_held":
		message = "Execution is held for maintenance. No policy change was accepted."
	case "unavailable":
	default:
		code = "unavailable"
	}
	safe := peopleFailure(code)
	safe.Error.Message = message
	sendJSON(w, status, safe)
}

// This is the consumer's finite HTTP shape. The plans-access owner separately
// performs strict recursive decoding and all policy validation under its lock.
func validPeopleAction(data []byte) bool {
	object, ok := peopleObject(data, "kind", "id", "role", "agent", "expected")
	if !ok {
		return false
	}
	var kind string
	if json.Unmarshal(object["kind"], &kind) != nil {
		return false
	}
	required := []string{"kind"}
	switch kind {
	case "add_person":
		required = append(required, "id", "role")
	case "change_role":
		required = append(required, "id", "role", "expected")
	case "disable_person", "retire_agent", "revoke_credential":
		required = append(required, "id", "expected")
	case "register_agent":
		required = append(required, "agent", "expected")
	default:
		return false
	}
	if len(object) != len(required) {
		return false
	}
	for _, key := range required {
		if _, found := object[key]; !found {
			return false
		}
	}
	for _, key := range []string{"id", "role"} {
		if raw, found := object[key]; found {
			var value string
			if json.Unmarshal(raw, &value) != nil || value == "" {
				return false
			}
		}
	}
	if kind == "register_agent" {
		agent, valid := peopleObject(object["agent"], "id", "principal_id", "kind", "address", "project_id")
		if !valid || len(agent) < 4 {
			return false
		}
		for _, key := range []string{"id", "principal_id", "kind", "address"} {
			var value string
			if json.Unmarshal(agent[key], &value) != nil || value == "" {
				return false
			}
		}
		if raw, found := agent["project_id"]; found {
			var value string
			if json.Unmarshal(raw, &value) != nil {
				return false
			}
		}
	}
	if kind == "add_person" {
		return true
	}
	var keys []string
	var target any
	switch kind {
	case "change_role", "disable_person", "register_agent":
		keys = []string{"id", "type", "role", "projects", "disabled", "has_delegations"}
		target = &PeoplePrincipal{}
	case "retire_agent":
		keys = []string{"id", "principal_id", "kind", "address", "project_id", "disabled"}
		target = &PeopleAgent{}
	case "revoke_credential":
		keys = []string{"id", "principal_id", "agent_id", "device", "audience", "revoked", "can_revoke"}
		target = &PeopleDevice{}
	}
	expected, valid := peopleObject(object["expected"], keys...)
	if !valid || len(expected) != len(keys) || json.Unmarshal(object["expected"], target) != nil {
		return false
	}
	if raw, found := expected["projects"]; found {
		var projects []json.RawMessage
		if json.Unmarshal(raw, &projects) != nil {
			return false
		}
		for _, project := range projects {
			var id string
			if bytes.Equal(bytes.TrimSpace(project), []byte("null")) || json.Unmarshal(project, &id) != nil {
				return false
			}
		}
	}
	return true
}

func peopleObject(data []byte, allowed ...string) (map[string]json.RawMessage, bool) {
	if !utf8.Valid(data) {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		fields[key] = true
	}
	out := map[string]json.RawMessage{}
	for decoder.More() {
		token, err = decoder.Token()
		key, stringKey := token.(string)
		if err != nil || !stringKey || !fields[key] {
			return nil, false
		}
		if _, duplicate := out[key]; duplicate {
			return nil, false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, false
		}
		out[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, false
	}
	return out, true
}
