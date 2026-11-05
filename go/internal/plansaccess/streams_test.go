package plansaccess

import (
	"encoding/json"
	"slices"
	"testing"
)

func streamTestPolicy() Policy {
	return Policy{Version: 1,
		Principals: []Principal{
			{ID: "david", Type: "person", Role: "owner", Projects: []string{"plans", "mail"}},
			{ID: "design", Type: "person", Role: "product", Projects: []string{"plans"}},
		},
		Agents: []Agent{
			{ID: "david-concierge", PrincipalID: "david", Kind: "concierge", Address: "people/david/concierge"},
			{ID: "design", PrincipalID: "design", Kind: "crew", Address: "people/design/crew", ProjectID: "plans"},
		},
		ProjectStreams: []ProjectStream{
			{ProjectID: "plans", Stream: "design", Decides: "agent:design", Informed: []string{"person:david"}},
			{ProjectID: "mail", Stream: "design", Decides: "person:david"},
		},
	}
}

func TestProjectStreamsStrictReaders(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Policy)
		valid  bool
	}{
		{"old format", func(p *Policy) { p.ProjectStreams = nil }, true},
		{"two scoped streams", func(*Policy) {}, true},
		{"disabled principal", func(p *Policy) { p.Principals[1].Disabled = true }, true},
		{"retired agent", func(p *Policy) { p.Agents[1].Disabled = true }, true},
		{"missing project", func(p *Policy) { p.ProjectStreams[0].ProjectID = "missing" }, false},
		{"aggregate workspace", func(p *Policy) {
			p.Principals[0].Projects = append(p.Principals[0].Projects, "projects")
			p.ProjectStreams[0].ProjectID = "projects"
		}, false},
		{"duplicate stream", func(p *Policy) { p.ProjectStreams = append(p.ProjectStreams, p.ProjectStreams[0]) }, false},
		{"invalid stream", func(p *Policy) { p.ProjectStreams[0].Stream = "bad/stream" }, false},
		{"untyped decider", func(p *Policy) { p.ProjectStreams[0].Decides = "design" }, false},
		{"unknown informed", func(p *Policy) { p.ProjectStreams[0].Informed = []string{"agent:missing"} }, false},
		{"duplicate informed", func(p *Policy) { p.ProjectStreams[0].Informed = []string{"agent:design", "agent:design"} }, false},
		{"foreign bound agent", func(p *Policy) { p.ProjectStreams[1].Decides = "agent:design" }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := streamTestPolicy()
			tc.change(&p)
			data, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			_, err = parse(data)
			if (err == nil) != tc.valid {
				t.Fatalf("valid = %t, want %t", err == nil, tc.valid)
			}
		})
	}
}

func TestProjectStreamRecipientsAreScopedAndRevocable(t *testing.T) {
	p := streamTestPolicy()
	got, found := p.ProjectStream("plans", "design")
	if !found || got.Decides != "agent:design" {
		t.Fatal("wrong Plans stream")
	}
	informed, ok := p.StreamRecipients(got)
	if !ok || !slices.Equal(informed, []string{"person:david", "agent:david-concierge"}) {
		t.Fatal("explicit delegation lost mandatory concierge")
	}
	mail, found := p.ProjectStream("mail", "design")
	if !found || mail.Decides != "person:david" {
		t.Fatal("project isolation failed")
	}
	if _, found := p.ProjectStream("mail", "security"); found {
		t.Fatal("absent definition invented a route")
	}
	p.Agents[1].Disabled = true
	if p.Validate() != nil {
		t.Fatal("retiring a delegate must not be blocked")
	}
	if _, ok := p.StreamRecipients(got); ok {
		t.Fatal("retired delegate resolved")
	}
	if _, ok := p.StreamRecipients(mail); !ok {
		t.Fatal("unrelated stream did not resolve")
	}
	p.Agents[0].Disabled = true
	if _, ok := p.StreamRecipients(mail); ok {
		t.Fatal("missing mandatory concierge")
	}
	if len(p.Principals[1].Delegations) != 0 || p.Agents[1].Allows("start", "david-concierge") {
		t.Fatal("routing granted authority")
	}
}
