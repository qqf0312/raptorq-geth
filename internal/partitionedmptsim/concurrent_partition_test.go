package partitionedmptsim

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/internal/fileipa"
	"github.com/ethereum/go-ethereum/internal/mptagg"
	"github.com/ethereum/go-ethereum/internal/mptagg/linearrecovery"
	"github.com/ethereum/go-ethereum/internal/mptproofmsg"
	"github.com/ethereum/go-ethereum/internal/mpttest"
	"github.com/ethereum/go-ethereum/internal/partitioning"
)

type simPartitionStorageMode int

const (
	simStoreFullOwned simPartitionStorageMode = iota
	simStoreCommitmentsOnly
	simStoreSparseRequester
	simStoreStaleArchive
)

type simPartitionNode struct {
	id          int
	mode        simPartitionStorageMode
	inbox       chan any
	roots       []string
	ownedByRoot map[string]map[string]partitioning.StatePath
	rawByRoot   map[string]map[string]bool
	recovered   map[string]map[string][]byte
	err         error
}

type simPartitionUpdateMsg struct {
	root      string
	partition partitioning.Partition
	rawSet    map[string]bool
	ack       chan error
}

type simPartitionProofRequestMsg struct {
	id          uint64
	root        string
	pathKey     string
	targetIndex int
	reply       chan simPartitionProofResponse
}

type simPartitionProofResponse struct {
	packet *mptproofmsg.FoldedFileIPAProofPacket
	err    error
}

type simPartitionRecoverMsg struct {
	root        string
	pathKey     string
	targetIndex int
	packet      *mptproofmsg.FoldedFileIPAProofPacket
	ack         chan error
}

type simPartitionStopMsg struct {
	done chan struct{}
}

