package mptproofmsg

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/internal/fileipa"
	"github.com/ethereum/go-ethereum/internal/ipa"
	"github.com/ethereum/go-ethereum/rlp"
)

func TestPacketRoundTrip(t *testing.T) {
	root := common.HexToHash("0x1234")
	tests := []Packet{
		&GetCommitmentPacket{ID: 1, Root: root, PathKey: []byte("account-a")},
		&CommitmentPacket{ID: 2, Root: root, PathKey: []byte("account-b"), Commitment: []byte("commitment")},
		&GetProofPacket{ID: 3, Root: root, PathKey: []byte("account-c")},
		&ProofPacket{ID: 4, Root: root, PathKey: []byte("account-d"), Proof: []byte("proof")},
		&GetFoldedFileIPAProofPacket{ID: 5, Root: root, Files: testFileRefs(), CoeffSets: ScalarMatrixToWire(testCoeffSets()), Domain: "domain"},
		&GetFountainOfferPacket{ID: 6, Root: root, PathKey: []byte("account-e")},
		&FountainOfferPacket{ID: 7, Found: true, Height: 10, Seed: []byte("seed"), SourceCount: 2, RowCount: 4, NodeCount: 3, CompactBytes: 400, AggregateHeight: 10, AggregateID: root},
		&GetFountainAggregatePacket{ID: 8, Height: 10, AggregateID: root},
		&FountainAggregatePacket{ID: 9, Found: true, Height: 10, AggregateID: root, Bundle: []byte("bundle")},
	}
	for _, want := range tests {
		code, payload, err := EncodePacket(want)
		if err != nil {
			t.Fatalf("EncodePacket %s: %v", want.Name(), err)
		}
		got, err := DecodePacket(code, payload)
		if err != nil {
			t.Fatalf("DecodePacket %s: %v", want.Name(), err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip %s mismatch:\n got: %#v\nwant: %#v", want.Name(), got, want)
		}
	}
}

func TestDecodeRejectsUnknownCode(t *testing.T) {
	if _, err := DecodePacket(0xff, nil); !errors.Is(err, ErrInvalidMessageCode) {
		t.Fatalf("DecodePacket error = %v, want ErrInvalidMessageCode", err)
	}
}

func TestDecodeRejectsMalformedPayload(t *testing.T) {
	if _, err := DecodePacket(GetProofMsg, []byte{0xff, 0x00}); err == nil {
		t.Fatal("expected malformed payload to fail")
	}
}

func TestHandlePacketDispatchesByCode(t *testing.T) {
	root := common.HexToHash("0x5678")
	handler := newRecordingHandler()
	tests := []struct {
		code uint64
		msg  any
		want string
	}{
		{GetCommitmentMsg, &GetCommitmentPacket{ID: 1, Root: root, PathKey: []byte("a")}, "get-commitment:1"},
		{CommitmentMsg, &CommitmentPacket{ID: 2, Root: root, PathKey: []byte("b"), Commitment: []byte("c")}, "commitment:2"},
		{GetProofMsg, &GetProofPacket{ID: 3, Root: root, PathKey: []byte("c")}, "get-proof:3"},
		{ProofMsg, &ProofPacket{ID: 4, Root: root, PathKey: []byte("d"), Proof: []byte("p")}, "proof:4"},
		{GetFoldedFileIPAProofMsg, &GetFoldedFileIPAProofPacket{ID: 5, Root: root, Files: testFileRefs(), CoeffSets: ScalarMatrixToWire(testCoeffSets()), Domain: "domain"}, "get-folded-file-ipa-proof:5"},
		{GetFountainOfferMsg, &GetFountainOfferPacket{ID: 6, Root: root, PathKey: []byte("e")}, "get-fountain-offer:6"},
		{FountainOfferMsg, &FountainOfferPacket{ID: 7}, "fountain-offer:7"},
		{GetFountainAggregateMsg, &GetFountainAggregatePacket{ID: 8}, "get-fountain-aggregate:8"},
		{FountainAggregateMsg, &FountainAggregatePacket{ID: 9}, "fountain-aggregate:9"},
	}
	for _, tt := range tests {
		payload, err := rlp.EncodeToBytes(tt.msg)
		if err != nil {
			t.Fatalf("encode %s: %v", tt.want, err)
		}
		if err := HandlePacket(handler, tt.code, payload); err != nil {
			t.Fatalf("HandlePacket %s: %v", tt.want, err)
		}
		if got := handler.last(); got != tt.want {
			t.Fatalf("last dispatch = %s, want %s", got, tt.want)
		}
	}
}

func TestFoldedFileIPAProofPacketRoundTripAndVerify(t *testing.T) {
	files := testFiles()
	coeffSets := testCoeffSets()
	domain := "mptproofmsg-folded-file-ipa-test"

	result, err := fileipa.BuildFoldedFileIPA(files, coeffSets, domain)
	if err != nil {
		t.Fatalf("BuildFoldedFileIPA: %v", err)
	}
	sharedQ := testSharedQForFiles(t, result.Params, files)
	packet, err := NewFoldedFileIPAProofPacket(99, common.HexToHash("0xabc"), testFileRefs(), sharedQ, domain, result)
	if err != nil {
		t.Fatalf("NewFoldedFileIPAProofPacket: %v", err)
	}

	code, payload, err := EncodePacket(packet)
	if err != nil {
		t.Fatalf("EncodePacket: %v", err)
	}
	if code != FoldedFileIPAProofMsg {
		t.Fatalf("code = %d, want %d", code, FoldedFileIPAProofMsg)
	}
	decoded, err := DecodePacket(code, payload)
	if err != nil {
		t.Fatalf("DecodePacket: %v", err)
	}
	got, ok := decoded.(*FoldedFileIPAProofPacket)
	if !ok {
		t.Fatalf("decoded type = %T", decoded)
	}
	if got.ID != packet.ID || got.Root != packet.Root || got.VectorLength != packet.VectorLength || got.Domain != packet.Domain {
		t.Fatalf("decoded metadata mismatch: got=%+v want=%+v", got, packet)
	}
	okVerify, err := VerifyFoldedFileIPAProofPacket(got)
	if err != nil {
		t.Fatalf("VerifyFoldedFileIPAProofPacket: %v", err)
	}
	if !okVerify {
		t.Fatal("expected folded file IPA proof packet to verify")
	}
}

func TestFoldedFileIPAProofSenderReceiver(t *testing.T) {
	const runs = 10
	for _, tt := range []struct {
		name     string
		fileSize int
	}{
		{name: "65-bytes", fileSize: 65},
		{name: "100-bytes", fileSize: 100},
		{name: "1000-bytes", fileSize: 1000},
		{name: "2000-bytes", fileSize: 2000},
		{name: "5000-bytes", fileSize: 5000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			testFoldedFileIPAProofSenderReceiverWithFileSize(t, tt.fileSize, runs)
		})
	}
}

