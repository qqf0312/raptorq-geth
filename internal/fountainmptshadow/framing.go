package fountainmptshadow

import (
	"encoding/binary"
	"fmt"
)

const nodeLengthPrefixSize = 4

// FramePathNodes serializes a path as a compact chunk-aligned stream and then
// splits it into fixed-size source symbols. Nodes are padded only to a 31-byte
// Fr boundary; only the final source symbol is padded to SourceSymbolSize.
func FramePathNodes(nodes [][]byte) ([][]byte, PathLayout, error) {
	if len(nodes) == 0 {
		return nil, PathLayout{}, fmt.Errorf("empty path")
	}
	if uint64(len(nodes)) > uint64(^uint32(0)) {
		return nil, PathLayout{}, fmt.Errorf("too many path nodes")
	}
	stream := make([]byte, SourceChunkSize)
	binary.BigEndian.PutUint32(stream[:nodeLengthPrefixSize], uint32(len(nodes)))
	for i := range nodes {
		if len(nodes[i]) == 0 {
			return nil, PathLayout{}, fmt.Errorf("empty path node %d", i)
		}
		if uint64(len(nodes[i])) > uint64(^uint32(0)) {
			return nil, PathLayout{}, fmt.Errorf("path node %d is too large", i)
		}
		recordSize := roundUp(nodeLengthPrefixSize+len(nodes[i]), SourceChunkSize)
		record, err := frameNode(nodes[i], uint64(recordSize))
		if err != nil {
			return nil, PathLayout{}, fmt.Errorf("frame path node %d: %w", i, err)
		}
		stream = append(stream, record...)
	}
	compactBytes := len(stream)
	sourceCount := (compactBytes + SourceSymbolSize - 1) / SourceSymbolSize
	stream = append(stream, make([]byte, sourceCount*SourceSymbolSize-compactBytes)...)
	files := make([][]byte, sourceCount)
	for i := range files {
		start := i * SourceSymbolSize
		files[i] = append([]byte(nil), stream[start:start+SourceSymbolSize]...)
	}
	return files, PathLayout{
		NodeCount:    uint64(len(nodes)),
		SourceCount:  uint64(sourceCount),
		FileSize:     SourceSymbolSize,
		CompactBytes: uint64(compactBytes),
	}, nil
}

func frameNode(node []byte, fileSize uint64) ([]byte, error) {
	if len(node) == 0 {
		return nil, fmt.Errorf("empty node")
	}
	if uint64(len(node)) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("node is too large")
	}
	if fileSize < nodeLengthPrefixSize || fileSize%SourceChunkSize != 0 {
		return nil, fmt.Errorf("invalid file size %d", fileSize)
	}
	if uint64(nodeLengthPrefixSize)+uint64(len(node)) > fileSize {
		return nil, fmt.Errorf("node length %d exceeds file size %d", len(node), fileSize)
	}
	file := make([]byte, int(fileSize))
	binary.BigEndian.PutUint32(file[:nodeLengthPrefixSize], uint32(len(node)))
	copy(file[nodeLengthPrefixSize:], node)
	return file, nil
}

// UnframePathNodes reverses FramePathNodes and rejects non-zero padding.
func UnframePathNodes(files [][]byte, layout PathLayout) ([][]byte, error) {
	if uint64(len(files)) != layout.SourceCount {
		return nil, fmt.Errorf("file count %d != layout source count %d", len(files), layout.SourceCount)
	}
	if layout.FileSize != SourceSymbolSize {
		return nil, fmt.Errorf("invalid layout file size %d", layout.FileSize)
	}
	stream := make([]byte, 0, len(files)*SourceSymbolSize)
	for i := range files {
		if uint64(len(files[i])) != layout.FileSize {
			return nil, fmt.Errorf("file %d length %d != layout file size %d", i, len(files[i]), layout.FileSize)
		}
		stream = append(stream, files[i]...)
	}
	if layout.CompactBytes < SourceChunkSize || layout.CompactBytes > uint64(len(stream)) ||
		layout.CompactBytes%SourceChunkSize != 0 {
		return nil, fmt.Errorf("invalid compact path length %d", layout.CompactBytes)
	}
	for _, b := range stream[layout.CompactBytes:] {
		if b != 0 {
			return nil, fmt.Errorf("non-zero final source-symbol padding")
		}
	}
	stream = stream[:layout.CompactBytes]
	nodeCount := uint64(binary.BigEndian.Uint32(stream[:nodeLengthPrefixSize]))
	if nodeCount != layout.NodeCount {
		return nil, fmt.Errorf("stream node count %d != layout node count %d", nodeCount, layout.NodeCount)
	}
	for _, b := range stream[nodeLengthPrefixSize:SourceChunkSize] {
		if b != 0 {
			return nil, fmt.Errorf("non-zero path header padding")
		}
	}
	out := make([][]byte, 0, nodeCount)
	offset := SourceChunkSize
	for i := uint64(0); i < nodeCount; i++ {
		if offset+nodeLengthPrefixSize > len(stream) {
			return nil, fmt.Errorf("missing node %d length", i)
		}
		nodeLen := int(binary.BigEndian.Uint32(stream[offset : offset+nodeLengthPrefixSize]))
		recordSize := roundUp(nodeLengthPrefixSize+nodeLen, SourceChunkSize)
		if nodeLen == 0 || offset+recordSize > len(stream) {
			return nil, fmt.Errorf("invalid node %d length %d", i, nodeLen)
		}
		nodeEnd := offset + nodeLengthPrefixSize + nodeLen
		for _, b := range stream[nodeEnd : offset+recordSize] {
			if b != 0 {
				return nil, fmt.Errorf("node %d has non-zero framing padding", i)
			}
		}
		out = append(out, append([]byte(nil), stream[offset+nodeLengthPrefixSize:nodeEnd]...))
		offset += recordSize
	}
	if offset != len(stream) {
		return nil, fmt.Errorf("compact path has %d trailing bytes", len(stream)-offset)
	}
	return out, nil
}

func frameCompactLeaf(leaf []byte) ([]byte, error) {
	return frameNode(leaf, uint64(roundUp(nodeLengthPrefixSize+len(leaf), SourceChunkSize)))
}

func roundUp(n, multiple int) int {
	if n%multiple == 0 {
		return n
	}
	return n + multiple - n%multiple
}
