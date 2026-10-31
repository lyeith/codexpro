package search

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/wire"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type ImpactFile struct {
	Path       string   `json:"path"`
	Confidence string   `json:"confidence"`
	Reasons    []string `json:"reasons"`
}
type Risk struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	Confidence string   `json:"confidence"`
	Paths      []string `json:"paths"`
	Reasons    []string `json:"reasons"`
}
type Recommendation struct {
	Command string   `json:"command"`
	Source  string   `json:"source"`
	Reasons []string `json:"reasons"`
}
type ChangeAnalysis struct {
	SchemaVersion       int              `json:"schemaVersion"`
	ChangedPaths        []string         `json:"changedPaths"`
	AffectedAreas       []string         `json:"affectedAreas"`
	DependentFiles      []ImpactFile     `json:"dependentFiles"`
	RelatedTests        []ImpactFile     `json:"relatedTests"`
	RiskSignals         []Risk           `json:"riskSignals"`
	RecommendedCommands []Recommendation `json:"recommendedCommands"`
	Coverage            Coverage         `json:"coverage"`
	Warnings            []string         `json:"warnings"`
	Cache               AnalysisCache    `json:"cache"`
}

var riskPatterns = []struct {
	id, label string
	re        *regexp.Regexp
}{
	{"authentication", "Authentication or sessions", regexp.MustCompile(`auth|session|token|oauth|login`)},
	{"migration", "Schema or migration", regexp.MustCompile(`migration|schema|prisma|drizzle`)},
	{"storage", "Storage or persistence", regexp.MustCompile(`database|storage|repository|model`)},
	{"build", "Build or dependency configuration", regexp.MustCompile(`package\.json|lock|build|webpack|vite|tsconfig`)},
	{"configuration", "Runtime configuration", regexp.MustCompile(`config|settings|\.ya?ml|\.toml`)},
	{"public-api", "Public API", regexp.MustCompile(`api|public|export|route`)},
}
var packageManager = regexp.MustCompile(`^(npm|pnpm|yarn|bun)(?:@|$)`)
var extension = regexp.MustCompile(`\.[^.]+$`)

