package main

import (
	"github.com/lyeith/codexpro/go/internal/admin"
	"github.com/lyeith/codexpro/go/internal/engine"
)

// HTTP config echoes must discover nested workspace roots before config
// fallbacks, just like src/pathLabels.ts redactConfigPaths(config, value).
func adminHTTPOptions(redactor engine.Redactor) *admin.HTTPOptions {
	return &admin.HTTPOptions{
		RedactValue: func(value any, unknown bool) any {
			return redactor.Value(value, redactor.Labels(value), unknown)
		},
		RedactText: func(value string) string {
			return redactor.Text(value, redactor.Labels(nil))
		},
	}
}
