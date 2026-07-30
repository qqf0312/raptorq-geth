package fountainmptshadow

import (
	"encoding/binary"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/internal/mptproofmsg"
)

const (
	compactPathVersion      = 0xa1
	compactHistoryVersion   = 0xa2
	compactAggregateVersion = 0xa3
)

type compactReader struct {
	data []byte
	off  int
}

func appendUint(out []byte, value uint64) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], value)
	return append(out, buf[:n]...)
}

func appendBytes(out, value []byte) []byte {
	out = appendUint(out, uint64(len(value)))
	return append(out, value...)
}

func (r *compactReader) uint() (uint64, error) {
	value, n := binary.Uvarint(r.data[r.off:])
	if n <= 0 {
		return 0, fmt.Errorf("invalid compact integer")
	}
	r.off += n
	return value, nil
}

func (r *compactReader) fixed(size int) ([]byte, error) {
	if size < 0 || r.off > len(r.data)-size {
		return nil, fmt.Errorf("truncated compact record")
	}
	value := append([]byte(nil), r.data[r.off:r.off+size]...)
	r.off += size
	return value, nil
}

func (r *compactReader) bytes() ([]byte, error) {
	size, err := r.uint()
	if err != nil || size > uint64(len(r.data)-r.off) {
		return nil, fmt.Errorf("invalid compact byte string")
	}
	return r.fixed(int(size))
}

func (r *compactReader) done() error {
	if r.off != len(r.data) {
		return fmt.Errorf("trailing compact record bytes")
	}
	return nil
}

func encodeCompactPath(path storedEncodedPath) []byte {
	out := []byte{compactPathVersion}
	out = appendUint(out, path.AggregateHeight)
	out = append(out, path.AggregateID[:]...)
	return out
}

func decodeCompactPath(blob []byte) (storedEncodedPath, error) {
	if len(blob) == 0 || blob[0] != compactPathVersion {
		return storedEncodedPath{}, fmt.Errorf("invalid compact path version")
	}
	r := compactReader{data: blob, off: 1}
	height, err := r.uint()
	if err != nil {
		return storedEncodedPath{}, err
	}
	id, err := r.fixed(common.HashLength)
	if err != nil {
		return storedEncodedPath{}, err
	}
	if err := r.done(); err != nil {
		return storedEncodedPath{}, err
	}
	return storedEncodedPath{
		Aggregated:      true,
		AggregateHeight: height,
		AggregateID:     common.BytesToHash(id),
	}, nil
}

func encodeCompactHistory(leaf storedHistoricalLeaf) []byte {
	out := []byte{compactHistoryVersion}
	out = appendUint(out, leaf.AggregateHeight)
	out = append(out, leaf.AggregateID[:]...)
	return appendBytes(out, leaf.Leaf)
}

func decodeCompactHistory(blob []byte) (storedHistoricalLeaf, error) {
	if len(blob) == 0 || blob[0] != compactHistoryVersion {
		return storedHistoricalLeaf{}, fmt.Errorf("invalid compact history version")
	}
	r := compactReader{data: blob, off: 1}
	height, err := r.uint()
	if err != nil {
		return storedHistoricalLeaf{}, err
	}
	id, err := r.fixed(common.HashLength)
	if err != nil {
		return storedHistoricalLeaf{}, err
	}
	leaf, err := r.bytes()
	if err != nil {
		return storedHistoricalLeaf{}, err
	}
	if err := r.done(); err != nil {
		return storedHistoricalLeaf{}, err
	}
	return storedHistoricalLeaf{
		Leaf:            leaf,
		Aggregated:      true,
		AggregateHeight: height,
		AggregateID:     common.BytesToHash(id),
	}, nil
}

func encodeCompactAggregate(stored storedEpochAggregate) []byte {
	out := []byte{compactAggregateVersion}
	out = appendUint(out, stored.Height)
	out = append(out, stored.PathRoot[:]...)
	out = appendBytes(out, stored.PathSeedBase)
	out = appendUint(out, uint64(len(stored.Relations)))
	for _, relation := range stored.Relations {
		out = append(out, relation.Kind)
		out = append(out, relation.Root[:]...)
		out = appendBytes(out, relation.Key)
		out = appendBytes(out, relation.Seed)
		for _, value := range []uint64{
			relation.Length, relation.SelectStart, relation.SelectCount,
			relation.NodeCount, relation.CompactBytes, relation.StartHeight,
			relation.TerminalHeight,
		} {
			out = appendUint(out, value)
		}
	}
	out = appendUint(out, uint64(len(stored.Segments)))
	for _, segment := range stored.Segments {
		out = appendBytes(out, segment.Q)
	}
	out = appendScalarMatrix(out, [][]mptproofmsg.ScalarWire{stored.RowCs})
	out = appendScalarMatrix(out, stored.FoldChallenges)
	out = appendWireSlice(out, stored.IPAProof.L)
	out = appendWireSlice(out, stored.IPAProof.R)
	out = appendBytes(out, stored.IPAProof.AFinal)
	out = appendBytes(out, stored.IPAProof.BFinal)
	return out
}

