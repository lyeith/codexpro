package search

import (
	"math/rand"
	"reflect"
	"regexp"
	"regexp/syntax"
	"sort"
	"strings"
	"testing"
)

type analysisGuard struct {
	name     string
	re       *regexp.Regexp
	required []string
}

func analysisGuards() []analysisGuard {
	guards := []analysisGuard{}
	for language, rules := range declarations {
		for _, rule := range rules {
			guards = append(guards, analysisGuard{language + ":" + rule.kind, rule.re, rule.required})
		}
	}
	guards = append(guards, analysisGuard{"imports:javascript", jsImport, jsImportRequired}, analysisGuard{"imports:c", cImport, cImportRequired})
	sort.Slice(guards, func(i, j int) bool { return guards[i].name < guards[j].name })
	return guards
}

// This structural proof is deliberately conservative. A concat is guaranteed
// to contain the literal if a required child does; every alternative must have
// its own proof. Optional/repeated-zero branches cannot establish a guarantee.
// FoldCase and non-ASCII filter literals are rejected, so Unicode simple-fold
// and regexp's invalid-UTF8 RuneError interpretation cannot remove a match.
func syntaxGuaranteesAny(expression *syntax.Regexp, required []string) bool {
	switch expression.Op {
	case syntax.OpLiteral:
		if expression.Flags&syntax.FoldCase != 0 {
			return false
		}
		text := string(expression.Rune)
		for _, literal := range required {
			if strings.Contains(text, literal) {
				return true
			}
		}
	case syntax.OpConcat:
		for _, child := range expression.Sub {
			if syntaxGuaranteesAny(child, required) {
				return true
			}
		}
	case syntax.OpAlternate:
		if len(expression.Sub) == 0 {
			return false
		}
		for _, child := range expression.Sub {
			if !syntaxGuaranteesAny(child, required) {
				return false
			}
		}
		return true
	case syntax.OpCapture, syntax.OpPlus:
		return len(expression.Sub) == 1 && syntaxGuaranteesAny(expression.Sub[0], required)
	case syntax.OpRepeat:
		return expression.Min >= 1 && len(expression.Sub) == 1 && syntaxGuaranteesAny(expression.Sub[0], required)
	}
	return false
}

func TestAnalysisRequiredLiteralsAreNecessaryForEveryPatternBranch(t *testing.T) {
	annotated, unfiltered := 0, 0
	for _, guard := range analysisGuards() {
		if len(guard.required) == 0 {
			unfiltered++
			continue
		}
		annotated++
		for _, literal := range guard.required {
			if literal == "" {
				t.Fatalf("%s has an empty required literal", guard.name)
			}
			for _, character := range []byte(literal) {
				if character >= 128 {
					t.Fatalf("%s uses a non-ASCII guard without an invalid-UTF8 proof", guard.name)
				}
			}
		}
		expression, err := syntax.Parse(guard.re.String(), syntax.Perl)
		if err != nil || !syntaxGuaranteesAny(expression, guard.required) {
			t.Fatalf("%s cannot safely skip this regex using %q: %v", guard.name, guard.required, err)
		}
	}
	if annotated != 29 || unfiltered != 2 {
		t.Fatalf("unexpected declaration/import coverage: annotated=%d unfiltered=%d", annotated, unfiltered)
	}
}

