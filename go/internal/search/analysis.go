package search

import (
	"context"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type AnalysisLimits struct {
	MaxInventoryFiles int `json:"maxInventoryFiles"`
	MaxAnalyzedFiles  int `json:"maxAnalyzedFiles"`
	MaxScannedBytes   int `json:"maxScannedBytes"`
	MaxSymbols        int `json:"maxSymbols"`
	MaxRelationships  int `json:"maxRelationships"`
}
type InventoryFile struct {
	Path       string  `json:"path"`
	Bytes      int64   `json:"bytes"`
	ModifiedMS float64 `json:"modifiedMs"`
	Language   string  `json:"language"`
	Role       string  `json:"role"`
	Generated  bool    `json:"generated"`
	Entrypoint bool    `json:"entrypoint"`
}
type Coverage struct {
	InventoryFiles    int      `json:"inventoryFiles"`
	AnalyzedFiles     int      `json:"analyzedFiles"`
	ScannedBytes      int      `json:"scannedBytes"`
	SymbolCount       int      `json:"symbolCount"`
	RelationshipCount int      `json:"relationshipCount"`
	Truncated         bool     `json:"truncated"`
	Warnings          []string `json:"warnings"`
}
type Symbol struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Exported   bool   `json:"exported"`
	Confidence string `json:"confidence"`
}
type Relationship struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Kind       string `json:"kind"`
	Confidence string `json:"confidence"`
	Source     string `json:"source"`
}
type Area struct {
	Path  string `json:"path"`
	Role  string `json:"role"`
	Files int    `json:"files"`
}
type AnalysisCache struct {
	Hit bool   `json:"hit"`
	Key string `json:"key"`
}
type Analysis struct {
	SchemaVersion  int             `json:"schemaVersion"`
	WorkspaceID    string          `json:"workspaceId"`
	Root           string          `json:"root"`
	Languages      []string        `json:"languages"`
	ProjectTypes   []string        `json:"projectTypes"`
	Entrypoints    []string        `json:"entrypoints"`
	ImportantFiles []string        `json:"importantFiles"`
	Areas          []Area          `json:"areas"`
	Files          []InventoryFile `json:"files"`
	Symbols        []Symbol        `json:"symbols"`
	Relationships  []Relationship  `json:"relationships"`
	Coverage       Coverage        `json:"coverage"`
	Warnings       []string        `json:"warnings"`
	Fingerprint    string          `json:"fingerprint"`
	Cache          AnalysisCache   `json:"cache"`
}

func (s *Service) analysisLimits() AnalysisLimits {
	return AnalysisLimits{
		s.Config.Int("maxInventoryFiles", 20000), s.Config.Int("maxAnalyzedFiles", 5000), s.Config.Int("maxScannedBytes", 64*1024*1024), s.Config.Int("maxSymbols", 100000), s.Config.Int("maxRelationships", 250000),
	}
}

var languagesByExtension = map[string]string{
	".ts": "typescript", ".tsx": "typescript", ".mts": "typescript", ".cts": "typescript",
	".js": "javascript", ".jsx": "javascript", ".mjs": "javascript", ".cjs": "javascript",
	".py": "python", ".go": "go", ".rs": "rust", ".swift": "swift", ".java": "java", ".cs": "csharp",
	".c": "c", ".h": "c", ".cc": "cpp", ".cpp": "cpp", ".cxx": "cpp", ".hpp": "cpp", ".hh": "cpp",
	".json": "json", ".yaml": "yaml", ".yml": "yaml", ".toml": "toml", ".md": "markdown", ".mdx": "markdown",
	".sh": "shell", ".bash": "shell", ".zsh": "shell",
}
var testPattern = regexp.MustCompile(`(?i)(^|/)(__tests__|test|tests|spec)/|\.(test|spec)\.[^.]+$|_test\.go$|Tests?\.swift$`)
var generatedPattern = regexp.MustCompile(`(?i)(^|/)(generated|vendor|vendors|third_party)/|\.generated\.|\.g\.(cs|dart)$`)
var infraPattern = regexp.MustCompile(`(?i)(^|/)(\.github|infra|infrastructure|terraform|deploy|k8s|helm)/|(^|/)Dockerfile$`)
var configPattern = regexp.MustCompile(`(?i)(^|/)(config|configs)/`)
var docsPattern = regexp.MustCompile(`(?i)(^|/)docs?/`)
var importantPattern = regexp.MustCompile(`(?i)(^|/)(README|AGENTS)\.md$`)
var sourceLanguages = stringSet("typescript", "javascript", "python", "go", "rust", "swift", "java", "csharp", "c", "cpp")
var configNames = stringSet("package.json", "tsconfig.json", "pyproject.toml", "Cargo.toml", "go.mod", "pom.xml", "build.gradle", "build.gradle.kts", "Package.swift")
var entrypointNames = stringSet("index.ts", "index.js", "main.ts", "main.js", "main.py", "main.go", "main.rs", "lib.rs", "App.swift", "Program.cs")