func TestFourPartitionUpdateRepartitionsAndRecoversMissingPathData(t *testing.T) {
	const partitionCount = 4
	manager := &partitioning.MPTPartitionManager{
		PartitionCount:    partitionCount,
		SortBy:            partitioning.SortByKey,
		Config:            simOptimizationConfig(),
		PreserveLatest:    false,
		PreserveLatestSet: true,
	}

	nodes := []*simPartitionNode{
		newSimPartitionNode(0, simStoreFullOwned),
		newSimPartitionNode(1, simStoreCommitmentsOnly),
		newSimPartitionNode(2, simStoreSparseRequester),
		newSimPartitionNode(3, simStoreStaleArchive),
	}
	localOwned := fourPartitionOwnedRawNodeSets(partitionCount)
	localViewOpts := simulationCompressOptions(t)
	var wg sync.WaitGroup
	for _, node := range nodes {
		wg.Add(1)
		go node.run(&wg)
	}
	defer stopSimPartitionNodes(t, nodes, &wg)

	root1, err := mpttest.BuildOrUpdateTestMPT(nil, initialMPTEntries())
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT root1: %v", err)
	}
	result1, err := manager.AddLatestRoot(root1)
	if err != nil {
		t.Fatalf("AddLatestRoot root1: %v", err)
	}
	logFourPartitionRound(t, "round-1 initial MPT", root1, result1, localOwned, localViewOpts)
	applySimPartitionUpdate(t, nodes, result1)

	root2, err := mpttest.BuildOrUpdateTestMPT(root1, updateMPTEntries())
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT root2: %v", err)
	}
	result2, err := manager.AddLatestRoot(root2)
	if err != nil {
		t.Fatalf("AddLatestRoot root2: %v", err)
	}
	logFourPartitionRound(t, "round-2 first update", root2, result2, localOwned, localViewOpts)
	applySimPartitionUpdate(t, nodes, result2)

	root3, err := mpttest.BuildOrUpdateTestMPT(root2, secondUpdateMPTEntries())
	if err != nil {
		t.Fatalf("BuildOrUpdateTestMPT root3: %v", err)
	}
	result3, err := manager.AddLatestRoot(root3)
	if err != nil {
		t.Fatalf("AddLatestRoot root3: %v", err)
	}
	logFourPartitionRound(t, "round-3 second update", root3, result3, localOwned, localViewOpts)
	applySimPartitionUpdate(t, nodes, result3)

	t.Logf("")
	t.Logf("========== storage state after repartition updates ==========")
	for _, node := range nodes {
		t.Logf("partition-%d mode=%s roots=%v", node.id, simPartitionStorageModeLabel(node.mode), node.roots)
		if len(node.roots) != 3 {
			t.Fatalf("partition-%d roots = %d, want 3", node.id, len(node.roots))
		}
		if node.roots[2] != result3.RootHash {
			t.Fatalf("partition-%d latest root = %s, want %s", node.id, node.roots[2], result3.RootHash)
		}
		t.Logf("partition-%d latest owned paths=%d raw hashes=%d recovered paths=%d",
			node.id,
			len(node.ownedByRoot[result3.RootHash]),
			len(node.rawByRoot[result3.RootHash]),
			len(node.recovered[result3.RootHash]))
	}
	if len(nodes[1].ownedByRoot[result3.RootHash]) != 0 {
		t.Fatalf("partition-1 commitment-only storage retained full latest paths")
	}
	if len(nodes[3].ownedByRoot[result3.RootHash]) != 0 {
		t.Fatalf("partition-3 stale archive storage retained full latest paths")
	}
	if _, ok := nodes[3].ownedByRoot[result1.RootHash]; !ok {
		t.Fatalf("partition-3 stale archive did not retain old root namespace")
	}

	sender := nodes[0]
	receiver := nodes[2]
	targetPath, targetIndex := selectRecoveryTarget(t, result3, sender.id, receiver.id)
	if sender.path(result3.RootHash, targetPath.Key) == nil {
		t.Fatalf("sender partition-%d does not store selected path 0x%x", sender.id, targetPath.Key)
	}
	if receiver.path(result3.RootHash, targetPath.Key) != nil {
		t.Fatalf("receiver partition-%d unexpectedly stores selected path 0x%x", receiver.id, targetPath.Key)
	}
	if simRawSetHasNodeHash(receiver.rawByRoot[result3.RootHash], targetPath.Nodes[targetIndex].Hash) {
		t.Fatalf("test setup expected receiver to miss node %d from selected path", targetIndex)
	}
	t.Logf("")
	t.Logf("========== recovery target selection ==========")
	t.Logf("selected sender=partition-%d receiver=partition-%d root=%s path=0x%x missingNode=%d hashes=%s",
		sender.id, receiver.id, shortenRootAwareHash(result3.RootHash), targetPath.Key, targetIndex, simShortPathHashes(targetPath))
	for i, node := range targetPath.Nodes {
		t.Logf("target path node[%d] type=%s pos=%d parent=%d edge=0x%x prefix=%s hash=%s receiverHasRaw=%t",
			i,
			node.NodeType,
			node.Position,
			node.ParentPosition,
			node.EdgeNibble,
			mpttest.FormatNibbles(node.PrefixNibbles),
			simShortHashDisplay(node.Hash),
			simRawSetHasNodeHash(receiver.rawByRoot[result3.RootHash], node.Hash))
	}

	t.Logf("")
	t.Logf("========== proof request / generation ==========")
	t.Logf("request proof id=1 sender=partition-%d root=%s path=0x%x targetIndex=%d",
		sender.id, shortenRootAwareHash(result3.RootHash), targetPath.Key, targetIndex)
	reply := make(chan simPartitionProofResponse, 1)
	sender.inbox <- simPartitionProofRequestMsg{
		id:          1,
		root:        result3.RootHash,
		pathKey:     string(targetPath.Key),
		targetIndex: targetIndex,
		reply:       reply,
	}
	response := waitSimProofResponse(t, reply)
	if response.err != nil {
		t.Fatalf("proof response: %v", response.err)
	}
	logFoldedFileIPAProofPacket(t, "sender proof packet", response.packet)
	if response.packet.Root != common.HexToHash(result3.RootHash) {
		t.Fatalf("proof root = %s, want %s", response.packet.Root, result3.RootHash)
	}
	logFoldedFileIPAVerification(t, "pre-recovery verifier check", response.packet, targetIndex)

	t.Logf("")
	t.Logf("========== receiver recovery ==========")
	t.Logf("send recovery packet to receiver=partition-%d root=%s path=0x%x targetIndex=%d",
		receiver.id, shortenRootAwareHash(result3.RootHash), targetPath.Key, targetIndex)
	ack := make(chan error, 1)
	receiver.inbox <- simPartitionRecoverMsg{
		root:        result3.RootHash,
		pathKey:     string(targetPath.Key),
		targetIndex: targetIndex,
		packet:      response.packet,
		ack:         ack,
	}
	if err := waitSimAck(t, ack); err != nil {
		t.Fatalf("receiver recovery: %v", err)
	}
	recovered := receiver.recovered[result3.RootHash][string(targetPath.Key)]
	want := simPathNodeHashPayloads(t, targetPath)[targetIndex]
	t.Logf("receiver recovered bytes=%x", recovered)
	t.Logf("expected node payload=%x", want)
	if !bytes.Equal(recovered, want) {
		t.Fatalf("recovered payload mismatch: got %x want %x", recovered, want)
	}
	if _, ok := receiver.recovered[result2.RootHash][string(targetPath.Key)]; ok {
		t.Fatalf("receiver stored latest recovery under stale root")
	}
}

