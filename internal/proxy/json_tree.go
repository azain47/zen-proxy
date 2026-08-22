package proxy

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type jsonNodeKind uint8

const (
	jsonObjectNode jsonNodeKind = iota
	jsonArrayNode
	jsonStringNode
	jsonNumberNode
	jsonBoolNode
	jsonNullNode
)

type jsonTreeNode struct {
	Key       string
	Path      string
	Kind      jsonNodeKind
	Value     string
	Raw       string
	Children  []*jsonTreeNode
	Parent    *jsonTreeNode
	Collapsed bool
}

func parseJSONTree(body string) (*jsonTreeNode, error) {
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	return decodeJSONTree(decoder, "", "", nil)
}

func decodeJSONTree(decoder *json.Decoder, key, path string, parent *jsonTreeNode) (*jsonTreeNode, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}

	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			node := &jsonTreeNode{Key: key, Path: path, Kind: jsonObjectNode, Parent: parent}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				childKey, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("JSON object key is %T, want string", keyToken)
				}
				child, err := decodeJSONTree(decoder, childKey, jsonChildPath(path, childKey), node)
				if err != nil {
					return nil, err
				}
				node.Children = append(node.Children, child)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return node, nil
		case '[':
			node := &jsonTreeNode{Key: key, Path: path, Kind: jsonArrayNode, Parent: parent}
			for index := 0; decoder.More(); index++ {
				child, err := decodeJSONTree(decoder, "", jsonChildPath(path, strconv.Itoa(index)), node)
				if err != nil {
					return nil, err
				}
				node.Children = append(node.Children, child)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return node, nil
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
	}

	node := &jsonTreeNode{Key: key, Path: path, Parent: parent}
	switch value := token.(type) {
	case string:
		node.Kind = jsonStringNode
		node.Value = value
		node.Raw = string(marshalNoEscape(value))
	case json.Number:
		node.Kind = jsonNumberNode
		node.Value = string(value)
		node.Raw = string(value)
	case bool:
		node.Kind = jsonBoolNode
		node.Value = strconv.FormatBool(value)
		node.Raw = node.Value
	case nil:
		node.Kind = jsonNullNode
		node.Value = "null"
		node.Raw = "null"
	default:
		return nil, fmt.Errorf("unsupported JSON value %T", token)
	}
	return node, nil
}

func jsonChildPath(parent, child string) string {
	return parent + "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(child)
}

func (node *jsonTreeNode) IsContainer() bool {
	return node != nil && (node.Kind == jsonObjectNode || node.Kind == jsonArrayNode)
}

func (node *jsonTreeNode) RawJSON() string {
	if node == nil {
		return "null"
	}
	if !node.IsContainer() {
		return node.Raw
	}
	var builder strings.Builder
	if node.Kind == jsonObjectNode {
		builder.WriteByte('{')
		for i, child := range node.Children {
			if i > 0 {
				builder.WriteByte(',')
			}
			builder.Write(marshalNoEscape(child.Key))
			builder.WriteByte(':')
			builder.WriteString(child.RawJSON())
		}
		builder.WriteByte('}')
	} else {
		builder.WriteByte('[')
		for i, child := range node.Children {
			if i > 0 {
				builder.WriteByte(',')
			}
			builder.WriteString(child.RawJSON())
		}
		builder.WriteByte(']')
	}
	return builder.String()
}

func visibleJSONNodes(root *jsonTreeNode) []*jsonTreeNode {
	if root == nil {
		return nil
	}
	result := make([]*jsonTreeNode, 0)
	var visit func(*jsonTreeNode)
	visit = func(node *jsonTreeNode) {
		result = append(result, node)
		if node.Collapsed {
			return
		}
		for _, child := range node.Children {
			visit(child)
		}
	}
	visit(root)
	return result
}

func jsonNodeDepth(node *jsonTreeNode) int {
	depth := 0
	for node != nil && node.Parent != nil {
		depth++
		node = node.Parent
	}
	return depth
}

func (m dashboardModel) currentPayload() (TracePayload, bool) {
	trace, ok := m.selectedTrace()
	if !ok {
		return TracePayload{}, false
	}
	payload, _ := tracePanel(trace, m.panel)
	return payload, true
}

func (m dashboardModel) jsonTreeFor(body string) (*jsonTreeNode, bool) {
	if m.cache != nil && m.cache.treeValid && m.cache.treeBody == body {
		return m.cache.tree, m.cache.tree != nil
	}
	tree, err := parseJSONTree(body)
	if m.cache != nil {
		m.cache.treeBody = body
		m.cache.tree = tree
		m.cache.treeValid = true
	}
	return tree, err == nil
}

func (m dashboardModel) currentJSONTree() (*jsonTreeNode, bool) {
	payload, ok := m.currentPayload()
	if !ok || !json.Valid([]byte(payload.Body)) {
		return nil, false
	}
	return m.jsonTreeFor(payload.Body)
}

func (m dashboardModel) jsonActive() bool {
	_, ok := m.currentJSONTree()
	return ok
}

func (m *dashboardModel) visibleJSONNodes() ([]*jsonTreeNode, bool) {
	tree, ok := m.currentJSONTree()
	if !ok {
		return nil, false
	}
	nodes := visibleJSONNodes(tree)
	if len(nodes) == 0 {
		return nil, false
	}
	if m.jsonCursor == "" || !containsJSONPath(nodes, m.jsonCursor) {
		m.jsonCursor = nodes[0].Path
	}
	return nodes, true
}

