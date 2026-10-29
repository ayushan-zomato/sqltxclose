package sqltxclose

import (
	"go/constant"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

type txState uint8

const (
	txNotStarted txState = iota
	txOpen
	txClosed
	txLeakedByReassign // tx was open when a new Begin overwrote the variable
)

type analysisState struct {
	block *ssa.BasicBlock
	state txState
}

type stateKey struct {
	block *ssa.BasicBlock
	state txState
}

func transactionClosedOnAllPaths(
	fn *ssa.Function,
	tx transaction,
	initialState txState,
	visitedFuncs map[*ssa.Function]struct{},
) (token.Pos, bool) {
	if len(fn.Blocks) == 0 {
		return token.NoPos, true
	}

	if visitedFuncs == nil {
		visitedFuncs = make(map[*ssa.Function]struct{})
	}
	visitedFuncs[fn] = struct{}{}

	directClose := hasAnyLocalClose(fn, tx, false)

	queue := []analysisState{{block: fn.Blocks[0], state: initialState}}
	visited := make(map[stateKey]struct{})

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		key := stateKey{block: current.block, state: current.state}
		if _, ok := visited[key]; ok {
			continue
		}
		visited[key] = struct{}{}

		state := current.state
		handledReturn := false

		for _, instr := range current.block.Instrs {
			state = transfer(instr, tx, state, directClose, fn, visitedFuncs)

			// Reassignment detected: tx was open when a new Begin overwrote it.
			// Report at the original Begin position, not the reassignment site.
			if state == txLeakedByReassign {
				return tx.pos, false
			}

			if ret, ok := instr.(*ssa.Return); ok {
				handledReturn = true
				if state == txOpen {
					pos := ret.Pos()
					if !pos.IsValid() {
						pos = tx.pos
					}
					return pos, false
				}
			}
		}

		if !handledReturn && len(current.block.Succs) == 0 {
			if state == txOpen {
				return tx.pos, false
			}
			continue
		}

		// At an `if err != nil` branching on the Begin error, the true-branch
		// means Begin failed — reset to txNotStarted on that path.
		if errBranch, ok := isBeginErrorBranch(current.block, tx.errVal, fn); ok {
			for i, succ := range current.block.Succs {
				s := state
				if i == errBranch {
					s = txNotStarted
				}
				queue = append(queue, analysisState{block: succ, state: s})
			}
		} else if nilBranch, ok := isTxValueNilBranch(current.block, tx); ok {
			// At `if tx == nil` / `if tx != nil`, the nil path means tx is
			// guaranteed nil — nothing to close, so reset to txNotStarted.
			for i, succ := range current.block.Succs {
				s := state
				if i == nilBranch {
					s = txNotStarted
				}
				queue = append(queue, analysisState{block: succ, state: s})
			}
		} else if closedBranch, ok := isBoolClosedBranch(current.block, fn, tx); ok {
			// At `if boolVar`, where every store of `true` to boolVar
			// occurs in a block that also closes the tx, the true-branch
			// means the tx is already closed.
			for i, succ := range current.block.Succs {
				s := state
				if i == closedBranch {
					s = txClosed
				}
				queue = append(queue, analysisState{block: succ, state: s})
			}
		} else {
			for _, succ := range current.block.Succs {
				queue = append(queue, analysisState{block: succ, state: state})
			}
		}
	}

	return token.NoPos, true
}