func stringSet(values ...string) map[string]bool {
	out := map[string]bool{}
	for _, v := range values {
		out[v] = true
	}
	return out
}
func language(path string) string {
	if v := languagesByExtension[strings.ToLower(filepath.Ext(path))]; v != "" {
		return v
	}
	return "unknown"
}
func fileRole(path, lang string) string {
	switch {
	case generatedPattern.MatchString(path):
		return "generated"
	case testPattern.MatchString(path):
		return "test"
	case configNames[filepath.Base(path)] || configPattern.MatchString(path):
		return "config"
	case lang == "markdown" || docsPattern.MatchString(path):
		return "docs"
	case infraPattern.MatchString(path):
		return "infrastructure"
	case sourceLanguages[lang]:
		return "source"
	default:
		return "other"
	}
}
func projectTypes(inventory []InventoryFile) []string {
	names := map[string]bool{}
	dotnet := false
	for _, f := range inventory {
		names[filepath.Base(f.Path)] = true
		if strings.HasSuffix(f.Path, ".sln") || strings.HasSuffix(f.Path, ".csproj") {
			dotnet = true
		}
	}
	out := []string{}
	for _, x := range []struct {
		kind  string
		found bool
	}{{"node", names["package.json"]}, {"python", names["pyproject.toml"] || names["requirements.txt"]}, {"go", names["go.mod"]}, {"rust", names["Cargo.toml"]}, {"swift", names["Package.swift"]}, {"jvm", names["pom.xml"] || names["build.gradle"] || names["build.gradle.kts"]}, {"dotnet", dotnet}, {"native", names["CMakeLists.txt"] || names["Makefile"]}} {
		if x.found {
			out = append(out, x.kind)
		}
	}
	return out
}
func (s *Service) inventory(ctx context.Context, w wire.Workspace, limit int) ([]InventoryFile, string, Coverage, error) {
	candidates, e := s.Files.ListFiles(ctx, w, ".", "", true, limit+1)
	if e != nil {
		return nil, "", Coverage{}, e
	}
	truncated := len(candidates) > limit
	out := []InventoryFile{}
	for _, candidate := range candidates[:min(limit, len(candidates))] {
		if e := ctx.Err(); e != nil {
			return nil, "", Coverage{}, e
		}
		abs, rel, e := s.Files.ResolvePath(w, candidate, false)
		if e != nil {
			continue
		}
		st, e := os.Stat(abs)
		if e != nil || !st.Mode().IsRegular() {
			continue
		}
		if !s.readableSource(abs, st, s.Files.ScanLimit()) {
			continue
		}
		lang := language(rel)
		out = append(out, InventoryFile{rel, st.Size(), float64(st.ModTime().Unix())*1000 + float64(st.ModTime().Nanosecond())/1e6, lang, fileRole(rel, lang), generatedPattern.MatchString(rel), entrypointNames[filepath.Base(rel)]})
	}
	sort.SliceStable(out, func(i, j int) bool { return files.Compare(out[i].Path, out[j].Path) < 0 })
	records := make([]string, 0, len(out))
	for _, f := range out {
		records = append(records, f.Path+":"+strconv.FormatInt(f.Bytes, 10)+":"+strconv.FormatFloat(f.ModifiedMS, 'f', -1, 64))
	}
	warn := []string{}
	if truncated {
		warn = append(warn, fmt.Sprintf("Inventory truncated at %d files.", limit))
	}
	return out, files.SHA(strings.Join(records, "\n")), Coverage{InventoryFiles: len(out), Truncated: truncated, Warnings: warn}, nil
}