func testFoldedFileIPAProofSenderReceiverWithFileSize(t *testing.T, fileSize, runs int) {
	t.Helper()

	var total foldedFileIPATimings
	for i := 0; i < runs; i++ {
		total.add(testFoldedFileIPAProofSenderReceiverOnce(t, fileSize))
	}
	avg := total.div(runs)

	t.Logf("runs: %d", runs)
	t.Logf("folded file IPA build avg: %s", avg.build)
	t.Logf("shared Q rebuild avg: %s", avg.sharedQ)
	t.Logf("proof packet build avg: %s", avg.packetBuild)
	t.Logf("send request avg: %s", avg.sendRequest)
	t.Logf("receive request avg: %s", avg.receiveRequest)
	t.Logf("send proof response avg: %s", avg.sendResponse)
	t.Logf("receive proof response avg: %s", avg.receiveResponse)
	t.Logf("verify proof packet avg: %s", avg.verify)
	t.Logf("restore target data avg: %s", avg.restore)
}

type foldedFileIPATimings struct {
	build           time.Duration
	sharedQ         time.Duration
	packetBuild     time.Duration
	sendRequest     time.Duration
	receiveRequest  time.Duration
	sendResponse    time.Duration
	receiveResponse time.Duration
	verify          time.Duration
	restore         time.Duration
}

func (t *foldedFileIPATimings) add(other foldedFileIPATimings) {
	t.build += other.build
	t.sharedQ += other.sharedQ
	t.packetBuild += other.packetBuild
	t.sendRequest += other.sendRequest
	t.receiveRequest += other.receiveRequest
	t.sendResponse += other.sendResponse
	t.receiveResponse += other.receiveResponse
	t.verify += other.verify
	t.restore += other.restore
}