func transfer(instr ssa.Instruction, tx transaction, state txState, hasDirectClose bool, fn *ssa.Function, visitedFuncs map[*ssa.Function]struct{}) txState {
	switch x := instr.(type) {
	case *ssa.Call:
		if isAnyCloseCall(x.Common(), tx) {
			return txClosed
		}
		// tx passed to a helper that closes it.
		if calleeClosesTxParam(x.Common(), tx, visitedFuncs) {
			return txClosed
		}
		// Terminator call (t.Fatal, os.Exit, log.Fatal, …) — the process is
		// ending; the DB server will roll back any uncommitted transaction.
		if isTerminatorCall(x.Common()) {
			return txClosed
		}
	case *ssa.Defer:
		if isAnyCloseCall(&x.Call, tx) {
			return txClosed
		}
		if deferredClosureClosesTx(&x.Call, tx, hasDirectClose, fn) {
			return txClosed
		}
	}

	// When the tx is open and we encounter a new Begin call (not this tx's own
	// Begin), the variable is being reassigned — the current tx leaks here.
	if state == txOpen {
		if call, ok := instr.(*ssa.Call); ok {
			if beginCallPackage(call) != "" && !sameValue(call, tx.value) {
				return txLeakedByReassign
			}
		}
	}

	// Transition when the instruction defines (produces) the tx value —
	// catches the Extract that yields *sql.Tx from Begin, or the Begin call for gorm.
	// For delegated transactions (tx.value == nil, tx.fieldBase != nil),
	// transition on the first field load of the struct field.
	if state == txNotStarted {
		if tx.value != nil {
			if v, ok := instr.(ssa.Value); ok && sameValue(v, tx.value) {
				return txOpen
			}
		} else if tx.fieldBase != nil {
			if v, ok := instr.(ssa.Value); ok && isFieldLoad(v, tx) {
				return txOpen
			}
		}
	}

	// If tx is returned from this function, closing responsibility is
	// delegated to the caller — treat this path as closed.
	if state == txOpen {
		if ret, ok := instr.(*ssa.Return); ok {
			for _, v := range ret.Results {
				if isTxValue(v, tx) {
					return txClosed
				}
			}
		}
	}

	return state
}

// isTxValueNilBranch detects `if tx == nil` / `if tx != nil` on the tracked
// transaction value. Returns the successor index for the nil path.
func isTxValueNilBranch(block *ssa.BasicBlock, tx transaction) (int, bool) {
	nilIdx, _, ok := nilCompBranchIndex(block, func(v ssa.Value) bool {
		return isTxValue(v, tx)
	})
	return nilIdx, ok
}

// isBeginErrorBranch detects `if err != nil` where err is the Begin error.
// Returns the successor index for the error path.
// Handles both direct comparisons (if tx.errVal != nil) and store-load
// indirections (err = tx.Error; if err != nil) where the error is stored
// to a named-return alloc and then reloaded.
func isBeginErrorBranch(block *ssa.BasicBlock, errVal ssa.Value, fn *ssa.Function) (int, bool) {
	if errVal == nil || len(block.Instrs) == 0 || len(block.Succs) != 2 {
		return 0, false
	}

	ifInstr, ok := block.Instrs[len(block.Instrs)-1].(*ssa.If)
	if !ok {
		return 0, false
	}

	binOp, ok := ifInstr.Cond.(*ssa.BinOp)
	if !ok {
		return 0, false
	}

	if !isErrValue(binOp.X, errVal, fn) && !isErrValue(binOp.Y, errVal, fn) {
		return 0, false
	}

	switch binOp.Op {
	case token.NEQ:
		return 0, true // err != nil → successor 0 is the error path
	case token.EQL:
		return 1, true // err == nil → successor 1 is the error path
	default:
		return 0, false
	}
}

// isBoolClosedBranch detects `if boolVar` where boolVar is a boolean variable
// whose `true` value is only ever set in blocks that also close the tx.
// Returns (closedBranchIdx, true) where closedBranchIdx is the successor index
// for the path where the tx is guaranteed closed.
//
// This handles the pattern:
//
//	errorInInner := false
//	for ... {
//	    if err != nil { tx.Rollback(); errorInInner = true; break }
//	}
//	if errorInInner { continue }  // ← tx is already closed on this path
//	tx.Commit()
func isBoolClosedBranch(block *ssa.BasicBlock, fn *ssa.Function, tx transaction) (int, bool) {
	if len(block.Instrs) == 0 || len(block.Succs) != 2 {
		return 0, false
	}

	ifInstr, ok := block.Instrs[len(block.Instrs)-1].(*ssa.If)
	if !ok {
		return 0, false
	}

	// Try alloc-based boolean first.
	if boolAlloc, trueBranch := resolveBoolCondition(ifInstr.Cond); boolAlloc != nil {
		if !isBoolAlloc(boolAlloc) {
			return 0, false
		}
		trueStoreBlocks := findTrueStoreBlocks(fn, boolAlloc)
		if len(trueStoreBlocks) == 0 {
			return 0, false
		}
		for _, b := range trueStoreBlocks {
			if !blockClosesTx(b, tx) {
				return 0, false
			}
		}
		return trueBranch, true
	}

	// Try Phi-based boolean (SSA optimized form).
	if phi, trueBranch := resolvePhiBoolCondition(ifInstr.Cond); phi != nil {
		trueEdgeBlocks := findPhiTrueEdgeBlocks(phi)
		if len(trueEdgeBlocks) == 0 {
			return 0, false
		}
		for _, b := range trueEdgeBlocks {
			if !blockOrPredClosesTx(b, tx) {
				return 0, false
			}
		}
		return trueBranch, true
	}

	return 0, false
}

