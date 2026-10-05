package sqltxclose

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

var txPackages = map[string]struct {
	beginMethods []string
	beginType    string
	closeMethods []string
	closeType    string
}{
	"database/sql": {
		beginMethods: []string{"Begin", "BeginTx"},
		beginType:    "DB",
		closeMethods: []string{"Commit", "Rollback"},
		closeType:    "Tx",
	},
	"gorm.io/gorm": {
		beginMethods: []string{"Begin"},
		beginType:    "DB",
		closeMethods: []string{"Commit", "Rollback"},
		closeType:    "DB",
	},
	"github.com/jinzhu/gorm": {
		beginMethods: []string{"Begin"},
		beginType:    "DB",
		closeMethods: []string{"Commit", "Rollback"},
		closeType:    "DB",
	},
}

func findTransactions(fn *ssa.Function) []transaction {
	var transactions []transaction

	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			call, ok := instr.(*ssa.Call)
			if !ok {
				continue
			}

			pkgPath := beginCallPackage(call)
			if pkgPath == "" {
				continue
			}

			switch pkgPath {
			case "database/sql":
				tx := extractFromTuple(fn, call, 0)
				if tx == nil {
					continue
				}
				errVal := extractFromTuple(fn, call, 1)
				txAlloc := findAllocFor(fn, tx)
				guardAlloc := findBeginGuard(fn, call)
				transactions = append(transactions, transaction{
					value:      tx,
					alloc:      txAlloc,
					errVal:     errVal,
					guardAlloc: guardAlloc,
					pos:        call.Pos(),
				})

			case "gorm.io/gorm", "github.com/jinzhu/gorm":
				txAlloc := findAllocFor(fn, call)
				errVal := gormErrorValue(fn, call, txAlloc)
				guardAlloc := findBeginGuard(fn, call)
				transactions = append(transactions, transaction{
					value:      call,
					alloc:      txAlloc,
					errVal:     errVal,
					guardAlloc: guardAlloc,
					pos:        call.Pos(),
				})
			}
		}
	}

	return transactions
}

// extractFromTuple finds the Extract instruction at the given index from a tuple-returning call.
func extractFromTuple(fn *ssa.Function, call *ssa.Call, index int) ssa.Value {
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			extract, ok := instr.(*ssa.Extract)
			if !ok || extract.Tuple != call || extract.Index != index {
				continue
			}
			return extract
		}
	}
	return nil
}

// gormErrorValue finds the loaded value of tx.Error for a GORM Begin call.
// SSA pattern (direct): t1 = &t0.Error → t2 = *t1
// SSA pattern (alloc-promoted): t1 = new *DB (tx); *t1 = t0; t3 = *t1; t4 = &t3.Error → t5 = *t4
func gormErrorValue(fn *ssa.Function, beginCall *ssa.Call, txAlloc ssa.Value) ssa.Value {
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			fa, ok := instr.(*ssa.FieldAddr)
			if !ok {
				continue
			}
			if !sameValue(fa.X, beginCall) && !isAllocLoad(fa.X, txAlloc) {
				continue
			}

			named := fieldAddrTypeName(fa)
			if named == "" {
				continue
			}

			// Find the load (*fa) of this FieldAddr
			for _, b2 := range fn.Blocks {
				for _, instr2 := range b2.Instrs {
					unop, ok := instr2.(*ssa.UnOp)
					if !ok || !sameValue(unop.X, fa) {
						continue
					}
					return unop
				}
			}
		}
	}
	return nil
}

// findAllocFor returns the Alloc that directly receives the given value via a Store,
// or nil if the value is not heap-promoted (e.g. not captured in a closure).
func findAllocFor(fn *ssa.Function, val ssa.Value) ssa.Value {
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			store, ok := instr.(*ssa.Store)
			if !ok || store.Val != val {
				continue
			}
			if _, ok := store.Addr.(*ssa.Alloc); ok {
				return store.Addr
			}
		}
	}
	return nil
}

