package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSavedToolInventoryDoesNotChangeRuntimePermissionOrRegistration(t *testing.T) {
	for _, tier := range []string{"minimal", "standard", "full"} {
		t.Run(tier, func(t *testing.T) {
			s, _, _ := fixture(t)
			mux := http.NewServeMux()
			s.RegisterRoutes(mux, nil)
			before := s.Registry.Names()
			response := request(mux, "POST", "/admin/profile", fmt.Sprintf(`{"toolMode":%q,"tunnel":"none"}`, tier), "")
			if response.Code != 200 {
				t.Fatalf("tier %s was rejected: status=%d %s", tier, response.Code, response.Body.String())
			}
			result := decode(t, response)
			effective := result["effective"].(map[string]any)
			runtime := result["runtime"].(map[string]any)
			if effective["toolMode"] != tier {
				t.Fatalf("saved tier missing: %#v", effective["toolMode"])
			}
			if runtime["toolMode"] != "standard" || s.Config.String("toolMode", "") != "standard" || len(s.Registry.Names()) != len(before) {
				t.Fatal("saving an inventory tier changed runtime registration")
			}
			if effective["bash"] != "full" || effective["write"] != "workspace" || runtime["bashMode"] != "full" || runtime["writeMode"] != "workspace" {
				t.Fatal("inventory selection changed full permissions")
			}
			data, err := os.ReadFile(s.ProfilePath())
			if err != nil {
				t.Fatal(err)
			}
			var saved map[string]any
			if err = json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			if saved["toolMode"] != tier {
				t.Fatalf("persisted tier = %#v", saved["toolMode"])
			}
			fetched := decode(t, request(mux, "GET", "/admin/profile", "", ""))
			if fetched["effective"].(map[string]any)["toolMode"] != tier {
				t.Fatal("saved tier not visible after re-read")
			}
			setup := request(mux, "GET", "/setup", "", "")
			if setup.Code != 200 || !strings.Contains(setup.Body.String(), `name="toolMode"`) || !strings.Contains(setup.Body.String(), fmt.Sprintf(`<option value="%s" selected>`, tier)) {
				t.Fatal("setup did not render selected saved tier")
			}
		})
	}
}

func TestExistingEmptyProfileHasIdentityAndMissingObjectsRemainAbsent(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		exists     bool
	}{
		{"missing", "", false}, {"empty object", "{}", true}, {"null", "null", false}, {"array", "[]", false}, {"number", "7", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := fixture(t)
			if tc.body != "" {
				if err := os.MkdirAll(filepath.Dir(s.ProfilePath()), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(s.ProfilePath(), []byte(tc.body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			mux := http.NewServeMux()
			s.RegisterRoutes(mux, nil)
			response := request(mux, "GET", "/admin/profile", "", "")
			if response.Code != 200 {
				t.Fatalf("GET status = %d", response.Code)
			}
			result := decode(t, response)
			if result["exists"] != tc.exists {
				t.Fatalf("exists = %#v, want %t", result["exists"], tc.exists)
			}
			profile, ok := result["profile"].(map[string]any)
			if !ok {
				t.Fatal("profile must always remain an object")
			}
			_, hasPath := profile["profilePath"]
			if hasPath != tc.exists {
				t.Fatalf("profile path presence = %t, want %t", hasPath, tc.exists)
			}
			runtime, ok := result["runtime_connection"].(map[string]any)
			if !ok || len(runtime) != 0 {
				t.Fatalf("missing runtime must be {}, got %#v", result["runtime_connection"])
			}
		})
	}
}

func TestEmptyAndNonObjectRuntimeRecordsReturnAnObject(t *testing.T) {
	for _, body := range []string{"{}", "null", "[]", "0"} {
		t.Run(body, func(t *testing.T) {
			s, _, _ := fixture(t)
			if err := os.MkdirAll(filepath.Dir(s.runtimePath()), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s.runtimePath(), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			value, err := s.runtimeConnection()
			if err != nil {
				t.Fatal(err)
			}
			if value == nil || len(value) != 0 {
				t.Fatalf("runtime connection = %#v", value)
			}
		})
	}
}