// resolveBoolCondition extracts the alloc and true-branch index from an If condition.
// Handles:
//   - *alloc (direct bool load) → trueBranch = 0 (succ[0] is true path)
//   - UnOp(NOT, *alloc) → trueBranch = 1 (succ[1] is the "was-true" path)
func resolveBoolCondition(cond ssa.Value) (alloc *ssa.Alloc, trueBranch int) {
	// Direct load: if *alloc
	if unop, ok := cond.(*ssa.UnOp); ok && unop.Op == token.MUL {
		if a, ok := unop.X.(*ssa.Alloc); ok {
			return a, 0
		}
	}

	// Negated: if !*alloc (SSA represents as UnOp NOT on *alloc)
	if unop, ok := cond.(*ssa.UnOp); ok && unop.Op == token.NOT {
		if inner, ok := unop.X.(*ssa.UnOp); ok && inner.Op == token.MUL {
			if a, ok := inner.X.(*ssa.Alloc); ok {
				return a, 1
			}
		}
	}

	return nil, 0
}

// resolvePhiBoolCondition checks if cond is a Phi node representing a boolean
// variable. Returns (phi, trueBranch) or (nil, 0) if not a Phi bool.
func resolvePhiBoolCondition(cond ssa.Value) (*ssa.Phi, int) {
	// Direct: if phi
	if phi, ok := cond.(*ssa.Phi); ok && isBasicBool(phi.Type()) {
		return phi, 0
	}

	// Negated: if !phi
	if unop, ok := cond.(*ssa.UnOp); ok && unop.Op == token.NOT {
		if phi, ok := unop.X.(*ssa.Phi); ok && isBasicBool(phi.Type()) {
			return phi, 1
		}
	}

	return nil, 0
}

func isBasicBool(t types.Type) bool {
	basic, ok := t.(*types.Basic)
	return ok && basic.Kind() == types.Bool
}

// isBoolAlloc checks if the alloc holds a boolean type.
func isBoolAlloc(alloc *ssa.Alloc) bool {
	t := alloc.Type()
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	basic, ok := t.(*types.Basic)
	return ok && basic.Kind() == types.Bool
}

// findTrueStoreBlocks finds all blocks that store `true` to the given alloc.
func findTrueStoreBlocks(fn *ssa.Function, alloc *ssa.Alloc) []*ssa.BasicBlock {
	var blocks []*ssa.BasicBlock
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			store, ok := instr.(*ssa.Store)
			if !ok || store.Addr != alloc {
				continue
			}
			if isConstTrue(store.Val) {
				blocks = append(blocks, block)
			}
		}
	}
	return blocks
}

// isConstTrue reports whether v is a boolean SSA constant with value true.
func isConstTrue(v ssa.Value) bool {
	c, ok := v.(*ssa.Const)
	return ok && c.Value != nil && c.Value.Kind() == constant.Bool && constant.BoolVal(c.Value)
}

// blockClosesTx returns true if the block contains a direct close call on tx.
func blockClosesTx(block *ssa.BasicBlock, tx transaction) bool {
	for _, instr := range block.Instrs {
		call, ok := instr.(*ssa.Call)
		if !ok {
			continue
		}
		if isAnyCloseCall(call.Common(), tx) {
			return true
		}
	}
	return false
}