func (t foldedFileIPATimings) div(n int) foldedFileIPATimings {
	return foldedFileIPATimings{
		build:           t.build / time.Duration(n),
		sharedQ:         t.sharedQ / time.Duration(n),
		packetBuild:     t.packetBuild / time.Duration(n),
		sendRequest:     t.sendRequest / time.Duration(n),
		receiveRequest:  t.receiveRequest / time.Duration(n),
		sendResponse:    t.sendResponse / time.Duration(n),
		receiveResponse: t.receiveResponse / time.Duration(n),
		verify:          t.verify / time.Duration(n),
		restore:         t.restore / time.Duration(n),
	}
}

func testFoldedFileIPAProofSenderReceiverOnce(t *testing.T, fileSize int) foldedFileIPATimings {
	t.Helper()

	aTransport, bTransport, closeTransport := NewMemoryTransportPair(2)
	defer closeTransport()

	var timings foldedFileIPATimings
	files := testFilesWithSize(fileSize)
	coeffSets := testCoeffSets()
	domain := "mptproofmsg-folded-file-ipa-transport-test"

	start := time.Now()
	result, err := fileipa.BuildFoldedFileIPA(files, coeffSets, domain)
	if err != nil {
		t.Fatalf("BuildFoldedFileIPA: %v", err)
	}
	timings.build = time.Since(start)

	start = time.Now()
	sharedQ := testSharedQForFiles(t, result.Params, files)
	timings.sharedQ = time.Since(start)

	start = time.Now()
	response, err := NewFoldedFileIPAProofPacket(100, common.HexToHash("0xdef"), testFileRefs(), sharedQ, domain, result)
	if err != nil {
		t.Fatalf("NewFoldedFileIPAProofPacket: %v", err)
	}
	timings.packetBuild = time.Since(start)

	request := GetFoldedFileIPAProofPacket{
		ID:        response.ID,
		Root:      response.Root,
		Files:     testFileRefs(),
		CoeffSets: ScalarMatrixToWire(coeffSets),
		Domain:    domain,
	}
	start = time.Now()
	if err := NewSender(aTransport).SendGetFoldedFileIPAProof(request); err != nil {
		t.Fatalf("SendGetFoldedFileIPAProof: %v", err)
	}
	timings.sendRequest = time.Since(start)

	start = time.Now()
	gotReq, err := NewReceiver(bTransport).Receive()
	if err != nil {
		t.Fatalf("Receive request: %v", err)
	}
	timings.receiveRequest = time.Since(start)
	if !reflect.DeepEqual(gotReq, &request) {
		t.Fatalf("request mismatch:\n got: %#v\nwant: %#v", gotReq, &request)
	}

	start = time.Now()
	if err := NewSender(bTransport).SendFoldedFileIPAProof(*response); err != nil {
		t.Fatalf("SendFoldedFileIPAProof: %v", err)
	}
	timings.sendResponse = time.Since(start)

	start = time.Now()
	gotRes, err := NewReceiver(aTransport).Receive()
	if err != nil {
		t.Fatalf("Receive response: %v", err)
	}
	timings.receiveResponse = time.Since(start)
	packet, ok := gotRes.(*FoldedFileIPAProofPacket)
	if !ok {
		t.Fatalf("response type = %T", gotRes)
	}

	start = time.Now()
	okVerify, err := VerifyFoldedFileIPAProofPacket(packet)
	if err != nil {
		t.Fatalf("VerifyFoldedFileIPAProofPacket: %v", err)
	}
	if !okVerify {
		t.Fatal("expected received folded file IPA proof packet to verify")
	}
	timings.verify = time.Since(start)

	start = time.Now()
	for i := range files {
		chunks, err := fileipa.BytesToFrChunks(files[i])
		if err != nil {
			t.Fatalf("restore file %d chunks: %v", i, err)
		}
		restored, err := fileipa.FrChunksToBytes(chunks, len(files[i]))
		if err != nil {
			t.Fatalf("restore file %d bytes: %v", i, err)
		}
		if !reflect.DeepEqual(restored, files[i]) {
			t.Fatalf("restored file %d mismatch", i)
		}
	}
	timings.restore = time.Since(start)

	return timings
}