func (s *Service) recommendations(w wire.Workspace) []Recommendation {
	out := []Recommendation{}
	abs, _, e := s.Files.ResolvePath(w, "package.json", false)
	if e == nil {
		b, e := os.ReadFile(abs)
		var pkg map[string]any
		if e == nil {
			e = json.Unmarshal(b, &pkg)
		}
		if e == nil {
			scripts, _ := pkg["scripts"].(map[string]any)
			runner := ""
			if raw, ok := pkg["packageManager"].(string); ok {
				m := packageManager.FindStringSubmatch(raw)
				if len(m) > 1 {
					runner = m[1]
				}
			}
			if runner == "" {
				for _, pair := range [][2]string{{"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"bun.lock", "bun"}, {"bun.lockb", "bun"}, {"package-lock.json", "npm"}, {"npm-shrinkwrap.json", "npm"}} {
					p, _, e := s.Files.ResolvePath(w, pair[0], false)
					if e == nil {
						if st, e := os.Stat(p); e == nil && st.Mode().IsRegular() {
							runner = pair[1]
							break
						}
					}
				}
			}
			if runner == "" {
				runner = "npm"
			}
			for _, name := range []string{"test", "test:unit", "typecheck", "lint", "build", "check"} {
				if _, ok := scripts[name].(string); !ok {
					continue
				}
				command := runner + " run " + name
				if (runner == "npm" || runner == "pnpm") && name == "test" {
					command = runner + " test"
				}
				reason := "project verification"
				if strings.Contains(name, "test") {
					reason = "related test coverage"
				}
				out = append(out, Recommendation{command, "package.json", []string{"existing project script", runner + " project", reason}})
			}
		}
	}
	for _, pair := range [][2]string{{"go.mod", "go test ./..."}, {"Cargo.toml", "cargo test"}, {"Package.swift", "swift test"}, {"pyproject.toml", "python3 -m pytest"}, {"pom.xml", "mvn test"}} {
		p, _, e := s.Files.ResolvePath(w, pair[0], false)
		if e == nil {
			if st, e := os.Stat(p); e == nil && st.Mode().IsRegular() {
				out = append(out, Recommendation{pair[1], pair[0], []string{"detected project manifest", "native project verification"}})
			}
		}
	}
	return out
}
func (s *Service) Review(ctx context.Context, w wire.Workspace, candidates []string) (*ChangeAnalysis, error) {
	changed := []string{}
	pathWarnings := []string{}
	for _, candidate := range candidates {
		_, rel, e := s.Files.ResolvePath(w, candidate, false)
		if e != nil {
			pathWarnings = append(pathWarnings, "Skipped unsafe or unreadable changed path: "+candidate)
			continue
		}
		if !contains(changed, rel) {
			changed = append(changed, rel)
		}
	}
	analysis, e := s.Analyze(ctx, w)
	if e != nil {
		return nil, e
	}
	result := &ChangeAnalysis{SchemaVersion: 1, ChangedPaths: changed, AffectedAreas: []string{}, DependentFiles: []ImpactFile{}, RelatedTests: []ImpactFile{}, RiskSignals: []Risk{}, RecommendedCommands: s.recommendations(w), Coverage: analysis.Coverage, Warnings: append(append([]string{}, analysis.Warnings...), pathWarnings...), Cache: analysis.Cache}
	areas := map[string]bool{}
	changedSet := stringSet(changed...)
	for _, p := range changed {
		area := "."
		if strings.Contains(p, "/") {
			area = strings.SplitN(p, "/", 2)[0]
		}
		areas[area] = true
	}
	for a := range areas {
		result.AffectedAreas = append(result.AffectedAreas, a)
	}
	sort.Strings(result.AffectedAreas)
	dependents, tests := map[string][]string{}, map[string][]string{}
	add := func(m map[string][]string, key, value string) {
		if !contains(m[key], value) {
			m[key] = append(m[key], value)
		}
	}
	for _, rel := range analysis.Relationships {
		if !changedSet[rel.To] {
			continue
		}
		target := dependents
		if rel.Kind == "tests" {
			target = tests
		}
		add(target, rel.From, rel.Kind+" "+rel.To)
	}
	for _, f := range analysis.Files {
		if f.Role != "test" {
			continue
		}
		for _, p := range changed {
			base := strings.ToLower(extension.ReplaceAllString(filepath.Base(p), ""))
			if files.JSLength(base) > 2 && strings.Contains(strings.ToLower(f.Path), base) {
				add(tests, f.Path, "test filename matches changed source")
				break
			}
		}
	}
	riskIndex := map[string]int{}
	for _, p := range changed {
		for _, pattern := range riskPatterns {
			if !pattern.re.MatchString(strings.ToLower(p)) {
				continue
			}
			idx, ok := riskIndex[pattern.id]
			if !ok {
				idx = len(result.RiskSignals)
				riskIndex[pattern.id] = idx
				result.RiskSignals = append(result.RiskSignals, Risk{pattern.id, pattern.label, "inferred", []string{}, []string{"path pattern matched " + pattern.id}})
			}
			risk := &result.RiskSignals[idx]
			if !contains(risk.Paths, p) {
				risk.Paths = append(risk.Paths, p)
			}
		}
	}
	for i := range result.RiskSignals {
		sort.Strings(result.RiskSignals[i].Paths)
	}
	for p, reasons := range dependents {
		result.DependentFiles = append(result.DependentFiles, ImpactFile{p, "strong", reasons})
	}
	for p, reasons := range tests {
		result.RelatedTests = append(result.RelatedTests, ImpactFile{p, "strong", reasons})
	}
	sort.SliceStable(result.DependentFiles, func(i, j int) bool {
		return files.Compare(result.DependentFiles[i].Path, result.DependentFiles[j].Path) < 0
	})
	sort.SliceStable(result.RelatedTests, func(i, j int) bool {
		return files.Compare(result.RelatedTests[i].Path, result.RelatedTests[j].Path) < 0
	})
	limit := max(1, s.Config.Int("maxSearchResults", 200))
	if len(result.DependentFiles) > limit || len(result.RelatedTests) > limit {
		result.Warnings = append(result.Warnings, fmt.Sprintf("Change-impact output was limited to %d dependent files and %d related tests.", limit, limit))
	}
	result.DependentFiles = result.DependentFiles[:min(limit, len(result.DependentFiles))]
	result.RelatedTests = result.RelatedTests[:min(limit, len(result.RelatedTests))]
	return result, nil
}
func (c *ChangeAnalysis) Wire() map[string]any {
	return map[string]any{"schema_version": c.SchemaVersion, "changed_paths": c.ChangedPaths, "affected_areas": c.AffectedAreas, "dependent_files": c.DependentFiles, "related_tests": c.RelatedTests, "risk_signals": c.RiskSignals, "recommended_commands": c.RecommendedCommands, "coverage": c.Coverage, "warnings": c.Warnings, "cache": c.Cache}
}