// findPhiTrueEdgeBlocks returns the predecessor blocks of the Phi's block that
// contribute a `true` value to the Phi. These are the blocks where the boolean
// was effectively set to true.
func findPhiTrueEdgeBlocks(phi *ssa.Phi) []*ssa.BasicBlock {
	phiBlock := phi.Block()
	var blocks []*ssa.BasicBlock
	for i, edge := range phi.Edges {
		if isConstTrue(edge) {
			if i < len(phiBlock.Preds) {
				blocks = append(blocks, phiBlock.Preds[i])
			}
		}
	}
	return blocks
}

// blockOrPredClosesTx returns true if the block itself or any of its
// (transitive) predecessors in a straight-line chain closes the tx.
// This handles cases where the Rollback is a few blocks before the
// block that jumps to the Phi.
func blockOrPredClosesTx(block *ssa.BasicBlock, tx transaction) bool {
	visited := make(map[*ssa.BasicBlock]struct{})
	for b := block; b != nil; {
		if _, ok := visited[b]; ok {
			break
		}
		visited[b] = struct{}{}
		if blockClosesTx(b, tx) {
			return true
		}
		// Only follow single-predecessor chains (straight-line code).
		if len(b.Preds) == 1 {
			b = b.Preds[0]
		} else {
			break
		}
	}
	return false
}
// isErrValue reports whether v represents the Begin error: either directly
// (v == errVal) or as a load from an alloc that was stored with errVal.
// The latter handles `err = tx.Error; if err != nil` where the named-return
// variable is heap-promoted to an alloc.
func isErrValue(v, errVal ssa.Value, fn *ssa.Function) bool {
	if sameValue(v, errVal) {
		return true
	}

	// Check if v is *alloc where alloc was stored with errVal.
	unop, ok := v.(*ssa.UnOp)
	if !ok || unop.Op != token.MUL {
		return false
	}
	alloc, ok := unop.X.(*ssa.Alloc)
	if !ok {
		return false
	}

	// Walk all stores to this alloc — if any stores errVal, match.
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			store, ok := instr.(*ssa.Store)
			if !ok {
				continue
			}
			if store.Addr == alloc && sameValue(store.Val, errVal) {
				return true
			}
		}
	}

	return false
}

// terminatorFuncs maps package path → set of function/method names that never
// return normally. After such a call the process (or goroutine) terminates;
// the DB server will roll back any uncommitted transaction on connection close.
var terminatorFuncs = map[string]map[string]struct{}{
	"testing": toSet("Fatal", "Fatalf", "FailNow"),
	"os":      toSet("Exit"),
	"log":     toSet("Fatal", "Fatalln", "Fatalf", "Panic", "Panicln", "Panicf"),
	"runtime": toSet("Goexit"),
}

func toSet(names ...string) map[string]struct{} {
	s := make(map[string]struct{}, len(names))
	for _, n := range names {
		s[n] = struct{}{}
	}
	return s
}

// isTerminatorCall reports whether the call is to a function that never returns
// normally (listed in terminatorFuncs).
func isTerminatorCall(common *ssa.CallCommon) bool {
	if common == nil {
		return false
	}

	callee := common.StaticCallee()
	if callee == nil {
		if common.Method == nil {
			return false
		}
		name := common.Method.Name()
		recv := common.Method.Type().(*types.Signature).Recv()
		if recv == nil {
			return false
		}
		recvType := recv.Type()
		if ptr, ok := recvType.(*types.Pointer); ok {
			recvType = ptr.Elem()
		}
		named, ok := recvType.(*types.Named)
		if !ok {
			return false
		}
		pkg := named.Obj().Pkg()
		if pkg == nil {
			return false
		}
		if names, ok := terminatorFuncs[pkg.Path()]; ok {
			_, found := names[name]
			return found
		}
		return false
	}

	if callee.Package() == nil {
		return false
	}
	pkg := callee.Package().Pkg
	if pkg == nil {
		return false
	}
	if names, ok := terminatorFuncs[pkg.Path()]; ok {
		_, found := names[callee.Name()]
		return found
	}
	return false
}