func TestFoldedFileIPAProofPacketRejectsTamper(t *testing.T) {
	files := testFiles()
	coeffSets := testCoeffSets()
	domain := "mptproofmsg-folded-file-ipa-tamper-test"
	result, err := fileipa.BuildFoldedFileIPA(files, coeffSets, domain)
	if err != nil {
		t.Fatalf("BuildFoldedFileIPA: %v", err)
	}
	sharedQ := testSharedQForFiles(t, result.Params, files)
	packet, err := NewFoldedFileIPAProofPacket(101, common.HexToHash("0x123"), testFileRefs(), sharedQ, domain, result)
	if err != nil {
		t.Fatalf("NewFoldedFileIPAProofPacket: %v", err)
	}
	packet.Rows[0].C[31] ^= 0x01

	ok, err := VerifyFoldedFileIPAProofPacket(packet)
	if err != nil {
		t.Fatalf("VerifyFoldedFileIPAProofPacket: %v", err)
	}
	if ok {
		t.Fatal("expected tampered folded file IPA proof packet to be rejected")
	}
}

func TestSenderReceiverRequestResponse(t *testing.T) {
	aTransport, bTransport, closeTransport := NewMemoryTransportPair(4)
	defer closeTransport()

	aSender := NewSender(aTransport)
	aReceiver := NewReceiver(aTransport)
	bSender := NewSender(bTransport)
	bReceiver := NewReceiver(bTransport)

	root := common.HexToHash("0x99")
	request := GetCommitmentPacket{ID: 42, Root: root, PathKey: []byte("account-a")}
	if err := aSender.SendGetCommitment(request); err != nil {
		t.Fatalf("SendGetCommitment: %v", err)
	}
	gotRequest, err := bReceiver.Receive()
	if err != nil {
		t.Fatalf("Receive request: %v", err)
	}
	if !reflect.DeepEqual(gotRequest, &request) {
		t.Fatalf("request mismatch:\n got: %#v\nwant: %#v", gotRequest, &request)
	}
	requestID, ok := RequestID(gotRequest)
	if !ok || requestID != request.ID {
		t.Fatalf("request id = %d,%v want %d,true", requestID, ok, request.ID)
	}

	response := CommitmentPacket{
		ID:         request.ID,
		Root:       root,
		PathKey:    append([]byte(nil), request.PathKey...),
		Commitment: []byte("commitment-bytes"),
	}
	if err := bSender.SendCommitment(response); err != nil {
		t.Fatalf("SendCommitment: %v", err)
	}
	gotResponse, err := aReceiver.Receive()
	if err != nil {
		t.Fatalf("Receive response: %v", err)
	}
	if !reflect.DeepEqual(gotResponse, &response) {
		t.Fatalf("response mismatch:\n got: %#v\nwant: %#v", gotResponse, &response)
	}
	responseID, ok := RequestID(gotResponse)
	if !ok || responseID != request.ID {
		t.Fatalf("response id = %d,%v want %d,true", responseID, ok, request.ID)
	}
}

func TestSenderReceiverProof(t *testing.T) {
	aTransport, bTransport, closeTransport := NewMemoryTransportPair(2)
	defer closeTransport()

	root := common.HexToHash("0x77")
	req := GetProofPacket{ID: 7, Root: root, PathKey: []byte("account-z")}
	if err := NewSender(aTransport).SendGetProof(req); err != nil {
		t.Fatalf("SendGetProof: %v", err)
	}
	gotReq, err := NewReceiver(bTransport).Receive()
	if err != nil {
		t.Fatalf("Receive GetProof: %v", err)
	}
	if !reflect.DeepEqual(gotReq, &req) {
		t.Fatalf("GetProof mismatch:\n got: %#v\nwant: %#v", gotReq, &req)
	}

	res := ProofPacket{ID: req.ID, Root: root, PathKey: []byte("account-z"), Proof: []byte("proof-bytes")}
	if err := NewSender(bTransport).SendProof(res); err != nil {
		t.Fatalf("SendProof: %v", err)
	}
	gotRes, err := NewReceiver(aTransport).Receive()
	if err != nil {
		t.Fatalf("Receive Proof: %v", err)
	}
	if !reflect.DeepEqual(gotRes, &res) {
		t.Fatalf("Proof mismatch:\n got: %#v\nwant: %#v", gotRes, &res)
	}
}

func TestMemoryTransportClosed(t *testing.T) {
	aTransport, _, closeTransport := NewMemoryTransportPair(1)
	closeTransport()
	if err := NewSender(aTransport).SendGetCommitment(GetCommitmentPacket{ID: 1}); !errors.Is(err, ErrTransportClosed) {
		t.Fatalf("send after close error = %v, want ErrTransportClosed", err)
	}
	if _, err := NewReceiver(aTransport).Receive(); !errors.Is(err, ErrTransportClosed) {
		t.Fatalf("receive after close error = %v, want ErrTransportClosed", err)
	}
}

