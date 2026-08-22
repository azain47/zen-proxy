package proxy

import (
	"strings"
	"testing"
)

func TestJSONTreePreservesOrderAndCollapsesSubtrees(t *testing.T) {
	root, err := parseJSONTree(`{"first":{"nested":1},"second":[true,false]}`)
	if err != nil {
		t.Fatalf("parseJSONTree() error = %v", err)
	}
	nodes := visibleJSONNodes(root)
	if len(nodes) != 6 || nodes[1].Key != "first" || nodes[3].Key != "second" {
		t.Fatalf("visible nodes = %#v", nodes)
	}

	root.Children[0].Collapsed = true
	nodes = visibleJSONNodes(root)
	if len(nodes) != 5 || strings.Contains(nodes[1].Path, "nested") {
		t.Fatalf("collapsed nodes = %#v", nodes)
	}
	if got := root.Children[1].RawJSON(); got != "[true,false]" {
		t.Fatalf("array raw JSON = %q", got)
	}
}

func TestJSONNodeMarkdownCopiesSelectedSubtree(t *testing.T) {
	model := dashboardModel{
		cache: &viewerCache{},
		traces: []RequestTrace{{
			ID:      1,
			Inbound: TracePayload{Body: `{"message":{"role":"user","content":"hello"}}`},
		}},
	}
	model.selectedJSONNode()
	model.moveJSONCursor(1)
	markdown, ok := model.jsonNodeMarkdown()
	if !ok || !strings.Contains(markdown, "JSON node `/message`") || !strings.Contains(markdown, "\"content\": \"hello\"") {
		t.Fatalf("subtree Markdown = %q, ok = %v", markdown, ok)
	}
}

func TestJSONCursorSelectorTracksJSONPointer(t *testing.T) {
	model := dashboardModel{
		cache: &viewerCache{},
		traces: []RequestTrace{{
			ID:      1,
			Inbound: TracePayload{Body: `{"message":{"role":"user"}}`},
		}},
	}
	if got := model.jsonCursorPath(); got != "/" {
		t.Fatalf("initial cursor path = %q, want root", got)
	}
	if !model.moveJSONCursor(1) {
		t.Fatal("moveJSONCursor(1) did not move to the first child")
	}
	if got := model.jsonCursorPath(); got != "/message" {
		t.Fatalf("cursor path after move = %q, want /message", got)
	}
}