// findBeginGuard detects when a Begin call is conditional on some variable being nil.
// Pattern: the Begin call sits in a block whose sole predecessor branches on `*alloc == nil`.
// Returns the alloc that gates the Begin, or nil if Begin is unconditional.
func findBeginGuard(fn *ssa.Function, beginCall *ssa.Call) ssa.Value {
	var beginBlock *ssa.BasicBlock
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			if instr == beginCall {
				beginBlock = block
				break
			}
		}
		if beginBlock != nil {
			break
		}
	}
	if beginBlock == nil || len(beginBlock.Preds) != 1 {
		return nil
	}

	pred := beginBlock.Preds[0]
	if len(pred.Instrs) == 0 || len(pred.Succs) != 2 {
		return nil
	}

	ifInstr, ok := pred.Instrs[len(pred.Instrs)-1].(*ssa.If)
	if !ok {
		return nil
	}

	binOp, ok := ifInstr.Cond.(*ssa.BinOp)
	if !ok {
		return nil
	}

	isNilConst := func(v ssa.Value) bool {
		c, ok := v.(*ssa.Const)
		return ok && c.IsNil()
	}
	allocOf := func(v ssa.Value) ssa.Value {
		unop, ok := v.(*ssa.UnOp)
		if !ok || unop.Op != token.MUL {
			return nil
		}
		if _, ok := unop.X.(*ssa.Alloc); ok {
			return unop.X
		}
		return nil
	}

	var guardAlloc ssa.Value
	if a := allocOf(binOp.X); a != nil && isNilConst(binOp.Y) {
		guardAlloc = a
	} else if a := allocOf(binOp.Y); a != nil && isNilConst(binOp.X) {
		guardAlloc = a
	}
	if guardAlloc == nil {
		return nil
	}

	// Verify beginBlock is on the "nil" path (guard == nil ⇒ Begin happens).
	switch binOp.Op {
	case token.EQL: // guard == nil → succ[0] is the nil path
		if pred.Succs[0] == beginBlock {
			return guardAlloc
		}
	case token.NEQ: // guard != nil → succ[1] is the nil path
		if pred.Succs[1] == beginBlock {
			return guardAlloc
		}
	}

	return nil
}

// isAllocLoad reports whether v is a pointer-dereference load of alloc (*alloc).
func isAllocLoad(v ssa.Value, alloc ssa.Value) bool {
	if alloc == nil {
		return false
	}
	unop, ok := v.(*ssa.UnOp)
	return ok && unop.Op == token.MUL && unop.X == alloc
}

// isTxValue reports whether v represents the transaction in the outer function:
// either the raw Begin result or any load from the heap-promoted alloc.
func isTxValue(v ssa.Value, tx transaction) bool {
	if sameValue(v, tx.value) {
		return true
	}
	return isAllocLoad(v, tx.alloc)
}

// deferredClosureClosesTx returns true when a deferred closure is guaranteed to
// call Commit or Rollback on tx on every exit path.
func deferredClosureClosesTx(call *ssa.CallCommon, tx transaction) bool {
	if call == nil || tx.alloc == nil {
		return false
	}
	closure, ok := call.Value.(*ssa.MakeClosure)
	if !ok {
		return false
	}

	bindingIdx := -1
	for i, b := range closure.Bindings {
		if b == tx.alloc {
			bindingIdx = i
			break
		}
	}
	if bindingIdx < 0 {
		return false
	}

	anonFn, ok := closure.Fn.(*ssa.Function)
	if !ok || bindingIdx >= len(anonFn.FreeVars) {
		return false
	}

	freeVar := anonFn.FreeVars[bindingIdx]

	// Collect every *freeVar load in the closure — these are the tx values inside it.
	txLoads := make(map[ssa.Value]struct{})
	for _, block := range anonFn.Blocks {
		for _, instr := range block.Instrs {
			if unop, ok := instr.(*ssa.UnOp); ok && unop.Op == token.MUL && unop.X == freeVar {
				txLoads[unop] = struct{}{}
			}
		}
	}

	// Find the guard FreeVar in the closure (if Begin was conditional).
	var guardFreeVar *ssa.FreeVar
	if tx.guardAlloc != nil {
		for i, b := range closure.Bindings {
			if b == tx.guardAlloc && i < len(anonFn.FreeVars) {
				guardFreeVar = anonFn.FreeVars[i]
				break
			}
		}
	}

	return closureClosesTxOnAllPaths(anonFn, txLoads, guardFreeVar)
}