type recordingHandler struct {
	events []string
}

func newRecordingHandler() *recordingHandler {
	return &recordingHandler{}
}

func (h *recordingHandler) last() string {
	if len(h.events) == 0 {
		return ""
	}
	return h.events[len(h.events)-1]
}

func (h *recordingHandler) HandleGetCommitment(packet *GetCommitmentPacket) error {
	h.events = append(h.events, "get-commitment:"+itoa(packet.ID))
	return nil
}

func (h *recordingHandler) HandleCommitment(packet *CommitmentPacket) error {
	h.events = append(h.events, "commitment:"+itoa(packet.ID))
	return nil
}

func (h *recordingHandler) HandleGetProof(packet *GetProofPacket) error {
	h.events = append(h.events, "get-proof:"+itoa(packet.ID))
	return nil
}

func (h *recordingHandler) HandleProof(packet *ProofPacket) error {
	h.events = append(h.events, "proof:"+itoa(packet.ID))
	return nil
}

func (h *recordingHandler) HandleGetFoldedFileIPAProof(packet *GetFoldedFileIPAProofPacket) error {
	h.events = append(h.events, "get-folded-file-ipa-proof:"+itoa(packet.ID))
	return nil
}

func (h *recordingHandler) HandleFoldedFileIPAProof(packet *FoldedFileIPAProofPacket) error {
	h.events = append(h.events, "folded-file-ipa-proof:"+itoa(packet.ID))
	return nil
}

func (h *recordingHandler) HandleGetFountainOffer(packet *GetFountainOfferPacket) error {
	h.events = append(h.events, "get-fountain-offer:"+itoa(packet.ID))
	return nil
}

func (h *recordingHandler) HandleFountainOffer(packet *FountainOfferPacket) error {
	h.events = append(h.events, "fountain-offer:"+itoa(packet.ID))
	return nil
}

func (h *recordingHandler) HandleGetFountainAggregate(packet *GetFountainAggregatePacket) error {
	h.events = append(h.events, "get-fountain-aggregate:"+itoa(packet.ID))
	return nil
}

func (h *recordingHandler) HandleFountainAggregate(packet *FountainAggregatePacket) error {
	h.events = append(h.events, "fountain-aggregate:"+itoa(packet.ID))
	return nil
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func testFiles() [][]byte {
	return testFilesWithSize(65)
}

func testFilesWithSize(fileSize int) [][]byte {
	return [][]byte{
		testBytes(fileSize, 0),
		testBytes(fileSize, 17),
		testBytes(fileSize, 41),
	}
}

func testBytes(n, offset int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte((i*31 + offset + 1) % 251)
	}
	return out
}

func testCoeffSets() [][]fr.Element {
	return [][]fr.Element{
		{testScalar(2), testScalar(5), testScalar(7)},
		{testScalar(3), testScalar(4), testScalar(11)},
		{testScalar(6), testScalar(1), testScalar(9)},
		{testScalar(8), testScalar(10), testScalar(12)},
	}
}

func testScalar(v uint64) fr.Element {
	var out fr.Element
	out.SetUint64(v)
	return out
}

func testFileRefs() []FileRefWire {
	return []FileRefWire{
		{Key: []byte("file-0"), Hash: common.HexToHash("0x100"), Size: 65},
		{Key: []byte("file-1"), Hash: common.HexToHash("0x101"), Size: 65},
		{Key: []byte("file-2"), Hash: common.HexToHash("0x102"), Size: 65},
	}
}

func testSharedQForFiles(t *testing.T, params *ipa.Params, files [][]byte) bn254.G1Affine {
	t.Helper()
	var bFlat []fr.Element
	for i := range files {
		chunks, err := fileipa.BytesToFrChunks(files[i])
		if err != nil {
			t.Fatalf("BytesToFrChunks file %d: %v", i, err)
		}
		bFlat = append(bFlat, chunks...)
	}
	bProof := make([]fr.Element, nextTestPowerOfTwo(len(bFlat)))
	copy(bProof, bFlat)
	q, err := ipa.CommitB(params, bProof)
	if err != nil {
		t.Fatalf("CommitB: %v", err)
	}
	return q
}

func nextTestPowerOfTwo(n int) int {
	if n <= 1 {
		return 1
	}
	out := 1
	for out < n {
		out <<= 1
	}
	return out
}