func fourPartitionOwnedRawNodeSets(partitionCount int) []*mptagg.OwnedRawNodeSet {
	out := make([]*mptagg.OwnedRawNodeSet, partitionCount)
	for i := range out {
		out[i] = mptagg.NewOwnedRawNodeSet()
	}
	return out
}

func logFourPartitionRound(t *testing.T, label string, root *mpttest.TestMPT, result *partitioning.MPTPartitionResult, owned []*mptagg.OwnedRawNodeSet, opts mptagg.CompressOptions) {
	t.Helper()
	t.Logf("")
	t.Logf("========== %s ==========", label)
	t.Logf("root=%s previous=%s latestPaths=%d changed=%d unchanged=%d removed=%d",
		result.RootHash,
		result.PreviousRootHash,
		len(result.LatestPaths),
		len(result.Diff.Changed),
		len(result.Diff.Unchanged),
		len(result.Diff.Removed))
	t.Logf("")
	t.Logf("----- full MPT tree -----")
	t.Logf("\n%s", mpttest.FormatTrie(root))
	t.Logf("----- extracted StatePaths -----")
	for _, path := range result.LatestPaths {
		t.Logf("  %s key=0x%x nibbles=%s hashes=%s",
			mpttest.LabelForPath(path),
			path.Key,
			mpttest.FormatNibbles(path.PathNibbles),
			simShortPathHashes(path))
	}
	t.Logf("----- changed paths -----")
	if len(result.Diff.Changed) == 0 {
		t.Logf("  none")
	}
	for _, path := range result.Diff.Changed {
		t.Logf("  %s key=0x%x hashes=%s", mpttest.LabelForPath(path), path.Key, simShortPathHashes(path))
	}
	t.Logf("----- primary partitions and raw node sets -----")
	for _, part := range result.Partitions {
		t.Logf("partition-%d range=[%d,%d) paths=%d start=%s end=%s rawHashes=%d %s",
			part.ID,
			part.StartIndex,
			part.EndIndex,
			len(part.Paths),
			simBytesDisplay(part.StartKey),
			simBytesDisplay(part.EndKey),
			len(result.RawNodeSets[part.ID]),
			rootAwareRawHashSummary(result.RawNodeSets[part.ID]))
		for _, path := range part.Paths {
			t.Logf("  owns %s key=0x%x hashes=%s", mpttest.LabelForPath(path), path.Key, simShortPathHashes(path))
		}
	}
	t.Logf("----- SuperNode reallocation plans -----")
	if len(result.Plans) == 0 {
		t.Logf("  none")
	}
	for i, plan := range result.Plans {
		t.Logf("  plan[%d] path=%s positions=[%d,%d] hashes=%s preferred=%d assigned=%d reason=%s",
			i,
			simBytesDisplay(plan.SuperNode.PathKey),
			plan.SuperNode.StartPosition,
			plan.SuperNode.EndPosition,
			simShortNodeHashes(plan.SuperNode.Nodes),
			plan.PreferredPartition,
			plan.AssignedPartition,
			plan.Reason)
	}
	t.Logf("----- local compressed views -----")
	for _, part := range result.Partitions {
		if part.ID < 0 || part.ID >= len(owned) {
			t.Fatalf("%s partition id %d outside owned set", label, part.ID)
		}
		view, err := mptagg.BuildLocalPartitionViewFromRootRawNodeSet(root, part.Paths, result.RawNodeSets[part.ID], owned[part.ID], part.ID, opts)
		if err != nil {
			t.Fatalf("%s BuildLocalPartitionViewFromRootRawNodeSet partition-%d: %v", label, part.ID, err)
		}
		t.Logf("")
		t.Logf("----- %s local view partition-%d -----", label, part.ID)
		t.Logf("summary rawPrimaryPaths=%d rawSuperNodes=%d candidateCompressed=%d compressed=%d rawAvailable=%s",
			len(view.RawPathKeys),
			len(view.RawSuperNodes),
			len(view.CandidateAggregatedNodes),
			len(view.AggregatedNodes),
			simRawAvailableHashes(view.RawAvailableHashes))
		t.Logf("tree:\n%s", shortenRootAwareCommitments(mptagg.FormatLocalPartitionViewTree(view)))
		t.Logf("compressed nodes:")
		if len(view.AggregatedNodes) == 0 {
			t.Logf("  none")
		}
		for _, agg := range view.AggregatedNodes {
			t.Logf("  agg id=%s path=%s nibbles=%x next=%d hashes=%s attachedRaw=%d commitment=%s",
				agg.ID,
				simBytesDisplay(agg.PathKey),
				agg.Nibbles,
				agg.NextPosition,
				simBytesHashes(agg.OriginalNodeHashes),
				len(agg.AttachedRawNodes),
				simCommitmentString(agg))
			for _, attached := range agg.AttachedRawNodes {
				t.Logf("    attached raw pos=%d path=%s hash=%s nodeID=%s",
					attached.Position,
					simBytesDisplay(attached.PathKey),
					simShortHashDisplay(attached.Hash),
					attached.NodeID)
			}
		}
	}
}

