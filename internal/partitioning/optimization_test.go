package partitioning

import "testing"

func TestComputePathWeight(t *testing.T) {
	w0, err := ComputePathWeight(0, 1.5)
	if err != nil {
		t.Fatalf("weight 0: %v", err)
	}
	w1, err := ComputePathWeight(1, 1.5)
	if err != nil {
		t.Fatalf("weight 1: %v", err)
	}
	w5, err := ComputePathWeight(5, 1.5)
	if err != nil {
		t.Fatalf("weight 5: %v", err)
	}
	if !(w0 > w1 && w1 > w5) {
		t.Fatalf("weights should decrease as replica count grows: %f %f %f", w0, w1, w5)
	}
	if _, err := ComputePathWeight(-1, 1); err == nil {
		t.Fatalf("negative replicaCount should fail")
	}
	if _, err := ComputePathWeight(1, 0); err == nil {
		t.Fatalf("non-positive beta should fail")
	}
}

func TestSelectHighWeightPaths(t *testing.T) {
	paths := []StatePath{
		mockStatePath("path0", "leaf0", 3),
		mockStatePath("path1", "leaf1", 3),
		mockStatePath("path2", "leaf2", 3),
		mockStatePath("path3", "leaf3", 3),
	}
	replicas := map[string]PathReplicaInfo{
		replicaMapKey(paths[0]): {ReplicaCount: 0},
		replicaMapKey(paths[1]): {ReplicaCount: 1},
		replicaMapKey(paths[2]): {ReplicaCount: 3},
		replicaMapKey(paths[3]): {ReplicaCount: 0},
	}
	weights, err := ComputePathWeights(paths, replicas, 1)
	if err != nil {
		t.Fatalf("ComputePathWeights: %v", err)
	}
	high, err := SelectHighWeightPaths(weights, 0.5)
	if err != nil {
		t.Fatalf("SelectHighWeightPaths: %v", err)
	}
	if len(high) != 2 {
		t.Fatalf("selected = %d, want 2", len(high))
	}
	if high[0].ReplicaCount != 0 || high[1].ReplicaCount != 0 {
		t.Fatalf("selected paths should have the lowest replica count: %+v", high)
	}
	if string(high[0].Path.Key) != "path0" || string(high[1].Path.Key) != "path3" {
		t.Fatalf("unexpected deterministic order: %s %s", high[0].Path.Key, high[1].Path.Key)
	}
	if _, err := SelectHighWeightPaths(weights, 0); err == nil {
		t.Fatalf("bad ratio should fail")
	}
}