type declaration struct {
	re       *regexp.Regexp
	kind     string
	required []string
}

// Each annotated expression must match at least one exact, case-sensitive
// literal. The original regex remains the matcher; this only avoids invoking
// it on lines that cannot match. Unannotated expressions always use the regex.
func decl(pattern, kind string, required ...string) declaration {
	return declaration{regexp.MustCompile(pattern), kind, required}
}

func matchRequired(re *regexp.Regexp, text string, required []string) []string {
	if len(required) == 0 {
		return re.FindStringSubmatch(text)
	}
	for _, literal := range required {
		if strings.Contains(text, literal) {
			return re.FindStringSubmatch(text)
		}
	}
	return nil
}

var declarations = map[string][]declaration{
	"typescript": {decl(`\b(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_$][\w$]*)`, "function", "function"), decl(`\b(?:export\s+)?class\s+([A-Za-z_$][\w$]*)`, "class", "class"), decl(`\b(?:export\s+)?interface\s+([A-Za-z_$][\w$]*)`, "interface", "interface"), decl(`\b(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)`, "variable", "const", "let", "var")},
	"javascript": {decl(`\b(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_$][\w$]*)`, "function", "function"), decl(`\b(?:export\s+)?class\s+([A-Za-z_$][\w$]*)`, "class", "class"), decl(`\b(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)`, "variable", "const", "let", "var")},
	"python":     {decl(`^\s*(?:async\s+)?def\s+([A-Za-z_]\w*)`, "function", "def"), decl(`^\s*class\s+([A-Za-z_]\w*)`, "class", "class")},
	"go":         {decl(`^func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)`, "function", "func"), decl(`^type\s+([A-Za-z_]\w*)\s+`, "type", "type")},
	"rust":       {decl(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?fn\s+([A-Za-z_]\w*)`, "function", "fn"), decl(`^\s*(?:pub\s+)?struct\s+([A-Za-z_]\w*)`, "struct", "struct"), decl(`^\s*(?:pub\s+)?enum\s+([A-Za-z_]\w*)`, "enum", "enum"), decl(`^\s*(?:pub\s+)?trait\s+([A-Za-z_]\w*)`, "trait", "trait")},
	"swift":      {decl(`^\s*(?:public\s+|internal\s+|private\s+)?func\s+([A-Za-z_]\w*)`, "function", "func"), decl(`^\s*(?:public\s+|internal\s+|private\s+)?class\s+([A-Za-z_]\w*)`, "class", "class"), decl(`^\s*(?:public\s+|internal\s+|private\s+)?struct\s+([A-Za-z_]\w*)`, "struct", "struct"), decl(`^\s*(?:public\s+|internal\s+|private\s+)?protocol\s+([A-Za-z_]\w*)`, "protocol", "protocol")},
	"java":       {decl(`^\s*(?:public|protected|private)?\s*(?:static\s+)?class\s+([A-Za-z_]\w*)`, "class", "class"), decl(`^\s*(?:public|protected|private)?\s*(?:static\s+)?interface\s+([A-Za-z_]\w*)`, "interface", "interface"), decl(`^\s*(?:public|protected|private)?\s*(?:static\s+)?enum\s+([A-Za-z_]\w*)`, "enum", "enum")},
	"csharp":     {decl(`^\s*(?:public|protected|private|internal)?\s*(?:static\s+)?class\s+([A-Za-z_]\w*)`, "class", "class"), decl(`^\s*(?:public|protected|private|internal)?\s*(?:static\s+)?interface\s+([A-Za-z_]\w*)`, "interface", "interface"), decl(`^\s*(?:public|protected|private|internal)?\s*(?:static\s+)?struct\s+([A-Za-z_]\w*)`, "struct", "struct")},
	"c":          {decl(`^\s*[A-Za-z_]\w*(?:\s+[*])?\s+([A-Za-z_]\w*)\s*\([^;]*\)\s*\{`, "function")},
	"cpp":        {decl(`^\s*class\s+([A-Za-z_]\w*)`, "class", "class"), decl(`^\s*struct\s+([A-Za-z_]\w*)`, "struct", "struct"), decl(`^\s*[A-Za-z_:][\w:<>,*&\s]*\s+([A-Za-z_]\w*)\s*\([^;]*\)\s*\{`, "function")},
}
var jsImport = regexp.MustCompile(`\b(?:import|export)\b[^"']*?["']([^"']+)["']|\brequire\(\s*["']([^"']+)["']\s*\)`)
var cImport = regexp.MustCompile(`^\s*#include\s*["<]([^">]+)[">]`)
var jsImportRequired = []string{"import", "export", "require"}
var cImportRequired = []string{"#include"}
var exportedDeclaration = regexp.MustCompile(`\b(export|public|pub)\b`)
var runtimeExtension = regexp.MustCompile(`\.(js|mjs|cjs)$`)

func resolveImport(from, spec string, present map[string]bool) string {
	if !strings.HasPrefix(spec, ".") {
		return ""
	}
	raw := pathpkg.Clean(pathpkg.Join(pathpkg.Dir(from), spec))
	base := runtimeExtension.ReplaceAllString(raw, "")
	candidates := []string{raw, base}
	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".py", ".go", ".rs", ".swift", ".java", ".cs", ".c", ".cpp", ".h", ".hpp"} {
		candidates = append(candidates, base+ext)
	}
	for _, n := range []string{"index.ts", "index.tsx", "index.js", "index.py"} {
		candidates = append(candidates, base+"/"+n)
	}
	for _, p := range candidates {
		if present[p] {
			return p
		}
	}
	return ""
}
func analysisLines(text string) []string {
	return strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
}

// analyze computes the analysis; Analyze (analysis_cache.go) bounds and shares it.
func (s *Service) analyze(ctx context.Context, w wire.Workspace) (*Analysis, error) {
	limits := s.analysisLimits()
	inventory, fingerprint, coverage, e := s.inventory(ctx, w, limits.MaxInventoryFiles)
	if e != nil {
		return nil, e
	}
	key := w.ID + ":" + fingerprint + ":" + files.JSONString(limits)
	s.mu.Lock()
	cached := s.analyses[key]
	if cached != nil {
		s.touchAnalysis(key)
		copy := *cached
		copy.Cache = AnalysisCache{true, key}
		s.mu.Unlock()
		return &copy, nil
	}
	s.mu.Unlock()
	result := &Analysis{SchemaVersion: 1, WorkspaceID: w.ID, Root: w.Root, Languages: []string{}, ProjectTypes: projectTypes(inventory), Entrypoints: []string{}, ImportantFiles: []string{}, Areas: []Area{}, Files: inventory, Symbols: []Symbol{}, Relationships: []Relationship{}, Coverage: coverage, Warnings: append([]string{}, coverage.Warnings...), Fingerprint: fingerprint, Cache: AnalysisCache{false, key}}
	present := map[string]bool{}
	langs := map[string]bool{}
	areaIndex := map[string]int{}
	for _, f := range inventory {
		present[f.Path] = true
		if f.Language != "unknown" {
			langs[f.Language] = true
		}
		if f.Entrypoint {
			result.Entrypoints = append(result.Entrypoints, f.Path)
		}
		if f.Role == "config" || importantPattern.MatchString(f.Path) {
			result.ImportantFiles = append(result.ImportantFiles, f.Path)
		}
		top := "."
		if strings.Contains(f.Path, "/") {
			top = strings.SplitN(f.Path, "/", 2)[0]
		}
		idx, ok := areaIndex[top]
		if !ok {
			idx = len(result.Areas)
			areaIndex[top] = idx
			result.Areas = append(result.Areas, Area{Path: top, Role: f.Role})
		}
		area := &result.Areas[idx]
		area.Files++
		if area.Role == "other" && f.Role != "other" {
			area.Role = f.Role
		}
	}
	for l := range langs {
		result.Languages = append(result.Languages, l)
	}
	sort.Strings(result.Languages)
	sort.SliceStable(result.Areas, func(i, j int) bool {
		a, b := result.Areas[i], result.Areas[j]
		if a.Files != b.Files {
			return a.Files > b.Files
		}
		return files.Compare(a.Path, b.Path) < 0
	})
	sourceLimit, symbolLimit, skipped := false, false, 0
	for _, f := range inventory {
		if !sourceLanguages[f.Language] || f.Generated {
			continue
		}
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		if result.Coverage.AnalyzedFiles >= limits.MaxAnalyzedFiles || int64(result.Coverage.ScannedBytes)+f.Bytes > int64(limits.MaxScannedBytes) {
			sourceLimit = true
			break
		}
		abs, _, e := s.Files.ResolvePath(w, f.Path, false)
		var facts *sourceFacts
		if e == nil {
			facts, e = s.extractSource(abs, f.Language, limits.MaxScannedBytes-result.Coverage.ScannedBytes)
		}
		if e != nil {
			skipped++
			continue
		}
		if result.Coverage.ScannedBytes+facts.TextLen > limits.MaxScannedBytes {
			sourceLimit = true
			break
		}
		result.Coverage.ScannedBytes += facts.TextLen
		result.Coverage.AnalyzedFiles++
		for _, d := range facts.Declarations {
			if len(result.Symbols) >= limits.MaxSymbols {
				symbolLimit = true
				continue
			}
			result.Symbols = append(result.Symbols, Symbol{d.Name, d.Kind, f.Path, d.Line, d.Exported, "strong"})
		}
		imports := []string{}
		seen := map[string]bool{}
		for _, spec := range facts.ImportSpecs {
			if target := resolveImport(f.Path, spec, present); target != "" && !seen[target] {
				seen[target] = true
				imports = append(imports, target)
			}
		}
		for _, target := range imports {
			if len(result.Relationships) >= limits.MaxRelationships {
				break
			}
			kind := "imports"
			if f.Role == "test" {
				kind = "tests"
			}
			result.Relationships = append(result.Relationships, Relationship{f.Path, target, kind, "strong", "built-in import extraction"})
		}
	}
	if sourceLimit {
		result.Warnings = append(result.Warnings, "Source analysis reached its file or byte limit.")
	}
	if symbolLimit {
		result.Warnings = append(result.Warnings, "Symbol extraction reached its configured limit.")
	}
	if skipped > 0 {
		suffix := "s"
		if skipped == 1 {
			suffix = ""
		}
		result.Warnings = append(result.Warnings, fmt.Sprintf("Skipped %d source file%s that changed or became unreadable during analysis.", skipped, suffix))
	}
	result.Coverage.SymbolCount = len(result.Symbols)
	result.Coverage.RelationshipCount = len(result.Relationships)
	result.Coverage.Truncated = result.Coverage.Truncated || sourceLimit || symbolLimit || skipped > 0
	result.Coverage.Warnings = result.Warnings
	s.mu.Lock()
	s.analyses[key] = result
	s.touchAnalysis(key)
	for len(s.analysisOrder) > 8 {
		old := s.analysisOrder[0]
		s.analysisOrder = s.analysisOrder[1:]
		delete(s.analyses, old)
	}
	s.mu.Unlock()
	return result, nil
}
func (s *Service) touchAnalysis(key string) {
	for i, k := range s.analysisOrder {
		if k == key {
			s.analysisOrder = append(s.analysisOrder[:i], s.analysisOrder[i+1:]...)
			break
		}
	}
	s.analysisOrder = append(s.analysisOrder, key)
}
func (s *Service) Invalidate(workspaceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := []string{}
	for _, k := range s.analysisOrder {
		if strings.HasPrefix(k, workspaceID+":") {
			delete(s.analyses, k)
		} else {
			next = append(next, k)
		}
	}
	s.analysisOrder = next
}
func scoped(prefix, path string) bool {
	return prefix == "" || path == prefix || strings.HasPrefix(path, prefix+"/")
}
func (s *Service) inspect(ctx context.Context, scope wire.Scope, w wire.Workspace, a map[string]any) (wire.Result, error) {
	path := wire.String(a, "path", ".")
	prefix := ""
	if path != "" {
		_, rel, e := s.Files.ResolvePath(w, path, false)
		if e != nil {
			return wire.Result{}, e
		}
		if files.TrimJS(path) != "" && rel != "." {
			prefix = rel
		}
	}
	analysis, e := s.Analyze(ctx, w)
	if e != nil {
		return wire.Result{}, e
	}
	limits := s.analysisLimits()
	fileLimit := max(1, min(limits.MaxInventoryFiles, wire.Int(a, "max_files", 300)))
	symbolLimit := max(1, min(limits.MaxSymbols, wire.Int(a, "max_symbols", 500)))
	relationshipLimit := max(1, min(limits.MaxRelationships, wire.Int(a, "max_relationships", 800)))
	sf := []InventoryFile{}
	ss := []Symbol{}
	sr := []Relationship{}
	areas := []Area{}
	entries := []string{}
	important := []string{}
	for _, v := range analysis.Files {
		if scoped(prefix, v.Path) {
			sf = append(sf, v)
		}
	}
	for _, v := range analysis.Symbols {
		if scoped(prefix, v.Path) {
			ss = append(ss, v)
		}
	}
	for _, v := range analysis.Relationships {
		if scoped(prefix, v.From) || scoped(prefix, v.To) {
			sr = append(sr, v)
		}
	}
	for _, v := range analysis.Areas {
		if prefix == "" || v.Path == "." || scoped(prefix, v.Path) || strings.HasPrefix(prefix, v.Path+"/") {
			areas = append(areas, v)
		}
	}
	for _, v := range analysis.Entrypoints {
		if scoped(prefix, v) {
			entries = append(entries, v)
		}
	}
	for _, v := range analysis.ImportantFiles {
		if scoped(prefix, v) {
			important = append(important, v)
		}
	}
	outFiles := sf[:min(len(sf), fileLimit)]
	outSymbols := ss[:min(len(ss), symbolLimit)]
	outRelations := sr[:min(len(sr), relationshipLimit)]
	hasSymbols, hasRelationships := wire.Bool(a, "include_symbols", true), wire.Bool(a, "include_relationships", true)
	if !hasSymbols {
		outSymbols = []Symbol{}
	}
	if !hasRelationships {
		outRelations = []Relationship{}
	}
	limited := len(outFiles) < len(sf) || hasSymbols && len(outSymbols) < len(ss) || hasRelationships && len(outRelations) < len(sr)
	warn := append([]string{}, analysis.Warnings...)
	if limited {
		warn = append(warn, "Structured output was limited. Use path or max_* arguments to request a narrower or larger result.")
	}
	projects, langs, entry := strings.Join(analysis.ProjectTypes, ", "), strings.Join(analysis.Languages, ", "), strings.Join(entries, ", ")
	if projects == "" {
		projects = "unknown"
	}
	if langs == "" {
		langs = "unknown"
	}
	if entry == "" {
		entry = "none detected"
	}
	partial := ""
	if analysis.Coverage.Truncated {
		partial = " (partial)"
	}
	text := fmt.Sprintf("# Workspace Analysis\n\nWorkspace: %s\nProjects: %s\nLanguages: %s\nEntrypoints: %s\nCoverage: %d/%d files analyzed, %d symbols, %d relationships%s\nReturned: %d files, %d symbols, %d relationships", w.Root, projects, langs, entry, analysis.Coverage.AnalyzedFiles, analysis.Coverage.InventoryFiles, analysis.Coverage.SymbolCount, analysis.Coverage.RelationshipCount, partial, len(outFiles), len(outSymbols), len(outRelations))
	if len(warn) > 0 {
		text += "\n\n## Warnings\n\n- " + strings.Join(warn, "\n- ")
	}
	data := files.Base(w)
	data["schema_version"] = 1
	data["path"] = path
	data["languages"] = analysis.Languages
	data["project_types"] = analysis.ProjectTypes
	data["entrypoints"] = entries
	data["important_files"] = important
	data["areas"] = areas
	data["files"] = outFiles
	data["symbols"] = outSymbols
	data["relationships"] = outRelations
	data["coverage"] = analysis.Coverage
	data["warnings"] = warn
	data["output_limited"] = limited
	data["returned"] = map[string]any{"files": len(outFiles), "symbols": len(outSymbols), "relationships": len(outRelations)}
	data["cache"] = analysis.Cache
	return wire.Success("inspect_workspace", "Inspect Workspace", data, text), nil
}