func logFoldedFileIPAProofPacket(t *testing.T, label string, packet *mptproofmsg.FoldedFileIPAProofPacket) {
	t.Helper()
	if packet == nil {
		t.Logf("%s: <nil packet>", label)
		return
	}
	t.Logf("%s: id=%d root=%s files=%d rows=%d foldRounds=%d vectorLength=%d params=%s domain=%s",
		label,
		packet.ID,
		packet.Root,
		len(packet.Files),
		len(packet.Rows),
		len(packet.FoldChallenges),
		packet.VectorLength,
		packet.ParamsID,
		packet.Domain)
	for i, file := range packet.Files {
		t.Logf("  file[%d] key=0x%x hash=%s size=%d", i, file.Key, file.Hash, file.Size)
	}
}

func logFoldedFileIPAVerification(t *testing.T, label string, packet *mptproofmsg.FoldedFileIPAProofPacket, targetIndex int) {
	t.Helper()
	t.Logf("")
	t.Logf("========== %s ==========", label)
	t.Logf("stage 1: decode packet to folded FileIPA result")
	result, sharedQ, domain, err := packet.ToFileIPAResult()
	if err != nil {
		t.Fatalf("ToFileIPAResult: %v", err)
	}
	t.Logf("  rows=%d sharedQ=%s domain=%s", len(result.Rows), simG1Summary(sharedQ), domain)
	for i, row := range result.Rows {
		t.Logf("  row[%d] len(A)=%d A_prefix=%s C=%s", i, len(row.A), simFrVectorSummary(row.A, 4), row.C.String())
	}
	t.Logf("stage 2: verify folded IPA proof")
	verified, err := mptproofmsg.VerifyFoldedFileIPAProofPacket(packet)
	if err != nil {
		t.Fatalf("VerifyFoldedFileIPAProofPacket: %v", err)
	}
	t.Logf("  verified=%t", verified)
	if !verified {
		t.Fatalf("folded FileIPA proof did not verify")
	}
	t.Logf("stage 3: recover target node from linear rows")
	rows := simRecoveryRowsFromFileIPARows(result.Rows)
	recovered, usedRows, err := linearrecovery.RecoverSingleNode(rows, len(packet.Files), targetIndex)
	if err != nil {
		t.Fatalf("RecoverSingleNode: %v", err)
	}
	t.Logf("  targetIndex=%d usedRows=%d recoveredFr=%s", targetIndex, len(usedRows), recovered.String())
	if targetIndex < 0 || targetIndex >= len(packet.Files) {
		t.Fatalf("target index %d outside file refs", targetIndex)
	}
	length := int(packet.Files[targetIndex].Size)
	recoveredBytes, err := fileipa.FrChunksToBytes([]fr.Element{recovered}, length)
	if err != nil {
		t.Fatalf("FrChunksToBytes: %v", err)
	}
	t.Logf("stage 4: convert recovered field element to raw node payload")
	t.Logf("  length=%d bytes=%x", length, recoveredBytes)
}