// closureClosesTxOnAllPaths performs a BFS over the closure body and returns
// true only when every exit path calls Commit or Rollback on a tx load.
// guardFreeVar, when non-nil, is the FreeVar for the conditional-Begin guard;
// on the "guard non-nil" path Begin never happened, so no close is needed.
func closureClosesTxOnAllPaths(fn *ssa.Function, txLoads map[ssa.Value]struct{}, guardFreeVar *ssa.FreeVar) bool {
	if len(fn.Blocks) == 0 {
		return false
	}

	// Collect guard loads: every *guardFreeVar in the closure.
	guardLoads := make(map[ssa.Value]struct{})
	if guardFreeVar != nil {
		for _, block := range fn.Blocks {
			for _, instr := range block.Instrs {
				if unop, ok := instr.(*ssa.UnOp); ok && unop.Op == token.MUL && unop.X == guardFreeVar {
					guardLoads[unop] = struct{}{}
				}
			}
		}
	}

	type item struct {
		block  *ssa.BasicBlock
		closed bool
	}
	type key struct {
		block  *ssa.BasicBlock
		closed bool
	}

	queue := []item{{block: fn.Blocks[0], closed: false}}
	visited := make(map[key]struct{})

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		k := key{curr.block, curr.closed}
		if _, ok := visited[k]; ok {
			continue
		}
		visited[k] = struct{}{}

		closed := curr.closed
		handledReturn := false

		for _, instr := range curr.block.Instrs {
			if !closed {
				if call, ok := instr.(*ssa.Call); ok {
					if isTxCloseCallOnLoads(call.Common(), txLoads) {
						closed = true
					}
				}
			}
			if _, ok := instr.(*ssa.Return); ok {
				handledReturn = true
				if !closed {
					return false
				}
			}
		}

		if !handledReturn && len(curr.block.Succs) == 0 {
			if !closed {
				return false
			}
			continue
		}

		// At a `if txLoad == nil` / `if txLoad != nil` branch, the nil path
		// means tx is guaranteed nil — nothing to close on that path.
		if nilBranch, ok := isTxNilBranch(curr.block, txLoads); ok {
			for i, succ := range curr.block.Succs {
				c := closed
				if i == nilBranch {
					c = true // tx is nil — nothing to close
				}
				queue = append(queue, item{block: succ, closed: c})
			}
		} else if nonNilBranch, ok := isGuardNonNilBranch(curr.block, guardLoads); ok {
			// At a `if guard == nil` / `if guard != nil` branch, the non-nil
			// path means Begin never happened — nothing to close.
			for i, succ := range curr.block.Succs {
				c := closed
				if i == nonNilBranch {
					c = true // guard is non-nil — Begin didn't happen
				}
				queue = append(queue, item{block: succ, closed: c})
			}
		} else {
			for _, succ := range curr.block.Succs {
				queue = append(queue, item{block: succ, closed: closed})
			}
		}
	}

	return true
}

