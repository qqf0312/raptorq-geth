package partitioning

import (
	"bytes"
	"fmt"
	"testing"
)

func TestDeterminePartitionCount(t *testing.T) {
	base := PartitionConfig{
		MinPartitions:       2,
		MaxPartitions:       8,
		BasePartitions:      4,
		TargetBlockInterval: 10,
		PreviousBlockTime:   100,
	}
	tests := []struct {
		name    string
		cfg     PartitionConfig
		want    int
		wantErr bool
	}{
		{name: "increase when active", cfg: withCurrent(base, 105), want: 8},
		{name: "decrease when slow", cfg: withCurrent(base, 120), want: 2},
		{name: "clamp min", cfg: withCurrent(PartitionConfig{MinPartitions: 3, MaxPartitions: 8, BasePartitions: 4, TargetBlockInterval: 10, PreviousBlockTime: 100}, 200), want: 3},
		{name: "clamp max", cfg: withCurrent(base, 101), want: 8},
		{name: "bad delta", cfg: withCurrent(base, 100), wantErr: true},
		{name: "bad base", cfg: withCurrent(PartitionConfig{MinPartitions: 1, MaxPartitions: 8, BasePartitions: 0, TargetBlockInterval: 10, PreviousBlockTime: 100}, 110), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DeterminePartitionCount(tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("DeterminePartitionCount: %v", err)
			}
			if got != tt.want {
				t.Fatalf("count = %d, want %d", got, tt.want)
			}
			if got < tt.cfg.MinPartitions || got > tt.cfg.MaxPartitions {
				t.Fatalf("count %d outside bounds [%d,%d]", got, tt.cfg.MinPartitions, tt.cfg.MaxPartitions)
			}
		})
	}
}

func TestSortStatePaths(t *testing.T) {
	paths := []StatePath{
		mockStatePath("keyC", "hash-C", 2),
		mockStatePath("keyA", "hash-B", 2),
		mockStatePath("keyB", "hash-A", 2),
	}
	byKey, err := SortStatePaths(paths, SortByKey)
	if err != nil {
		t.Fatalf("SortStatePaths key: %v", err)
	}
	assertKeys(t, byKey, []string{"keyA", "keyB", "keyC"})
	assertKeys(t, paths, []string{"keyC", "keyA", "keyB"})

	byHash, err := SortStatePaths(paths, SortByHash)
	if err != nil {
		t.Fatalf("SortStatePaths hash: %v", err)
	}
	assertKeys(t, byHash, []string{"keyB", "keyA", "keyC"})

	if _, err := SortStatePaths(paths, "bad"); err == nil {
		t.Fatalf("invalid sortBy should fail")
	}
}

func TestBuildPartitionsBalanced(t *testing.T) {
	paths := make([]StatePath, 10)
	for i := range paths {
		paths[i] = mockStatePath(fmt.Sprintf("key%02d", i), fmt.Sprintf("leaf%02d", i), 3)
	}
	partitions, err := BuildPartitions(paths, 3, SortByKey)
	if err != nil {
		t.Fatalf("BuildPartitions: %v", err)
	}
	if len(partitions) != 3 {
		t.Fatalf("partition count = %d, want 3", len(partitions))
	}
	wantSizes := []int{4, 3, 3}
	seen := make(map[string]struct{})
	for i, part := range partitions {
		if part.ID != i {
			t.Fatalf("partition ID = %d, want %d", part.ID, i)
		}
		if len(part.Paths) != wantSizes[i] {
			t.Fatalf("partition %d size = %d, want %d", i, len(part.Paths), wantSizes[i])
		}
		if part.StartIndex != sum(wantSizes[:i]) || part.EndIndex != sum(wantSizes[:i+1]) {
			t.Fatalf("partition %d range = [%d,%d), want [%d,%d)", i, part.StartIndex, part.EndIndex, sum(wantSizes[:i]), sum(wantSizes[:i+1]))
		}
		if len(part.Paths) > 0 {
			if !bytes.Equal(part.StartKey, part.Paths[0].Key) {
				t.Fatalf("partition %d StartKey mismatch", i)
			}
			if !bytes.Equal(part.EndKey, part.Paths[len(part.Paths)-1].Key) {
				t.Fatalf("partition %d EndKey mismatch", i)
			}
		}
		for _, path := range part.Paths {
			key := string(path.Key)
			if _, ok := seen[key]; ok {
				t.Fatalf("path %s appeared more than once", key)
			}
			seen[key] = struct{}{}
			if len(path.NodeHashes) != 3 {
				t.Fatalf("path %s NodeHashes split or changed: %d", key, len(path.NodeHashes))
			}
		}
	}
	if len(seen) != len(paths) {
		t.Fatalf("seen paths = %d, want %d", len(seen), len(paths))
	}

	if _, err := BuildPartitions(paths, 0, SortByKey); err == nil {
		t.Fatalf("invalid partitionCount should fail")
	}
	empty, err := BuildPartitions(nil, 3, SortByKey)
	if err != nil {
		t.Fatalf("empty BuildPartitions: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty paths should return empty partitions")
	}
}