func simPartitionStorageModeLabel(mode simPartitionStorageMode) string {
	switch mode {
	case simStoreFullOwned:
		return "full-owned"
	case simStoreCommitmentsOnly:
		return "commitments-only"
	case simStoreSparseRequester:
		return "sparse-requester"
	case simStoreStaleArchive:
		return "stale-archive"
	default:
		return fmt.Sprintf("unknown-%d", mode)
	}
}

func secondUpdateMPTEntries() []mpttest.TestTrieEntry {
	return []mpttest.TestTrieEntry{
		{Name: "A", Key: []byte{0x12, 0x34}, Value: simLongValue("A-v3")},
		{Name: "W", Key: []byte{0x11, 0xee}, Value: simLongValue("W-v3")},
	}
}

func newSimPartitionNode(id int, mode simPartitionStorageMode) *simPartitionNode {
	return &simPartitionNode{
		id:          id,
		mode:        mode,
		inbox:       make(chan any, 8),
		ownedByRoot: make(map[string]map[string]partitioning.StatePath),
		rawByRoot:   make(map[string]map[string]bool),
		recovered:   make(map[string]map[string][]byte),
	}
}

func (node *simPartitionNode) run(wg *sync.WaitGroup) {
	defer wg.Done()
	for msg := range node.inbox {
		switch msg := msg.(type) {
		case simPartitionUpdateMsg:
			msg.ack <- node.applyUpdate(msg)
		case simPartitionProofRequestMsg:
			packet, err := node.buildProof(msg.id, msg.root, msg.pathKey)
			msg.reply <- simPartitionProofResponse{packet: packet, err: err}
		case simPartitionRecoverMsg:
			msg.ack <- node.recover(msg)
		case simPartitionStopMsg:
			close(msg.done)
			return
		}
	}
}

func (node *simPartitionNode) applyUpdate(msg simPartitionUpdateMsg) error {
	node.roots = append(node.roots, msg.root)
	node.rawByRoot[msg.root] = cloneStringBoolMap(msg.rawSet)
	if node.recovered[msg.root] == nil {
		node.recovered[msg.root] = make(map[string][]byte)
	}
	switch node.mode {
	case simStoreFullOwned:
		node.ownedByRoot[msg.root] = pathsByKey(msg.partition.Paths)
	case simStoreCommitmentsOnly, simStoreSparseRequester:
		node.ownedByRoot[msg.root] = make(map[string]partitioning.StatePath)
	case simStoreStaleArchive:
		if len(node.roots) == 1 {
			node.ownedByRoot[msg.root] = pathsByKey(msg.partition.Paths)
		} else {
			node.ownedByRoot[msg.root] = make(map[string]partitioning.StatePath)
		}
	default:
		return fmt.Errorf("unknown storage mode %d", node.mode)
	}
	return nil
}

func (node *simPartitionNode) buildProof(id uint64, root string, pathKey string) (*mptproofmsg.FoldedFileIPAProofPacket, error) {
	path := node.path(root, []byte(pathKey))
	if path == nil {
		return nil, fmt.Errorf("partition-%d missing path 0x%x at root %s", node.id, []byte(pathKey), root)
	}
	files := simPathNodeHashPayloadsNoT(*path)
	coeffSets := simRecoveryVandermondeCoeffSets(len(files))
	domain := "partitionedmptsim/four-partition/recovery-v1"
	folded, err := fileipa.BuildFoldedFileIPA(files, coeffSets, domain)
	if err != nil {
		return nil, err
	}
	sharedQ, err := simSharedFileIPACommitment(folded.Params, files)
	if err != nil {
		return nil, err
	}
	return mptproofmsg.NewFoldedFileIPAProofPacket(id, common.HexToHash(root), simFileRefsForPath(*path, files), sharedQ, domain, folded)
}