func TestBuildPathReallocationPlans(t *testing.T) {
	path := mockStatePath("account-A", "leaf-A", 3)
	high := []PathWeight{
		{
			Path:         path,
			ReplicaCount: 1,
			Weight:       0.5,
			Holders:      []string{"node-existing"},
		},
	}
	cfg := OptimizationConfig{
		TargetReplicaCount: 3,
		CandidateStorageNodes: []StorageNode{
			{ID: "node-existing", FailureDomain: "fd-a", Capacity: 10, Used: 1, Load: 0.1},
			{ID: "node-full", FailureDomain: "fd-b", Capacity: 10, Used: 10, Load: 0.0},
			{ID: "node-low-diff", FailureDomain: "fd-b", Capacity: 10, Used: 1, Load: 0.2},
			{ID: "node-high-diff", FailureDomain: "fd-c", Capacity: 10, Used: 1, Load: 0.7},
			{ID: "node-same-low", FailureDomain: "fd-a", Capacity: 10, Used: 1, Load: 0.05},
		},
	}
	plans, err := BuildPathReallocationPlans(high, cfg)
	if err != nil {
		t.Fatalf("BuildPathReallocationPlans: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("plans = %d, want 1", len(plans))
	}
	plan := plans[0]
	if plan.AdditionalReplicaCount != len(plan.NewHolders) {
		t.Fatalf("AdditionalReplicaCount = %d, new holders = %d", plan.AdditionalReplicaCount, len(plan.NewHolders))
	}
	if plan.AdditionalReplicaCount != 2 {
		t.Fatalf("additional replicas = %d, want 2", plan.AdditionalReplicaCount)
	}
	want := []string{"node-low-diff", "node-high-diff"}
	for i := range want {
		if plan.NewHolders[i] != want[i] {
			t.Fatalf("new holder[%d] = %s, want %s", i, plan.NewHolders[i], want[i])
		}
	}
	for _, holder := range plan.NewHolders {
		if holder == "node-existing" || holder == "node-full" {
			t.Fatalf("selected invalid holder %s", holder)
		}
	}

	again, err := BuildPathReallocationPlans(high, cfg)
	if err != nil {
		t.Fatalf("BuildPathReallocationPlans second run: %v", err)
	}
	if !sameStrings(again[0].NewHolders, plan.NewHolders) {
		t.Fatalf("selection not deterministic: %v vs %v", again[0].NewHolders, plan.NewHolders)
	}
}

func TestOptimizePathDistribution(t *testing.T) {
	paths := []StatePath{
		mockStatePath("path0", "leaf0", 3),
		mockStatePath("path1", "leaf1", 3),
		mockStatePath("path2", "leaf2", 3),
	}
	replicas := map[string]PathReplicaInfo{
		replicaMapKey(paths[0]): {ReplicaCount: 0, Holders: nil},
		replicaMapKey(paths[1]): {ReplicaCount: 1, Holders: []string{"node-a"}},
		replicaMapKey(paths[2]): {ReplicaCount: 3, Holders: []string{"node-a", "node-b", "node-c"}},
	}
	cfg := OptimizationConfig{
		Beta:               1,
		HighWeightRatio:    1,
		TargetReplicaCount: 2,
		CandidateStorageNodes: []StorageNode{
			{ID: "node-a", FailureDomain: "fd-a", Capacity: 10, Used: 1, Load: 0.3},
			{ID: "node-b", FailureDomain: "fd-b", Capacity: 10, Used: 1, Load: 0.2},
			{ID: "node-c", FailureDomain: "fd-c", Capacity: 10, Used: 1, Load: 0.1},
		},
	}
	plans, high, err := OptimizePathDistribution(paths, replicas, cfg)
	if err != nil {
		t.Fatalf("OptimizePathDistribution: %v", err)
	}
	if len(high) != 3 {
		t.Fatalf("high weights = %d, want 3", len(high))
	}
	if len(plans) != 2 {
		t.Fatalf("plans = %d, want 2", len(plans))
	}
	for _, plan := range plans {
		if string(plan.Path.Key) == "path2" {
			t.Fatalf("path2 reached target and should not produce a plan")
		}
		if plan.CurrentReplicaCount >= cfg.TargetReplicaCount {
			t.Fatalf("plan generated for path at target: %+v", plan)
		}
	}
}

func TestDistributionOptimizationVerbose(t *testing.T) {
	paths := []StatePath{
		mockStatePath("account-A", "leaf-A", 3),
		mockStatePath("account-B", "leaf-B", 3),
		mockStatePath("account-C", "leaf-C", 3),
		mockStatePath("account-D", "leaf-D", 3),
	}
	replicas := map[string]PathReplicaInfo{
		replicaMapKey(paths[0]): {ReplicaCount: 0, Holders: nil},
		replicaMapKey(paths[1]): {ReplicaCount: 1, Holders: []string{"node-a"}},
		replicaMapKey(paths[2]): {ReplicaCount: 3, Holders: []string{"node-a", "node-b", "node-c"}},
		replicaMapKey(paths[3]): {ReplicaCount: 0, Holders: nil},
	}
	cfg := OptimizationConfig{
		Beta:               1.2,
		HighWeightRatio:    0.5,
		TargetReplicaCount: 2,
		CandidateStorageNodes: []StorageNode{
			{ID: "node-a", FailureDomain: "fd-a", Capacity: 10, Used: 1, Load: 0.3},
			{ID: "node-b", FailureDomain: "fd-b", Capacity: 10, Used: 1, Load: 0.1},
			{ID: "node-c", FailureDomain: "fd-c", Capacity: 10, Used: 10, Load: 0.0},
			{ID: "node-d", FailureDomain: "fd-d", Capacity: 10, Used: 2, Load: 0.2},
		},
	}
	t.Logf("weight formula: weight = 1 / math.Pow(1 + replicaCount, beta), beta=%f", cfg.Beta)
	for _, path := range paths {
		info := replicas[replicaMapKey(path)]
		t.Logf("path=%s replicaCount=%d holders=%v", path.Key, info.ReplicaCount, info.Holders)
	}
	weights, err := ComputePathWeights(paths, replicas, cfg.Beta)
	if err != nil {
		t.Fatalf("ComputePathWeights: %v", err)
	}
	for _, weight := range weights {
		t.Logf("weight path=%s replicaCount=%d weight=%f holders=%v", weight.Path.Key, weight.ReplicaCount, weight.Weight, weight.Holders)
	}
	high, err := SelectHighWeightPaths(weights, cfg.HighWeightRatio)
	if err != nil {
		t.Fatalf("SelectHighWeightPaths: %v", err)
	}
	t.Logf("HighWeightRatio=%f selected=%d TargetReplicaCount=%d", cfg.HighWeightRatio, len(high), cfg.TargetReplicaCount)
	for i, weight := range high {
		t.Logf("high[%d] path=%s weight=%f replicas=%d", i, weight.Path.Key, weight.Weight, weight.ReplicaCount)
	}
	t.Logf("CandidateStorageNodes:")
	for _, node := range cfg.CandidateStorageNodes {
		t.Logf("  id=%s fd=%s capacity=%d used=%d load=%f available=%t", node.ID, node.FailureDomain, node.Capacity, node.Used, node.Load, node.Used < node.Capacity)
	}
	plans, err := BuildPathReallocationPlans(high, cfg)
	if err != nil {
		t.Fatalf("BuildPathReallocationPlans: %v", err)
	}
	for _, plan := range plans {
		t.Logf("plan path=%s existing=%v new=%v additional=%d current=%d target=%d", plan.Path.Key, plan.ExistingHolders, plan.NewHolders, plan.AdditionalReplicaCount, plan.CurrentReplicaCount, plan.TargetReplicaCount)
	}
	if len(plans) == 0 {
		t.Fatalf("expected at least one reallocation plan")
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