func analysisOracleLines() []string {
	lines := []string{
		"", "    ", "const value = 1;", "let value = 1;", "var value = 1;",
		"export async function calculate(value) {", "function local() {", "class Widget {", "export class Widget {",
		"export interface Contract {", "interface Contract {", "export const value = 1;",
		"def calculate(value):", "    async def calculate(value):", "class Widget:",
		"func calculate(value int) int {", "func (x *Thing) calculate(value int) int {", "type Widget struct {",
		"pub fn calculate(value: u32) -> u32 {", "pub(crate) async fn calculate() {", "pub struct Widget {", "pub enum Mode {", "pub trait Contract {",
		"public func calculate() {", "internal class Widget {", "private struct Widget {", "protocol Contract {",
		"public static class Widget {", "public interface Contract {", "protected enum Mode {", "internal static struct Widget {",
		"int calculate(int value) {", "std::string calculate() {", "struct Widget {", "enum Mode {", "trait Contract {",
		"import {value} from './dependency';", "export {value} from './dependency';", "const value = require('./dependency');",
		"#include <fixture.h>", "  #include \"fixture.h\"", "// import comment 'same-reference-heuristic'",
		"// function inComment() {", "const keyword = 'class Fake';", "functionality = 1;", "myclass Word {", "CONST value = 1;",
		"imporT value from './missing'", "#Include <missing.h>", "Function upper() {", "var\tspaced = 1;",
		"export\nfunction multiLine() {", "class\rWidget {", "const\vvalue = 1;", "let\fvalue = 1;",
		"éclass Widget {", "Kclass Widget {", "ſtruct Widget {", "struct Kelvin {", "func ſample() {",
		"class Emoji😀 {", "😀 function calculate() {", "éexport const ascii = 1;", "\ufeffinterface Contract {",
		"const\u00a0value = 1;", "export\u2003function absent() {", "import './é😀Kſ';",
		string([]byte{'c', 'o', 'n', 's', 't', ' ', 0xff, 'x', ' ', '=', '1'}),
		string([]byte{0xff, 'c', 'l', 'a', 's', 's', ' ', 'X', ' ', '{'}),
		string([]byte{'f', 'u', 'n', 'c', 't', 'i', 0xfe, 'o', 'n', ' ', 'x', '(', ')'}),
		string([]byte{'i', 'm', 'p', 'o', 'r', 't', ' ', '\'', '.', '/', 0xff, '\''}),
		"\x00class Widget {", "\x00import './dependency'", strings.Repeat("x", 8192),
	}
	// Deterministic mixed Unicode/invalid-byte lines catch interactions between
	// boundaries, comments, optional whitespace and multiple possible matches.
	random := rand.New(rand.NewSource(7310))
	tokens := []string{"import", "export", "require", "const", "let", "var", "class", "interface", "func", "function", "def", "fn", "struct", "enum", "trait", "protocol", "type", "pub", "async", "#include", " ", "\t", "\n", "\v", "\r", "\u00a0", "K", "ſ", "é", "😀", "\xff", "\xfe", "\x00", "Name", "()", "{}", "'./dep'", "x", "_", "$"}
	for range 800 {
		var line strings.Builder
		for count := random.Intn(16) + 1; count > 0; count-- {
			line.WriteString(tokens[random.Intn(len(tokens))])
		}
		lines = append(lines, line.String())
	}
	return lines
}

func TestAnalysisPrefiltersExactlyPreserveOriginalRegexCaptures(t *testing.T) {
	lines := analysisOracleLines()
	for _, guard := range analysisGuards() {
		t.Run(guard.name, func(t *testing.T) {
			matches := 0
			for _, line := range lines {
				// The oracle invokes the unchanged original expression directly,
				// without any filter or reconstructed matching implementation.
				want := guard.re.FindStringSubmatch(line)
				got := matchRequired(guard.re, line, guard.required)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("capture changed for %q: got=%#v want=%#v", line, got, want)
				}
				if want != nil {
					matches++
				}
			}
			if matches == 0 {
				t.Fatal("oracle fixture never exercised a positive original match")
			}
		})
	}
}

var analysisPrefilterBenchmarkMatches int

// This measures the matcher change only. It is not a substitute for the actual
// 160-call cold-start profile, whose original slow observations remain retained.
func BenchmarkAnalysisDeclarationPrefilters(b *testing.B) {
	guards := analysisGuards()
	lines := []string{
		"  const response = await service.call(context, arguments);",
		"  if (response && response.status === 200) {",
		"    return { value: response.value, complete: true };",
		"  }",
		"  // The caller owns this resource until completion.",
		"  throw new Error('The operation did not complete.');",
		"}",
		"export async function invoke(context) {",
	}
	for _, filtered := range []bool{false, true} {
		name := "original"
		if filtered {
			name = "required_literals"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			total := 0
			for range b.N {
				for _, line := range lines {
					for _, guard := range guards {
						if filtered {
							total += len(matchRequired(guard.re, line, guard.required))
						} else {
							total += len(guard.re.FindStringSubmatch(line))
						}
					}
				}
			}
			analysisPrefilterBenchmarkMatches = total
		})
	}
}