func TestLocatePartitionForPath(t *testing.T) {
	paths := make([]StatePath, 7)
	for i := range paths {
		paths[i] = mockStatePath(fmt.Sprintf("key%02d", i), fmt.Sprintf("leaf%02d", i), 3)
	}
	partitions, err := BuildPartitions(paths, 3, SortByKey)
	if err != nil {
		t.Fatalf("BuildPartitions: %v", err)
	}
	for _, part := range partitions {
		for _, path := range part.Paths {
			got, ok := LocatePartitionForPath(partitions, path.Key)
			if !ok {
				t.Fatalf("key %s not found", path.Key)
			}
			if got != part.ID {
				t.Fatalf("key %s located in %d, want %d", path.Key, got, part.ID)
			}
		}
	}
	if _, ok := LocatePartitionForPath(partitions, []byte("missing")); ok {
		t.Fatalf("missing key should not be found")
	}
}

func TestPartitioningVerbose(t *testing.T) {
	paths := []StatePath{
		mockStatePath("account-C", "leaf-C", 3),
		mockStatePath("account-A", "leaf-A", 3),
		mockStatePath("account-B", "leaf-B", 3),
		mockStatePath("account-D", "leaf-D", 3),
	}
	cfg := PartitionConfig{
		MinPartitions:       1,
		MaxPartitions:       4,
		BasePartitions:      2,
		TargetBlockInterval: 10,
		PreviousBlockTime:   100,
		CurrentBlockTime:    105,
		SortBy:              SortByKey,
	}
	t.Logf("partition unit: one complete StatePath, not an individual MPT node")
	t.Logf("input StatePaths:")
	for i, path := range paths {
		t.Logf("  path[%d] key=%s nibbles=%x nodeHashes=%d leafHash=%x size=%d", i, path.Key, path.PathNibbles, len(path.NodeHashes), path.LeafHash, path.Size)
	}
	deltaT := cfg.CurrentBlockTime - cfg.PreviousBlockTime
	t.Logf("block times previous=%d current=%d deltaT=%d", cfg.PreviousBlockTime, cfg.CurrentBlockTime, deltaT)
	t.Logf("BasePartitions=%d TargetBlockInterval=%d", cfg.BasePartitions, cfg.TargetBlockInterval)
	count, err := DeterminePartitionCount(cfg)
	if err != nil {
		t.Fatalf("DeterminePartitionCount: %v", err)
	}
	t.Logf("final partitionCount=%d", count)
	sorted, err := SortStatePaths(paths, cfg.SortBy)
	if err != nil {
		t.Fatalf("SortStatePaths: %v", err)
	}
	for i, path := range sorted {
		t.Logf("  sorted[%d] key=%s leafHash=%x", i, path.Key, path.LeafHash)
	}
	partitions, err := BuildPartitions(paths, count, cfg.SortBy)
	if err != nil {
		t.Fatalf("BuildPartitions: %v", err)
	}
	for _, part := range partitions {
		t.Logf("partition id=%d range=[%d,%d) startKey=%s endKey=%s paths=%d", part.ID, part.StartIndex, part.EndIndex, part.StartKey, part.EndKey, len(part.Paths))
		for _, path := range part.Paths {
			id, ok := LocatePartitionForPath(partitions, path.Key)
			t.Logf("  locate key=%s -> partition=%d ok=%t", path.Key, id, ok)
		}
	}
}

func withCurrent(cfg PartitionConfig, current int64) PartitionConfig {
	cfg.CurrentBlockTime = current
	return cfg
}

func mockStatePath(key string, leafHash string, nodeHashCount int) StatePath {
	nodeHashes := make([][]byte, nodeHashCount)
	for i := range nodeHashes {
		nodeHashes[i] = []byte(fmt.Sprintf("%s-node-%d", key, i))
	}
	return StatePath{
		Key:         []byte(key),
		PathNibbles: []byte{byte(len(key)), byte(nodeHashCount)},
		NodeHashes:  nodeHashes,
		LeafHash:    []byte(leafHash),
		Size:        100 + len(key) + nodeHashCount,
	}
}

func assertKeys(t *testing.T, paths []StatePath, want []string) {
	t.Helper()
	if len(paths) != len(want) {
		t.Fatalf("len(paths) = %d, want %d", len(paths), len(want))
	}
	for i := range paths {
		if string(paths[i].Key) != want[i] {
			t.Fatalf("key[%d] = %s, want %s", i, paths[i].Key, want[i])
		}
	}
}

func sum(values []int) int {
	out := 0
	for _, value := range values {
		out += value
	}
	return out
}
