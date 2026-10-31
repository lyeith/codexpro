package search

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type StructuredMatch struct {
	Path       string   `json:"path"`
	Line       int      `json:"line"`
	Text       string   `json:"text"`
	Group      string   `json:"group"`
	Score      int      `json:"score"`
	Reasons    []string `json:"reasons"`
	Confidence string   `json:"confidence"`
	Source     string   `json:"source"`
}
type StructuredResult struct {
	SchemaVersion int                          `json:"schemaVersion"`
	Query         string                       `json:"query"`
	Intent        string                       `json:"intent"`
	Groups        map[string][]StructuredMatch `json:"groups"`
	Matches       []StructuredMatch            `json:"matches"`
	Coverage      Coverage                     `json:"coverage"`
	Warnings      []string                     `json:"warnings"`
	Cache         AnalysisCache                `json:"cache"`
}

var groupOrder = []string{"definitions", "references", "tests", "configuration", "documentation", "other"}

func emptyGroups() map[string][]StructuredMatch {
	m := map[string][]StructuredMatch{}
	for _, g := range groupOrder {
		m[g] = []StructuredMatch{}
	}
	return m
}

var symbolQuery = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)

func searchIntent(query, requested string, regex bool) string {
	if requested != "" && requested != "auto" {
		return requested
	}
	if !regex && symbolQuery.MatchString(query) {
		return "symbol"
	}
	return "text"
}
func (s *Service) structured(ctx context.Context, w wire.Workspace, o options) *StructuredResult {
	result := &StructuredResult{SchemaVersion: 1, Query: o.Query, Intent: o.Intent, Groups: emptyGroups(), Matches: []StructuredMatch{}, Coverage: Coverage{Truncated: true, Warnings: []string{}}, Warnings: []string{}, Cache: AnalysisCache{false, "unavailable"}}
	if result.Intent == "" || result.Intent == "auto" {
		result.Intent = "text"
	}
	if !s.Config.Bool("analysisEnabled", true) {
		result.Cache.Key = "disabled"
		result.Warnings = []string{"Repository analysis is disabled by configuration."}
		result.Coverage.Warnings = result.Warnings
		return result
	}
	actual, e := s.structuredSearch(ctx, w, o)
	if e == nil {
		return actual
	}
	result.Warnings = []string{"Repository analysis unavailable: " + wire.Redact(e.Error())}
	return result
}
func (s *Service) structuredSearch(ctx context.Context, w wire.Workspace, o options) (*StructuredResult, error) {
	query := files.TrimJS(o.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required.")
	}
	analysis, e := s.Analyze(ctx, w)
	if e != nil {
		return nil, e
	}
	result := &StructuredResult{SchemaVersion: 1, Query: query, Intent: searchIntent(query, o.Intent, o.Regex), Groups: emptyGroups(), Matches: []StructuredMatch{}, Coverage: analysis.Coverage, Warnings: append([]string{}, analysis.Warnings...), Cache: analysis.Cache}
	limit := max(1, min(s.Config.Int("maxSearchResults", 200), o.Max))
	candidates := max(limit, min(limit*4, 20000))
	limits := s.analysisLimits()
	prefix := ""
	if files.TrimJS(o.Root) != "" {
		_, prefix, e = s.Files.ResolvePath(w, o.Root, false)
		if e != nil {
			return nil, e
		}
		if prefix == "." {
			prefix = ""
		}
	}
	if o.Regex {
		result.Warnings = append(result.Warnings, "Grouped results are unavailable for regular expression searches. Lexical regex matching remains delegated to ripgrep.")
		result.Coverage.Truncated = true
		result.Coverage.Warnings = result.Warnings
		return result, nil
	}
	definitions := map[string]map[int]Symbol{}
	for _, sym := range analysis.Symbols {
		if definitions[sym.Path] == nil {
			definitions[sym.Path] = map[int]Symbol{}
		}
		definitions[sym.Path][sym.Line] = sym
	}
	lowered := strings.ToLower(query)
	matches := []StructuredMatch{}
	scanned, bytesRead, skipped := 0, 0, 0
	budgetReached, candidateReached := false, false
scan:
	for _, f := range analysis.Files {
		if f.Generated || !o.IncludeTests && f.Role == "test" {
			continue
		}
		if !scoped(prefix, f.Path) && !(o.IncludeTests && f.Role == "test") {
			continue
		}
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		if scanned >= limits.MaxAnalyzedFiles || int64(bytesRead)+f.Bytes > int64(limits.MaxScannedBytes) {
			budgetReached = true
			break
		}
		abs, _, e := s.Files.ResolvePath(w, f.Path, false)
		var b []byte
		if e == nil {
			b, e = readAnalysisBytes(abs, limits.MaxScannedBytes-bytesRead)
		}
		if e != nil {
			skipped++
			continue
		}
		text := files.DecodeText(b)
		if bytesRead+len(text) > limits.MaxScannedBytes {
			budgetReached = true
			break
		}
		scanned++
		bytesRead += len(text)
		for i, line := range analysisLines(text) {
			if !strings.Contains(strings.ToLower(line), lowered) {
				continue
			}
			sym, found := definitions[f.Path][i+1]
			definition := found && strings.ToLower(sym.Name) == lowered
			group := "references"
			reasons := []string{"exact text match"}
			score, confidence := 100, "exact"
			if definition {
				group = "definitions"
				reasons = append(reasons, "symbol definition")
				score = 190
				confidence = "strong"
			} else {
				switch f.Role {
				case "test":
					group = "tests"
					reasons = append(reasons, "related test")
					score = 160
				case "config":
					group = "configuration"
				case "docs":
					group = "documentation"
				}
			}
			if len(matches) >= candidates {
				candidateReached = true
				break scan
			}
			matches = append(matches, StructuredMatch{f.Path, i + 1, wire.Redact(files.JSSlice(files.TrimJS(line), 0, 400)), group, score, reasons, confidence, "built-in analysis"})
		}
	}
	if budgetReached {
		result.Warnings = append(result.Warnings, "Grouped search reached its configured file or byte limit.")
	}
	if skipped > 0 {
		suffix := "s"
		if skipped == 1 {
			suffix = ""
		}
		result.Warnings = append(result.Warnings, fmt.Sprintf("Grouped search skipped %d file%s that changed or became unreadable during analysis.", skipped, suffix))
	}
	if result.Intent == "references" || result.Intent == "impact" {
		paths := map[string]bool{}
		for _, sym := range analysis.Symbols {
			if strings.ToLower(sym.Name) == lowered {
				paths[sym.Path] = true
			}
		}
		for _, rel := range analysis.Relationships {
			if !paths[rel.To] {
				continue
			}
			group, reason, score := "references", "dependent module", 165
			if rel.Kind == "tests" {
				group, reason, score = "tests", "dependent test", 170
			}
			if group == "tests" && !o.IncludeTests {
				continue
			}
			existing := -1
			for i, m := range matches {
				if m.Path == rel.From && m.Group == group {
					existing = i
					break
				}
			}
			if existing >= 0 {
				m := &matches[existing]
				if !contains(m.Reasons, reason) {
					m.Reasons = append(m.Reasons, reason)
				}
				m.Score = max(m.Score, score)
				m.Confidence = "strong"
				continue
			}
			if len(matches) >= candidates {
				candidateReached = true
				continue
			}
			matches = append(matches, StructuredMatch{rel.From, 1, rel.Kind + " " + rel.To, group, score, []string{reason, rel.Kind + " relationship"}, "strong", rel.Source})
		}
	}
	if candidateReached {
		result.Warnings = append(result.Warnings, fmt.Sprintf("Grouped search retained the first %d candidates before ranking.", candidates))
	}
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if c := files.Compare(a.Path, b.Path); c != 0 {
			return c < 0
		}
		return a.Line < b.Line
	})
	for _, m := range matches[:min(limit, len(matches))] {
		result.Groups[m.Group] = append(result.Groups[m.Group], m)
	}
	for _, g := range groupOrder {
		result.Matches = append(result.Matches, result.Groups[g]...)
	}
	result.Coverage.Truncated = result.Coverage.Truncated || budgetReached || candidateReached || skipped > 0
	result.Coverage.Warnings = result.Warnings
	return result, nil
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
