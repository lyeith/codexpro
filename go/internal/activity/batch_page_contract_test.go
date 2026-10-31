package activity

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"
	"testing"
)

var batchPreBlock = regexp.MustCompile(`(?s)<pre>(.*?)</pre>`)

// These expected labels/sections come from the pinned TypeScript page.ts
// renderer, including the current-definition versus historical-invocation
// distinction. The previous Go page omitted them.
func TestSavedBatchPageRetainsSourceReviewInformation(t *testing.T) {
	args := map[string]any{"path": `<sp6-evidence data-value="quoted">&'.txt`}
	definition := map[string]any{"version": 1, "mode": "serial", "continue_on_error": false,
		"operations": []any{map[string]any{"id": "read-0", "tool": "read", "args": args}}}
	view := map[string]any{"path": "owned/recipe.json", "projectLabel": "Project label",
		"projectId": "project-id", "workspaceId": "activity_project-id", "autoStored": false,
		"definition": definition}
	page := RenderBatchPage(view)
	for _, marker := range []string{
		"Saved CodexPro batch", "Project label <code>project-id</code>",
		" · workspace <code>activity_project-id</code>", `<h2>Operations</h2>`,
		`continue on error: no`, `custom JSON`, `1 operation`,
		`This is the current saved definition. If the batch was edited after the action ran, it may differ from the historical invocation.`,
		`Exact commands and arguments can contain credentials or other sensitive values; treat this authenticated page as sensitive.`,
		`href="/activity" data-local-link`, `initialUrl.hash`, `if (queryCredential)`,
		`sessionStorage.setItem(authStorageName, queryCredential)`,
		`target.searchParams.set("codexpro_token", connectorCredential)`,
	} {
		if !strings.Contains(page, marker) {
			t.Errorf("source page behavior missing: %s", marker)
		}
	}
	if strings.Contains(page, "<sp6-evidence") {
		t.Fatal("saved argument became an HTML element")
	}
	blocks := batchPreBlock.FindAllStringSubmatch(page, -1)
	if len(blocks) != 2 {
		t.Fatalf("want one operation and complete raw JSON, got %d pre blocks", len(blocks))
	}
	for i, expected := range []any{args, definition} {
		var value any
		if err := json.Unmarshal([]byte(html.UnescapeString(blocks[i][1])), &value); err != nil {
			t.Fatal(err)
		}
		actualJSON, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		expectedJSON, err := json.Marshal(expected)
		if err != nil {
			t.Fatal(err)
		}
		if string(actualJSON) != string(expectedJSON) {
			t.Fatalf("block%d loses source JSON value", i)
		}
	}
}

func TestSavedBatchPagePreservesEmptyModeAndOptionalWorkspace(t *testing.T) {
	view := map[string]any{"path": "owned/recipe.json", "projectLabel": "Label", "projectId": "id",
		"autoStored": true, "definition": map[string]any{"mode": "", "operations": []any{}, "continue_on_error": true}}
	page, fragment := RenderBatchPage(view), RenderBatchFragment(view)
	for _, output := range []string{page, fragment} {
		if !strings.Contains(output, `<span class="badge"></span>`) {
			t.Fatal("empty string mode was replaced")
		}
		if !strings.Contains(output, "This JSON has no operation list.") {
			t.Fatal("empty operation fallback disappeared")
		}
		if !strings.Contains(output, "auto-stored") {
			t.Fatal("stored attribution disappeared")
		}
	}
	if strings.Contains(page, " · workspace ") {
		t.Fatal("missing workspace was fabricated by pure renderer")
	}
	if !strings.Contains(page, "continue on error: yes") {
		t.Fatal("true continuation flag lost")
	}
	if strings.Contains(fragment, "Label") || strings.Contains(fragment, "Saved CodexPro batch") {
		t.Fatal("inline fragment acquired full-page attribution")
	}
}

func TestSavedBatchViewerKeepsNonObjectRawJSON(t *testing.T) {
	raw := []any{"caller content", map[string]any{"retained": true}}
	view := map[string]any{"definition": raw}
	for _, output := range []string{RenderBatchFragment(view), RenderBatchPage(view)} {
		blocks := batchPreBlock.FindAllStringSubmatch(output, -1)
		if len(blocks) != 1 {
			t.Fatalf("expected the raw document only, got%d", len(blocks))
		}
		var value any
		if err := json.Unmarshal([]byte(html.UnescapeString(blocks[0][1])), &value); err != nil {
			t.Fatal(err)
		}
		got, _ := json.Marshal(value)
		want, _ := json.Marshal(raw)
		if string(got) != string(want) {
			t.Fatal("viewer coerced non-object JSON instead of displaying it")
		}
		if !strings.Contains(output, `<span class="badge">unknown</span>`) {
			t.Fatal("non-string mode fallback changed")
		}
	}
}