func containsJSONPath(nodes []*jsonTreeNode, path string) bool {
	for _, node := range nodes {
		if node.Path == path {
			return true
		}
	}
	return false
}

func (m *dashboardModel) selectedJSONNode() (*jsonTreeNode, bool) {
	nodes, ok := m.visibleJSONNodes()
	if !ok {
		return nil, false
	}
	for _, node := range nodes {
		if node.Path == m.jsonCursor {
			return node, true
		}
	}
	return nodes[0], true
}
func (m dashboardModel) jsonCursorPath() string {
	node, ok := m.selectedJSONNode()
	if !ok || node.Path == "" {
		if ok {
			return "/"
		}
		return ""
	}
	return node.Path
}

func (m *dashboardModel) moveJSONCursor(delta int) bool {
	nodes, ok := m.visibleJSONNodes()
	if !ok {
		return false
	}
	index := 0
	for i, node := range nodes {
		if node.Path == m.jsonCursor {
			index = i
			break
		}
	}
	next := min(max(0, index+delta), len(nodes)-1)
	if next == index {
		return false
	}
	m.jsonCursor = nodes[next].Path
	m.ensureJSONCursorVisible(len(nodes))
	return true
}

func (m *dashboardModel) ensureJSONCursorVisible(nodeCount int) {
	_, height := m.detailDimensions()
	if nodeCount <= height || height <= 0 {
		return
	}
	index := 0
	if nodes, ok := m.visibleJSONNodes(); ok {
		for i, node := range nodes {
			if node.Path == m.jsonCursor {
				index = i
				break
			}
		}
	}
	if index < m.scroll {
		m.scroll = index
	}
	if index >= m.scroll+height {
		m.scroll = index - height + 1
	}
}

func (m *dashboardModel) toggleJSONNode() bool {
	node, ok := m.selectedJSONNode()
	if !ok || !node.IsContainer() {
		return false
	}
	node.Collapsed = !node.Collapsed
	m.invalidateViewer()
	return true
}

func (m *dashboardModel) expandJSONNode() bool {
	node, ok := m.selectedJSONNode()
	if !ok || !node.IsContainer() {
		return false
	}
	if node.Collapsed {
		node.Collapsed = false
		m.invalidateViewer()
		return true
	}
	if len(node.Children) > 0 {
		m.jsonCursor = node.Children[0].Path
		m.scroll = 0
		return true
	}
	return false
}

func (m *dashboardModel) collapseJSONNode() bool {
	node, ok := m.selectedJSONNode()
	if !ok {
		return false
	}
	if node.IsContainer() && !node.Collapsed {
		node.Collapsed = true
		m.invalidateViewer()
		return true
	}
	if node.Parent != nil {
		m.jsonCursor = node.Parent.Path
		m.scroll = 0
		return true
	}
	return false
}

func (m *dashboardModel) invalidateViewer() {
	if m.cache != nil {
		m.cache.detailValid = false
	}
}

func (m dashboardModel) renderJSONTree(body string, width int) ([]string, []string, bool) {
	tree, ok := m.jsonTreeFor(body)
	if !ok {
		return nil, nil, false
	}
	nodes := visibleJSONNodes(tree)
	lines := make([]string, 0, len(nodes))
	paths := make([]string, 0, len(nodes))
	for _, node := range nodes {
		line := jsonTreeNodeLine(node, node.Path == m.jsonCursor)
		key := encodeNodePath(node)
		if m.noWrap {
			lines = append(lines, highlightJSONLine(horizontalSlice(line, m.horizontal, width)))
			paths = append(paths, key)
			continue
		}
		for _, segment := range wrapJSONLine(line, width) {
			lines = append(lines, highlightJSONLine(segment))
			paths = append(paths, key)
		}
	}
	return lines, paths, true
}

// jsonNodePathSentinel tags a detail line as belonging to a JSON tree node so a
// mouse click can distinguish "root node" (empty path) from "not a node line".
const jsonNodePathSentinel = "\x00node\x00"

func encodeNodePath(node *jsonTreeNode) string {
	return jsonNodePathSentinel + node.Path
}

func decodeNodePath(entry string) (string, bool) {
	if !strings.HasPrefix(entry, jsonNodePathSentinel) {
		return "", false
	}
	return entry[len(jsonNodePathSentinel):], true
}

func jsonTreeNodeLine(node *jsonTreeNode, selected bool) string {
	indent := strings.Repeat("  ", jsonNodeDepth(node))
	marker := "  "
	if selected {
		marker = "> "
	}
	branch := "-"
	if node.IsContainer() {
		branch = "v"
		if node.Collapsed {
			branch = ">"
		}
	}
	label := "root"
	if node.Key != "" {
		label = fmt.Sprintf("%q:", node.Key)
	} else if node.Parent != nil {
		label = fmt.Sprintf("[%s]", jsonPathLeaf(node.Path))
	}
	return fmt.Sprintf("%s%s%s %s %s", indent, marker, branch, label, jsonNodeSummary(node))
}

func jsonPathLeaf(path string) string {
	if index := strings.LastIndexByte(path, '/'); index >= 0 {
		return strings.NewReplacer("~1", "/", "~0", "~").Replace(path[index+1:])
	}
	return path
}

func jsonNodeSummary(node *jsonTreeNode) string {
	if node.IsContainer() {
		if node.Kind == jsonObjectNode {
			return fmt.Sprintf("{%d keys}", len(node.Children))
		}
		return fmt.Sprintf("[%d items]", len(node.Children))
	}
	return node.Raw
}
