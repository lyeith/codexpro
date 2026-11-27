package transport

import (
	"encoding/json"

	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// discoveryCacheTTLMs is the 2026-07-28 client-side cache hint for static
	// discovery and tool-list metadata (5 minutes, scoped per credential).
	discoveryCacheTTLMs = 300_000
	discoveryCacheScope = "private"
)

type rawResult struct {
	mcp.ResultBase
	Value any
	// complete marks a 2026-07-28 result. The SDK can stamp only its own
	// result types, so the raw envelope adds resultType and the _meta the SDK
	// set (serverInfo) itself. Older protocols get the envelope unchanged.
	complete  bool
	cacheable *mcp.Cacheable
}

func mergeMeta(existing map[string]any, extra mcp.Meta) map[string]any {
	meta := make(map[string]any, len(existing)+len(extra))
	for k, v := range existing {
		meta[k] = v
	}
	for k, v := range extra {
		if _, ok := meta[k]; !ok {
			meta[k] = v
		}
	}
	return meta
}

func (r *rawResult) MarshalJSON() ([]byte, error) {
	if !r.complete && len(r.Meta) == 0 && r.cacheable == nil {
		return json.Marshal(r.Value)
	}
	switch val := r.Value.(type) {
	case wire.Result:
		out := map[string]any{"content": val.Content}
		if len(r.Meta) != 0 {
			out["_meta"] = mergeMeta(val.Meta, r.Meta)
		} else if val.Meta != nil {
			out["_meta"] = val.Meta
		}
		if val.StructuredContent != nil {
			out["structuredContent"] = val.StructuredContent
		}
		if val.IsError {
			out["isError"] = true
		}
		if r.complete {
			out["resultType"] = "complete"
		}
		if r.cacheable != nil {
			out["ttlMs"] = r.cacheable.TTLMs
			if r.cacheable.CacheScope != "" {
				out["cacheScope"] = r.cacheable.CacheScope
			}
		}
		return json.Marshal(out)
	case map[string]any:
		out := make(map[string]any, len(val)+4)
		for k, v := range val {
			out[k] = v
		}
		if len(r.Meta) != 0 {
			existing, _ := out["_meta"].(map[string]any)
			out["_meta"] = mergeMeta(existing, r.Meta)
		}
		if r.complete {
			out["resultType"] = "complete"
		}
		if r.cacheable != nil {
			out["ttlMs"] = r.cacheable.TTLMs
			if r.cacheable.CacheScope != "" {
				out["cacheScope"] = r.cacheable.CacheScope
			}
		}
		return json.Marshal(out)
	}
	raw, err := json.Marshal(r.Value)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return raw, nil
	}
	if len(r.Meta) != 0 {
		meta := map[string]any{}
		if existing, ok := fields["_meta"]; ok {
			_ = json.Unmarshal(existing, &meta)
		}
		if fields["_meta"], err = json.Marshal(mergeMeta(meta, r.Meta)); err != nil {
			return nil, err
		}
	}
	if r.complete {
		fields["resultType"] = json.RawMessage(`"complete"`)
	}
	if r.cacheable != nil {
		if fields["ttlMs"], err = json.Marshal(r.cacheable.TTLMs); err != nil {
			return nil, err
		}
		if r.cacheable.CacheScope != "" {
			if fields["cacheScope"], err = json.Marshal(r.cacheable.CacheScope); err != nil {
				return nil, err
			}
		}
	}
	return json.Marshal(fields)
}

type mcpEnvelopePeek struct {
	ID     any             `json:"id"`
	Method json.RawMessage `json:"method"`
	Params json.RawMessage `json:"params"`
}

var legacyProtocolMarker = []byte("2024-10-07")

// restrictedInstructions replace the server guidance for read-only credentials.
const restrictedInstructions = "Use list_projects to find granted workspace IDs for file tools and literal text search. Use a project_id with activity_list and its action_id with activity_get."

// modernProtocol is the sessionless MCP revision. The SDK serves it only on a
// stateless handler and never negotiates it through initialize.
const modernProtocol = "2026-07-28"

func usesModernProtocol(request mcp.Request) bool {
	session, ok := request.GetSession().(*mcp.ServerSession)
	if !ok || session == nil {
		return false
	}
	params := session.InitializeParams()
	return params != nil && params.ProtocolVersion >= modernProtocol
}
