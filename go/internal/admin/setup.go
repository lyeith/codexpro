package admin

import (
	"bytes"
	_ "embed"
	"html/template"
	"net"
	"strconv"
	"strings"

	"github.com/lyeith/codexpro/go/internal/wire"
)

//go:embed assets/setup.html
var setupHTML string

type choice struct {
	Value, Label string
	Selected     bool
}

var choiceLabels = map[string]string{"cloudflare": "Cloudflare quick tunnel", "ngrok": "ngrok stable URL", "cloudflare-named": "Cloudflare named tunnel", "tailscale": "Tailscale Funnel", "none": "Local only", "off": "Off", "mcp": "MCP isolated worktrees", "compact": "Compact", "minimal": "Minimal", "standard": "Standard", "full": "Full"}

func choices(kind, current string) []choice {
	values := []string{}
	switch kind {
	case "tunnel":
		values = tunnels
	case "worktreeMode":
		values = []string{"off", "mcp"}
	case "bashTranscript":
		values = []string{"compact", "full"}
	case "toolMode":
		values = []string{"minimal", "standard", "full"}
	}
	out := []choice{}
	for _, value := range values {
		out = append(out, choice{value, choiceLabels[value], value == current})
	}
	return out
}

var setupTemplate = template.Must(template.New("setup").Funcs(template.FuncMap{"choices": choices}).Parse(setupHTML))

func (s *Service) setupPage() (string, error) {
	profile, e := s.readProfile()
	if e != nil {
		return "", e
	}
	runtime, e := s.runtimeConnection()
	if e != nil {
		return "", e
	}
	host, port := s.hostPort()
	local := "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/mcp"
	endpoint := wire.Redact(wire.String(runtime, "endpoint", ""))
	display := endpoint
	if display != "" {
		glue := "?"
		if strings.Contains(display, "?") {
			glue = "&"
		}
		display += glue + "codexpro_token=<redacted>"
	}
	data := map[string]any{"Exists": wire.String(profile, "profilePath", "") != "", "Values": s.effectiveProfile(profile), "ProfilePath": s.ProfilePath(), "RuntimeEndpoint": endpoint, "RuntimeDisplay": display, "LocalMCP": local, "LocalDisplay": local + "?codexpro_token=<redacted>",
		"DefaultRoot": s.defaultRoot(), "DefaultProject": s.Workspaces.DefaultProject(), "ProjectsFile": s.Config.ProjectsFile, "ProjectCount": len(s.Workspaces.Projects()), "AuditMode": s.Config.String("auditMode", "metadata"), "ToolMode": s.Config.String("toolMode", "standard"), "WorktreeMode": s.Config.String("worktreeMode", "off"), "AllowedRoots": s.allowedRoots()}
	var b bytes.Buffer
	if e = setupTemplate.Execute(&b, data); e != nil {
		return "", e
	}
	return b.String(), nil
}