// isGuardNonNilBranch detects `if *guard == nil` or `if *guard != nil` where
// *guard is a load of the Begin-guard FreeVar. Returns the successor index
// for the path where the guard is non-nil (meaning Begin did NOT happen).
func isGuardNonNilBranch(block *ssa.BasicBlock, guardLoads map[ssa.Value]struct{}) (int, bool) {
	if len(guardLoads) == 0 || len(block.Instrs) == 0 || len(block.Succs) != 2 {
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

	isGuardLoad := func(v ssa.Value) bool {
		_, ok := guardLoads[v]
		return ok
	}
	isNil := func(v ssa.Value) bool {
		c, ok := v.(*ssa.Const)
		return ok && c.IsNil()
	}

	if !(isGuardLoad(binOp.X) && isNil(binOp.Y)) && !(isGuardLoad(binOp.Y) && isNil(binOp.X)) {
		return 0, false
	}

	switch binOp.Op {
	case token.EQL: // guard == nil → succ[0]=nil (Begin happened), succ[1]=non-nil (no Begin)
		return 1, true
	case token.NEQ: // guard != nil → succ[0]=non-nil (no Begin), succ[1]=nil (Begin happened)
		return 0, true
	default:
		return 0, false
	}
}

// isTxNilBranch detects `if txLoad == nil` or `if txLoad != nil` at the end
// of a closure block, where txLoad is a load of the captured tx variable.
// Returns (nilBranchIdx, true) where nilBranchIdx is the successor index
// for the path where tx is guaranteed nil (and thus needs no closing).
func isTxNilBranch(block *ssa.BasicBlock, txLoads map[ssa.Value]struct{}) (int, bool) {
	if len(block.Instrs) == 0 || len(block.Succs) != 2 {
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

	isTxLoad := func(v ssa.Value) bool {
		_, ok := txLoads[v]
		return ok
	}
	isNil := func(v ssa.Value) bool {
		c, ok := v.(*ssa.Const)
		return ok && c.IsNil()
	}

	if !(isTxLoad(binOp.X) && isNil(binOp.Y)) && !(isTxLoad(binOp.Y) && isNil(binOp.X)) {
		return 0, false
	}

	switch binOp.Op {
	case token.NEQ: // tx != nil → succ[0]=non-nil, succ[1]=nil
		return 1, true
	case token.EQL: // tx == nil → succ[0]=nil, succ[1]=non-nil
		return 0, true
	default:
		return 0, false
	}
}

// isTxCloseCallOnLoads checks whether common calls Commit or Rollback on any value in txLoads.
func isTxCloseCallOnLoads(common *ssa.CallCommon, txLoads map[ssa.Value]struct{}) bool {
	if common == nil {
		return false
	}
	fn := common.StaticCallee()
	if fn == nil || len(common.Args) == 0 {
		return false
	}
	if fn.Name() != "Commit" && fn.Name() != "Rollback" {
		return false
	}
	if _, ok := txLoads[common.Args[0]]; !ok {
		return false
	}
	for pkgPath, info := range txPackages {
		if isMethodOnPackageType(fn, pkgPath, info.closeType) {
			return true
		}
	}
	return false
}

func fieldAddrTypeName(fa *ssa.FieldAddr) string {
	ptrType, ok := fa.X.Type().(*types.Pointer)
	if !ok {
		return ""
	}
	st, ok := ptrType.Elem().Underlying().(*types.Struct)
	if !ok {
		return ""
	}
	if fa.Field >= st.NumFields() {
		return ""
	}
	return st.Field(fa.Field).Name()
}

// beginCallPackage returns the package path if the call is a Begin/BeginTx on a recognised type.
func beginCallPackage(call *ssa.Call) string {
	common := call.Common()
	if common == nil {
		return ""
	}

	fn := common.StaticCallee()
	if fn == nil {
		return ""
	}

	for pkgPath, info := range txPackages {
		if !isMethodOnPackageType(fn, pkgPath, info.beginType) {
			continue
		}
		for _, m := range info.beginMethods {
			if fn.Name() == m {
				return pkgPath
			}
		}
	}

	return ""
}

func isCommitCall(common *ssa.CallCommon, tx transaction) bool {
	return isCloseCall(common, tx, "Commit")
}

func isRollbackCall(common *ssa.CallCommon, tx transaction) bool {
	return isCloseCall(common, tx, "Rollback")
}

func isCloseCall(common *ssa.CallCommon, tx transaction, method string) bool {
	if common == nil {
		return false
	}

	fn := common.StaticCallee()
	if fn == nil || fn.Name() != method || len(common.Args) == 0 {
		return false
	}

	if !isTxValue(common.Args[0], tx) {
		return false
	}

	for pkgPath, info := range txPackages {
		if !isMethodOnPackageType(fn, pkgPath, info.closeType) {
			continue
		}
		for _, m := range info.closeMethods {
			if fn.Name() == m {
				return true
			}
		}
	}

	return false
}

// isMethodOnPackageType checks if fn is a method on *pkgPath.typeName.
func isMethodOnPackageType(fn *ssa.Function, pkgPath, typeName string) bool {
	if fn == nil {
		return false
	}

	sig := fn.Signature
	if sig == nil {
		return false
	}

	recv := sig.Recv()
	if recv == nil {
		// Could be a package-level function (e.g. (*sql.DB).Begin is a method).
		// Check via package.
		return fn.Pkg != nil && fn.Pkg.Pkg != nil && fn.Pkg.Pkg.Path() == pkgPath
	}

	recvType := recv.Type()
	if ptr, ok := recvType.(*types.Pointer); ok {
		recvType = ptr.Elem()
	}

	named, ok := recvType.(*types.Named)
	if !ok {
		return false
	}

	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == pkgPath && obj.Name() == typeName
}

func sameValue(a, b ssa.Value) bool {
	return a != nil && b != nil && a == b
}
