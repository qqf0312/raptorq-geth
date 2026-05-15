# AGENTS.md

Long-term coding rules and project-specific constraints for future Codex sessions.

## 1. Project Rules

- Use minimal, localized changes.
- Do not make large refactors unless explicitly requested.
- Keep public APIs stable unless explicitly requested.
- Do not introduce unnecessary dependencies.
- Reuse existing helpers when possible.
- Always run relevant Go tests after changes.

## 2. IPA Module Rules

- Basic IPA only implements single inner-product proof and verification.
- Do not change IPA math logic unless explicitly requested.
- Do not change IPA public APIs unless explicitly requested.
- IPA uses `github.com/consensys/gnark-crypto/ecc/bn254` and `github.com/consensys/gnark-crypto/ecc/bn254/fr`.
- Code uses additive notation.
- `Q = CommitB(b)` binds the witness vector `b`.
- `P = CommitA(a) + Q + c * U` can be reconstructed by the verifier.

## 3. Folding Module Rules

- Folding folds multiple IPA statements into one root statement.
- A statement has the form:

  ```go
  type Statement struct {
      A []fr.Element
      Q bn254.G1Affine
      C fr.Element
  }
  ```

- Current folding setting is shared-witness folding:

  ```text
  c_i = <a_i, b>
  Q = CommitB(b)
  x_i = (a_i, Q, c_i)
  ```

- Therefore all folded statements must share the same `Q`.

- The correct 2-to-1 folding rule is:

  ```text
  A* = A_left + chi * A_right
  C* = C_left + chi * C_right
  Q* = Q_left
  ```

- Folding must check:

  ```text
  Q_left == Q_right
  ```

- Do not fold Q as:

  ```text
  Q* = Q_left + chi * Q_right
  ```

  because that is incorrect for the current shared-witness setting.

## 4. Testing Rules

- Add or update unit tests in the same module as the change.
- Folding + IPA integration tests should verify:
  - Multiple rows share the same witness `b`.
  - All statements share the same `Q = CommitB(b)`.
  - Folding produces a root statement.
  - The root satisfies:

    ```text
    root.C = <root.A, b>
    root.Q = Q
    ```

  - The root statement can be verified by the basic IPA module.
  - Tampered rows or mismatched Q values should be rejected.

- Run tests with:

  ```bash
  go test ./...
  ```
