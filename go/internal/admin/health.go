package admin

import (
	"errors"
	"net/http"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/httpcompat"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

// NewHealthHandler needs only validated configuration and the loaded catalog.
// It shares the steady route implementation, including exact body and path
// redaction, without opening audit, job, archive or analysis services.
func NewHealthHandler(cfg *config.Config, manager *workspace.Manager, options *HTTPOptions) (http.Handler, error) {
	if cfg == nil || manager == nil {
		return nil, errors.New("health requires configuration and project catalog")
	}
	return (&Service{Config: cfg, Workspaces: manager}).HealthHandler(options), nil
}

func (s *Service) HealthHandler(options *HTTPOptions) http.Handler {
	opts := HTTPOptions{}
	if options != nil {
		opts = *options
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			httpcompat.NotFound(w, r)
			return
		}
		sendJSON(w, http.StatusOK, publicValue(s.Health(), opts))
	})
}