func appendScalarMatrix(out []byte, matrix [][]mptproofmsg.ScalarWire) []byte {
	out = appendUint(out, uint64(len(matrix)))
	for _, row := range matrix {
		out = appendUint(out, uint64(len(row)))
		for _, scalar := range row {
			out = appendBytes(out, scalar)
		}
	}
	return out
}

func appendWireSlice(out []byte, points []mptproofmsg.G1Wire) []byte {
	out = appendUint(out, uint64(len(points)))
	for _, point := range points {
		out = appendBytes(out, point)
	}
	return out
}

func decodeCompactAggregate(blob []byte) (storedEpochAggregate, error) {
	if len(blob) == 0 || blob[0] != compactAggregateVersion {
		return storedEpochAggregate{}, fmt.Errorf("invalid compact aggregate version")
	}
	r := compactReader{data: blob, off: 1}
	height, err := r.uint()
	if err != nil {
		return storedEpochAggregate{}, err
	}
	pathRoot, err := r.fixed(common.HashLength)
	if err != nil {
		return storedEpochAggregate{}, err
	}
	seed, err := r.bytes()
	if err != nil {
		return storedEpochAggregate{}, err
	}
	relationCount, err := r.uint()
	if err != nil {
		return storedEpochAggregate{}, err
	}
	stored := storedEpochAggregate{
		Height:       height,
		PathRoot:     common.BytesToHash(pathRoot),
		PathSeedBase: seed,
		Relations:    make([]storedAggregateRelation, int(relationCount)),
	}
	for i := range stored.Relations {
		kind, err := r.fixed(1)
		if err != nil {
			return storedEpochAggregate{}, err
		}
		root, err := r.fixed(common.HashLength)
		if err != nil {
			return storedEpochAggregate{}, err
		}
		key, err := r.bytes()
		if err != nil {
			return storedEpochAggregate{}, err
		}
		relationSeed, err := r.bytes()
		if err != nil {
			return storedEpochAggregate{}, err
		}
		values := make([]uint64, 7)
		for j := range values {
			values[j], err = r.uint()
			if err != nil {
				return storedEpochAggregate{}, err
			}
		}
		stored.Relations[i] = storedAggregateRelation{
			Kind: kind[0], Root: common.BytesToHash(root), Key: key, Seed: relationSeed,
			Length: values[0], SelectStart: values[1], SelectCount: values[2],
			NodeCount: values[3], CompactBytes: values[4], StartHeight: values[5],
			TerminalHeight: values[6],
		}
	}
	segmentCount, err := r.uint()
	if err != nil {
		return storedEpochAggregate{}, err
	}
	stored.Segments = make([]storedAggregateSegmentCommitment, int(segmentCount))
	for i := range stored.Segments {
		q, err := r.bytes()
		if err != nil {
			return storedEpochAggregate{}, err
		}
		stored.Segments[i].Q = q
	}
	rowMatrix, err := readScalarMatrix(&r)
	if err != nil || len(rowMatrix) != 1 {
		return storedEpochAggregate{}, fmt.Errorf("invalid compact aggregate rows")
	}
	stored.RowCs = rowMatrix[0]
	stored.FoldChallenges, err = readScalarMatrix(&r)
	if err != nil {
		return storedEpochAggregate{}, err
	}
	stored.IPAProof.L, err = readWireSlice(&r)
	if err != nil {
		return storedEpochAggregate{}, err
	}
	stored.IPAProof.R, err = readWireSlice(&r)
	if err != nil {
		return storedEpochAggregate{}, err
	}
	stored.IPAProof.AFinal, err = r.bytes()
	if err != nil {
		return storedEpochAggregate{}, err
	}
	stored.IPAProof.BFinal, err = r.bytes()
	if err != nil {
		return storedEpochAggregate{}, err
	}
	return stored, r.done()
}

func readScalarMatrix(r *compactReader) ([][]mptproofmsg.ScalarWire, error) {
	rows, err := r.uint()
	if err != nil {
		return nil, err
	}
	out := make([][]mptproofmsg.ScalarWire, int(rows))
	for i := range out {
		columns, err := r.uint()
		if err != nil {
			return nil, err
		}
		out[i] = make([]mptproofmsg.ScalarWire, int(columns))
		for j := range out[i] {
			out[i][j], err = r.bytes()
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func readWireSlice(r *compactReader) ([]mptproofmsg.G1Wire, error) {
	count, err := r.uint()
	if err != nil {
		return nil, err
	}
	out := make([]mptproofmsg.G1Wire, int(count))
	for i := range out {
		out[i], err = r.bytes()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