func (node *simPartitionNode) recover(msg simPartitionRecoverMsg) error {
	if msg.packet == nil {
		return fmt.Errorf("nil proof packet")
	}
	if msg.packet.Root != common.HexToHash(msg.root) {
		return fmt.Errorf("proof root %s does not match requested root %s", msg.packet.Root, msg.root)
	}
	verified, err := mptproofmsg.VerifyFoldedFileIPAProofPacket(msg.packet)
	if err != nil {
		return err
	}
	if !verified {
		return fmt.Errorf("folded FileIPA proof did not verify")
	}
	result, _, _, err := msg.packet.ToFileIPAResult()
	if err != nil {
		return err
	}
	rows := simRecoveryRowsFromFileIPARows(result.Rows)
	recovered, _, err := linearrecovery.RecoverSingleNode(rows, len(msg.packet.Files), msg.targetIndex)
	if err != nil {
		return err
	}
	if msg.targetIndex < 0 || msg.targetIndex >= len(msg.packet.Files) {
		return fmt.Errorf("target index %d outside file refs", msg.targetIndex)
	}
	length := int(msg.packet.Files[msg.targetIndex].Size)
	recoveredBytes, err := fileipa.FrChunksToBytes([]fr.Element{recovered}, length)
	if err != nil {
		return err
	}
	if node.recovered[msg.root] == nil {
		node.recovered[msg.root] = make(map[string][]byte)
	}
	node.recovered[msg.root][msg.pathKey] = recoveredBytes
	return nil
}

func (node *simPartitionNode) path(root string, key []byte) *partitioning.StatePath {
	paths := node.ownedByRoot[root]
	path, ok := paths[string(key)]
	if !ok {
		return nil
	}
	return &path
}

func applySimPartitionUpdate(t *testing.T, nodes []*simPartitionNode, result *partitioning.MPTPartitionResult) {
	t.Helper()
	if len(result.Partitions) != len(nodes) {
		t.Fatalf("partitions = %d, nodes = %d", len(result.Partitions), len(nodes))
	}
	for _, part := range result.Partitions {
		if part.ID < 0 || part.ID >= len(nodes) {
			t.Fatalf("partition id %d outside node set", part.ID)
		}
		ack := make(chan error, 1)
		nodes[part.ID].inbox <- simPartitionUpdateMsg{
			root:      result.RootHash,
			partition: part,
			rawSet:    result.RawNodeSets[part.ID],
			ack:       ack,
		}
		if err := waitSimAck(t, ack); err != nil {
			t.Fatalf("partition-%d apply root %s: %v", part.ID, result.RootHash, err)
		}
	}
}

func selectRecoveryTarget(t *testing.T, result *partitioning.MPTPartitionResult, senderID int, receiverID int) (partitioning.StatePath, int) {
	t.Helper()
	if senderID >= len(result.Partitions) || receiverID >= len(result.RawNodeSets) {
		t.Fatalf("invalid sender=%d receiver=%d", senderID, receiverID)
	}
	for _, path := range result.Partitions[senderID].Paths {
		if partitionHasPath(result.Partitions[receiverID], string(path.Key)) {
			continue
		}
		if targetIndex, ok := simFirstNodeMissingFromRawSet(path.Nodes, result.RawNodeSets[receiverID]); ok {
			return path, targetIndex
		}
	}
	t.Fatalf("no recovery target from partition-%d missing in partition-%d", senderID, receiverID)
	return partitioning.StatePath{}, 0
}

func stopSimPartitionNodes(t *testing.T, nodes []*simPartitionNode, wg *sync.WaitGroup) {
	t.Helper()
	for _, node := range nodes {
		done := make(chan struct{})
		node.inbox <- simPartitionStopMsg{done: done}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout stopping partition-%d", node.id)
		}
	}
	wg.Wait()
}

func waitSimAck(t *testing.T, ack chan error) error {
	t.Helper()
	select {
	case err := <-ack:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for partition ack")
	}
	return nil
}

func waitSimProofResponse(t *testing.T, reply chan simPartitionProofResponse) simPartitionProofResponse {
	t.Helper()
	select {
	case response := <-reply:
		return response
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for proof response")
	}
	return simPartitionProofResponse{}
}

func pathsByKey(paths []partitioning.StatePath) map[string]partitioning.StatePath {
	out := make(map[string]partitioning.StatePath, len(paths))
	for _, path := range paths {
		out[string(path.Key)] = path
	}
	return out
}

func cloneStringBoolMap(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func simPathNodeHashPayloadsNoT(path partitioning.StatePath) [][]byte {
	files := make([][]byte, len(path.Nodes))
	for i := range path.Nodes {
		payload := make([]byte, 31)
		copy(payload, path.Nodes[i].Hash)
		files[i] = payload
	}
	return files
}
