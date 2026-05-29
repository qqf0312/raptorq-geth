package mptagg

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// FormatLocalPartitionViewTree returns a deterministic tree-like rendering of
// the final local compressed MPT view. It is a diagnostics helper only.
func FormatLocalPartitionViewTree(view *LocalPartitionView) string {
	if view == nil {
		return "<nil local partition view>\n"
	}
	nodes := localViewNodesByID(view)
	children := localViewChildrenByID(view)
	roots := localViewRootIDs(view, children, nodes)

	var out strings.Builder
	if len(roots) == 0 {
		out.WriteString("<empty local compressed MPT>\n")
		return out.String()
	}
	visited := make(map[string]int)
	for _, root := range roots {
		formatLocalTreeNode(&out, root, "", nodes, children, visited)
	}
	return out.String()
}

func localViewNodesByID(view *LocalPartitionView) map[string]*LocalViewNode {
	nodes := make(map[string]*LocalViewNode)
	for _, node := range view.Nodes {
		if node == nil || node.ID == "" {
			continue
		}
		if _, exists := nodes[node.ID]; exists {
			continue
		}
		nodes[node.ID] = node
	}
	return nodes
}

func localViewChildrenByID(view *LocalPartitionView) map[string][]LocalViewEdge {
	children := make(map[string][]LocalViewEdge)
	for _, edge := range view.Edges {
		if edge.FromID == "" || edge.ToID == "" {
			continue
		}
		children[edge.FromID] = append(children[edge.FromID], edge)
	}
	for from := range children {
		sort.Slice(children[from], func(i, j int) bool {
			if cmp := bytes.Compare(children[from][i].PathKey, children[from][j].PathKey); cmp != 0 {
				return cmp < 0
			}
			if children[from][i].ToID != children[from][j].ToID {
				return children[from][i].ToID < children[from][j].ToID
			}
			return children[from][i].Kind < children[from][j].Kind
		})
		children[from] = dedupeLocalViewEdges(children[from])
	}
	return children
}

func localViewRootIDs(view *LocalPartitionView, children map[string][]LocalViewEdge, nodes map[string]*LocalViewNode) []string {
	hasParent := make(map[string]bool)
	for _, edges := range children {
		for _, edge := range edges {
			hasParent[edge.ToID] = true
		}
	}
	var roots []string
	seen := make(map[string]bool)
	for _, node := range view.Nodes {
		if node == nil || node.ID == "" || seen[node.ID] || hasParent[node.ID] {
			continue
		}
		roots = append(roots, node.ID)
		seen[node.ID] = true
	}
	sort.Slice(roots, func(i, j int) bool {
		return localViewNodeSortKey(nodes[roots[i]]) < localViewNodeSortKey(nodes[roots[j]])
	})
	return roots
}

func formatLocalTreeNode(out *strings.Builder, id, indent string, nodes map[string]*LocalViewNode, children map[string][]LocalViewEdge, visited map[string]int) {
	node := nodes[id]
	if node == nil {
		out.WriteString(indent)
		out.WriteString("REF id=")
		out.WriteString(id)
		out.WriteByte('\n')
		return
	}
	out.WriteString(indent)
	out.WriteString(formatLocalViewNodeLine(node))
	out.WriteByte('\n')
	if node.Kind == AggregatedCommitmentNode && node.Aggregated != nil {
		for _, attached := range node.Aggregated.AttachedRawNodes {
			out.WriteString(indent)
			out.WriteString("  ")
			out.WriteString(formatAttachedRawNodeLine(attached))
			out.WriteByte('\n')
		}
	}
	visited[id]++
	if visited[id] > 1 {
		out.WriteString(indent)
		out.WriteString("  ")
		out.WriteString("REF id=")
		out.WriteString(id)
		out.WriteString(" already printed")
		out.WriteByte('\n')
		visited[id]--
		return
	}
	for _, edge := range children[id] {
		formatLocalTreeNode(out, edge.ToID, indent+"  ", nodes, children, visited)
	}
	visited[id]--
}

func formatLocalViewNodeLine(node *LocalViewNode) string {
	switch node.Kind {
	case RawMPTNode:
		if node.RawNode == nil {
			return fmt.Sprintf("RAW id=%s path=%s", node.ID, formatLocalBytes(node.PathKey))
		}
		return fmt.Sprintf("RAW hash=%s path=%s pos=%d", formatLocalHash(node.RawNode.Hash), formatLocalBytes(node.PathKey), node.RawNode.Position)
	case AggregatedCommitmentNode:
		agg := node.Aggregated
		if agg == nil {
			return fmt.Sprintf("AGG id=%s path=%s", node.ID, formatLocalBytes(node.PathKey))
		}
		return fmt.Sprintf("AGG id=%s path=%s nibbles=%x commitmentPrefixZeros=%d hashes=%s commit=%s",
			agg.ID, formatLocalBytes(node.PathKey), agg.Nibbles, agg.CommitmentPrefixZeros, formatLocalHashes(agg.OriginalNodeHashes), formatG1Local(agg.Commitment))
	default:
		return fmt.Sprintf("UNKNOWN id=%s path=%s", node.ID, formatLocalBytes(node.PathKey))
	}
}

func formatAttachedRawNodeLine(node AttachedRawNode) string {
	return fmt.Sprintf("ATTACHED RAW pos=%d hash=%s path=%s nodeID=%s", node.Position, formatLocalHash(node.Hash), formatLocalBytes(node.PathKey), node.NodeID)
}

func formatLocalHashes(hashes [][]byte) string {
	out := ""
	for i := range hashes {
		if i > 0 {
			out += "->"
		}
		out += formatLocalHash(hashes[i])
	}
	return out
}

func formatLocalHash(hash []byte) string {
	if len(hash) == 0 {
		return ""
	}
	if len(hash) <= 3 {
		return formatLocalBytes(hash)
	}
	return "0x" + hex.EncodeToString(hash[:3]) + "..."
}

func formatLocalBytes(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return "0x" + hex.EncodeToString(b)
		}
	}
	return string(b)
}

func dedupeLocalViewEdges(edges []LocalViewEdge) []LocalViewEdge {
	out := edges[:0]
	seen := make(map[string]bool, len(edges))
	for _, edge := range edges {
		key := fmt.Sprintf("%s|%s|%s", edge.FromID, edge.ToID, edge.Kind)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, edge)
	}
	return out
}

func localViewNodeSortKey(node *LocalViewNode) string {
	if node == nil {
		return ""
	}
	if node.Kind == RawMPTNode && node.RawNode != nil {
		return fmt.Sprintf("%s:%08d:%s", node.PathKey, node.RawNode.Position, node.RawNode.Hash)
	}
	if node.Kind == AggregatedCommitmentNode && node.Aggregated != nil {
		return fmt.Sprintf("%s:%s", node.PathKey, node.Aggregated.ID)
	}
	return fmt.Sprintf("%s:%s", node.PathKey, node.ID)
}
