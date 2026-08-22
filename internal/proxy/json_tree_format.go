package proxy

import "strings"

func prettyJSONTree(node *jsonTreeNode) string {
	return formatJSONTree(node, 0)
}

func formatJSONTree(node *jsonTreeNode, depth int) string {
	if node == nil || !node.IsContainer() {
		if node == nil {
			return "null"
		}
		return node.Raw
	}

	indent := strings.Repeat("  ", depth)
	childIndent := strings.Repeat("  ", depth+1)
	var result strings.Builder
	if node.Kind == jsonObjectNode {
		result.WriteByte('{')
		for i, child := range node.Children {
			if i == 0 {
				result.WriteByte('\n')
			} else {
				result.WriteString(",\n")
			}
			result.WriteString(childIndent)
			result.Write(marshalNoEscape(child.Key))
			result.WriteString(": ")
			result.WriteString(formatJSONTree(child, depth+1))
		}
		if len(node.Children) > 0 {
			result.WriteByte('\n')
			result.WriteString(indent)
		}
		result.WriteByte('}')
		return result.String()
	}

	result.WriteByte('[')
	for i, child := range node.Children {
		if i == 0 {
			result.WriteByte('\n')
		} else {
			result.WriteString(",\n")
		}
		result.WriteString(childIndent)
		result.WriteString(formatJSONTree(child, depth+1))
	}
	if len(node.Children) > 0 {
		result.WriteByte('\n')
		result.WriteString(indent)
	}
	result.WriteByte(']')
	return result.String()
}
