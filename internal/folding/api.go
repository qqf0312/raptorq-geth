package folding

import (
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/internal/ipa"
)

// FoldedIPAResult contains the complete shared-witness folding result and the
// basic IPA proof for the folded root statement.
type FoldedIPAResult struct {
	Params    *ipa.Params
	Leaves    []Statement
	Root      Statement
	FoldProof *FoldProof
	IPAProof  *ipa.Proof
	Q         bn254.G1Affine
}

// BuildFoldedIPA builds shared-witness leaf statements from vectors and b,
// folds them with domain, and proves the folded root with the basic IPA module.
// All rows share the same Q = CommitB(b), matching this package's folding mode.
//
// 中文说明：
// 输入 params 是 IPA 参数，vectors 是多行公开向量 A_i，b 是所有行共享的
// witness 向量，domain 用于生成 folding challenge。函数会先计算共享承诺
// Q = CommitB(b)，再为每一行生成叶子语句：
//
//	leaf_i = (A_i, Q, C_i), 其中 C_i = <A_i, b>
//
// 随后将所有 leaf_i 折叠成一个 Root statement，并只对这个 Root statement
// 生成一个基础 IPA proof。输出 FoldedIPAResult 包含原始叶子语句、折叠后的
// Root、folding proof、Root 的 IPA proof 以及共享 Q。
func BuildFoldedIPA(params *ipa.Params, vectors [][]fr.Element, b []fr.Element, domain string) (*FoldedIPAResult, error) {
	if len(vectors) == 0 {
		return nil, fmt.Errorf("empty vectors")
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("empty witness vector")
	}
	if len(vectors[0]) != len(b) {
		return nil, fmt.Errorf("vector length mismatch: %d != %d", len(vectors[0]), len(b))
	}

	Q, err := ipa.CommitB(params, b)
	if err != nil {
		return nil, err
	}
	leaves := make([]Statement, len(vectors))
	for i := range vectors {
		if len(vectors[i]) != len(b) {
			return nil, fmt.Errorf("vector %d length %d != %d", i, len(vectors[i]), len(b))
		}
		c, err := ipa.InnerProduct(vectors[i], b)
		if err != nil {
			return nil, err
		}
		leaves[i] = Statement{
			A: append([]fr.Element(nil), vectors[i]...),
			Q: Q,
			C: c,
		}
	}

	root, foldProof, err := FoldStatements(leaves, domain)
	if err != nil {
		return nil, err
	}
	ipaProof, qFromProve, cFromProve, err := ipa.Prove(params, root.A, b)
	if err != nil {
		return nil, err
	}
	if !qFromProve.Equal(&root.Q) {
		return nil, fmt.Errorf("IPA prover commitment does not match folded root Q")
	}
	if !cFromProve.Equal(&root.C) {
		return nil, fmt.Errorf("IPA prover inner product does not match folded root C")
	}

	clonedLeaves := make([]Statement, len(leaves))
	for i := range leaves {
		clonedLeaves[i] = cloneStatement(leaves[i])
	}

	return &FoldedIPAResult{
		Params:    params,
		Leaves:    clonedLeaves,
		Root:      cloneStatement(root),
		FoldProof: foldProof,
		IPAProof:  ipaProof,
		Q:         Q,
	}, nil
}

// VerifyFoldedIPAResult verifies the folding proof first, then verifies the
// basic IPA proof for the folded root statement.
//
// 中文说明：
// 验证分两步进行。第一步使用 Leaves、Root 和 FoldProof 重新执行 folding
// 验证，确认多行 leaf statement 的折叠过程确实得到该 Root。第二步检查
// Root.Q 是否等于结果中记录的共享 Q，然后调用基础 IPA verifier 验证：
//
//	Root.C = <Root.A, b>
//	Root.Q = CommitB(b)
//
// 注意 verifier 不需要看到 witness b；它只使用公开的 Root.A、Root.Q、
// Root.C 和 IPAProof 来验证 folded root statement。
func VerifyFoldedIPAResult(result *FoldedIPAResult, domain string) (bool, error) {
	if result == nil {
		return false, fmt.Errorf("nil folded IPA result")
	}
	ok, err := VerifyFold(result.Leaves, result.Root, result.FoldProof, domain)
	if err != nil || !ok {
		return ok, err
	}
	if !pointEqual(result.Root.Q, result.Q) {
		return false, nil
	}
	return ipa.Verify(result.Params, result.Root.A, result.Root.Q, result.Root.C, result.IPAProof)
}
