package activity

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnknownProjectPrecedesInvalidNumericOptions(t *testing.T) {
	h := testHandler(t, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/activity.json?project_id=missing&limit=0", nil))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "unknown_project") {
		t.Fatalf("validation precedence changed: %d %s", rec.Code, rec.Body.String())
	}
}
