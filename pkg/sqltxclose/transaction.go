package sqltxclose

import (
	"go/constant"
	"go/token"
	"go/types"
	"strings"

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
		closeMethods: []string{"Commit", "Rollback"}, // RollbackUnlessCommitted is jinzhu-only
		closeType:    "DB",
	},
	"github.com/jinzhu/gorm": {
		beginMethods: []string{"Begin", "BeginTx"},
		beginType:    "DB",
		closeMethods: []string{"Commit", "Rollback", "RollbackUnlessCommitted"},
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
					// No Extract: tuple result fully discarded — unconditional leak.
					// Use errVal=nil sentinel so run() can report immediately at tx.pos
					// without running the BFS (which would report at the return site).
					transactions = append(transactions, transaction{
						value:    call,
						discards: true,
						pos:      call.Pos(),
					})
					continue
				}
				errVal := extractFromTuple(fn, call, 1)
				txAlloc := findAllocFor(fn, tx)
				guardAlloc := findBeginGuard(fn, call)
				// If BOTH the tx and err Extracts have no referrers, both were assigned
				// to _ (e.g. _, _ = db.Begin()). This is an unconditional leak.
				discards := txAlloc == nil &&
					(tx.Referrers() == nil || len(*tx.Referrers()) == 0) &&
					(errVal == nil || errVal.Referrers() == nil || len(*errVal.Referrers()) == 0)
				transactions = append(transactions, transaction{
					value:      tx,
					alloc:      txAlloc,
					errVal:     errVal,
					guardAlloc: guardAlloc,
					discards:   discards,
					pos:        call.Pos(),
				})

			case "gorm.io/gorm", "github.com/jinzhu/gorm":
				txAlloc := findAllocFor(fn, call)
				var fieldBase ssa.Value
				var fieldIdx int
				if txAlloc == nil {
					fieldBase, fieldIdx, _ = findFieldStorage(fn, call)
				}
				errVal := gormErrorValue(fn, call, txAlloc, fieldBase, fieldIdx)
				guardAlloc := findBeginGuard(fn, call)
				transactions = append(transactions, transaction{
					value:      call,
					alloc:      txAlloc,
					errVal:     errVal,
					guardAlloc: guardAlloc,
					fieldBase:  fieldBase,
					fieldIndex: fieldIdx,
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
// SSA pattern (field-stored): c.tx = t0; t1 = *(&(*c).tx); t2 = &t1.Error → t3 = *t2
func gormErrorValue(fn *ssa.Function, beginCall *ssa.Call, txAlloc ssa.Value, fieldBase ssa.Value, fieldIdx int) ssa.Value {
	isTxSource := func(v ssa.Value) bool {
		if sameValue(v, beginCall) {
			return true
		}
		if isAllocLoad(v, txAlloc) {
			return true
		}
		if fieldBase != nil {
			// Check if v is a field load matching the tx field
			if unop, ok := v.(*ssa.UnOp); ok && unop.Op == token.MUL {
				if fa, ok := unop.X.(*ssa.FieldAddr); ok && fa.Field == fieldIdx {
					if sameFieldBase(fa.X, fieldBase) {
						return true
					}
				}
			}
		}
		return false
	}

	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			fa, ok := instr.(*ssa.FieldAddr)
			if !ok {
				continue
			}
			if !isTxSource(fa.X) {
				continue
			}

			// Bounds-check: ensure fa.Field is valid for this struct type.
			ptrType, ok := fa.X.Type().(*types.Pointer)
			if !ok {
				continue
			}
			st, ok := ptrType.Elem().Underlying().(*types.Struct)
			if !ok || fa.Field >= st.NumFields() {
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

// findFieldStorage detects when val is stored to a struct field (e.g. c.tx = db.Begin()).
// Returns the stable base identity and the field index.
// SSA pattern: t0 = &base.field; *t0 = val
// For closure matching we normalize to the deepest stable identity:
//   - If fa.X is *Alloc (a load from alloc), return the Alloc
//   - If fa.X is an Alloc directly, return the Alloc
//   - If fa.X is a Parameter, look for an alloc that stores it (heap promotion),
//     and return that alloc if found; otherwise return the Parameter.
func findFieldStorage(fn *ssa.Function, val ssa.Value) (base ssa.Value, fieldIdx int, ok bool) {
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			store, storeOk := instr.(*ssa.Store)
			if !storeOk || store.Val != val {
				continue
			}
			fa, faOk := store.Addr.(*ssa.FieldAddr)
			if !faOk {
				continue
			}
			if stable := stableBase(fn, fa.X); stable != nil {
				return stable, fa.Field, true
			}
		}
	}
	return nil, 0, false
}

// stableBase returns the deepest stable SSA identity for a struct pointer.
// This is used for matching across the outer function and closures.
func stableBase(fn *ssa.Function, v ssa.Value) ssa.Value {
	switch v.(type) {
	case *ssa.Alloc:
		return v
	case *ssa.Parameter:
		// Check if the parameter is heap-promoted (stored to an Alloc).
		if alloc := findAllocFor(fn, v); alloc != nil {
			return alloc
		}
		return v
	}
	if unop, ok := v.(*ssa.UnOp); ok && unop.Op == token.MUL {
		if _, isAlloc := unop.X.(*ssa.Alloc); isAlloc {
			return unop.X // the Alloc itself
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
// either the raw Begin result, any load from the heap-promoted alloc, or a
// load from a struct field (c.tx).
func isTxValue(v ssa.Value, tx transaction) bool {
	if sameValue(v, tx.value) {
		return true
	}
	if isAllocLoad(v, tx.alloc) {
		return true
	}
	return isFieldLoad(v, tx)
}

// isFieldLoad reports whether v is a load of the tx field from the struct
// that holds the transaction. SSA pattern: *(&base.field).
// The base in the outer function may be the Parameter itself while
// tx.fieldBase is its heap-promoted Alloc, so we check both direct
// equality and whether base is stored to tx.fieldBase.
func isFieldLoad(v ssa.Value, tx transaction) bool {
	if tx.fieldBase == nil {
		return false
	}
	unop, ok := v.(*ssa.UnOp)
	if !ok || unop.Op != token.MUL {
		return false
	}
	fa, ok := unop.X.(*ssa.FieldAddr)
	if !ok || fa.Field != tx.fieldIndex {
		return false
	}
	return sameFieldBase(fa.X, tx.fieldBase)
}

// sameFieldBase reports whether v resolves to the same stable base identity.
// Handles: direct equality, v stored to base (Parameter→Alloc heap promotion),
// or v is *base (a load from the alloc).
func sameFieldBase(v, base ssa.Value) bool {
	if v == base {
		return true
	}
	// v is the Parameter, base is the Alloc that stores it.
	if _, isAlloc := base.(*ssa.Alloc); isAlloc {
		refs := v.Referrers()
		if refs != nil {
			for _, ref := range *refs {
				if store, ok := ref.(*ssa.Store); ok && store.Addr == base && store.Val == v {
					return true
				}
			}
		}
	}
	// v is *base (a load from the alloc — used after heap promotion or in closure).
	if unop, ok := v.(*ssa.UnOp); ok && unop.Op == token.MUL && unop.X == base {
		return true
	}
	return false
}

// deferredClosureClosesTx returns true when a deferred closure is guaranteed to
// close the transaction. Two-tier check:
//  1. Closure closes tx on ALL paths (nil/guard branches excused).
//  2. Closure conditionally closes via a rollback-guard bool, AND a direct close
//     exists in the outer function (boolean guard / commit-then-disarm pattern).
func deferredClosureClosesTx(call *ssa.CallCommon, tx transaction, hasDirectClose bool, fn *ssa.Function) bool {
	if call == nil {
		return false
	}
	closure, ok := call.Value.(*ssa.MakeClosure)
	if !ok {
		return false
	}

	anonFn, ok := closure.Fn.(*ssa.Function)
	if !ok {
		return false
	}

	txLoads := collectClosureTxLoads(closure, anonFn, tx)
	if len(txLoads) == 0 {
		return false
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

	// Tier 1: closure closes on ALL paths (nil/guard branches excused).
	if closureClosesTxOnAllPaths(anonFn, txLoads, guardFreeVar) {
		return true
	}

	// Tier 2a: rollback-guard bool pattern.
	if closureClosesTxOnAnyPath(anonFn, txLoads) {
		boolAlloc, disarmVal := closureDisarmAlloc(closure, anonFn, fn)
		if boolAlloc != nil {
			if isTier2DisarmSound(fn, boolAlloc, disarmVal, tx) {
				return true
			}
		}
		if isArmedBeforeDeferSound(call, closure, anonFn, fn, tx) {
			return true
		}
		// Tier 2b: named-return-error pattern.
		if hasDirectClose && closureConditionsOnNamedReturnError(closure, anonFn) {
			return true
		}
	}

	return false
}

// isArmedBeforeDeferSound accepts `rollback := false; ...; rollback = true;
// defer func() { if rollback { tx.Rollback() } }()` when every later disarm
// store is paired with a close of tx.
func isArmedBeforeDeferSound(deferCall *ssa.CallCommon, closure *ssa.MakeClosure, anonFn, fn *ssa.Function, tx transaction) bool {
	for _, block := range fn.Blocks {
		for i, instr := range block.Instrs {
			if d, ok := instr.(*ssa.Defer); !ok || &d.Call != deferCall {
				continue
			}
			for j := i - 1; j >= 0; j-- {
				store, ok := block.Instrs[j].(*ssa.Store)
				if !ok || !isBoolBinding(store.Addr, closure, anonFn) {
					continue
				}
				c, ok := store.Val.(*ssa.Const)
				if !ok || c.Value == nil || c.Value.Kind() != constant.Bool {
					continue
				}
				return disarmStoresClose(fn, store.Addr, !constant.BoolVal(c.Value), tx)
			}
		}
	}
	return false
}

func isBoolBinding(v ssa.Value, closure *ssa.MakeClosure, anonFn *ssa.Function) bool {
	for i, b := range closure.Bindings {
		if b == v && i < len(anonFn.FreeVars) {
			basic, ok := v.Type().(*types.Pointer).Elem().(*types.Basic)
			return ok && basic.Kind() == types.Bool
		}
	}
	return false
}

// disarmStoresClose requires every store of disarm to addr, except the
// initializer, to be paired with a close of tx.
func disarmStoresClose(fn *ssa.Function, addr ssa.Value, disarm bool, tx transaction) bool {
	for _, block := range fn.Blocks {
		skipInit := block == addr.(*ssa.Alloc).Block()
		for _, instr := range block.Instrs {
			store, ok := instr.(*ssa.Store)
			if !ok || store.Addr != addr {
				continue
			}
			if skipInit {
				skipInit = false
				continue
			}
			if c, ok := store.Val.(*ssa.Const); ok && c.Value != nil && c.Value.Kind() == constant.Bool &&
				constant.BoolVal(c.Value) == disarm && !blockOrPredClosesTx(block, tx) {
				return false
			}
		}
	}
	return true
}

// closureDisarmAlloc finds the bool alloc used as a rollback guard in the closure
// and returns (alloc, disarmValue) where disarmValue is the constant that disarms it.
// Polarity is inferred from the initialization: `rollback := true` → disarm=false,
// `committed := false` → disarm=true.
func closureDisarmAlloc(closure *ssa.MakeClosure, anonFn *ssa.Function, outerFn *ssa.Function) (*ssa.Alloc, bool) {
	for i, b := range closure.Bindings {
		if i >= len(anonFn.FreeVars) {
			break
		}
		alloc, ok := b.(*ssa.Alloc)
		if !ok {
			continue
		}
		// Must be a bool alloc (not *error, not *gorm.DB, etc.)
		ptr, ok := alloc.Type().(*types.Pointer)
		if !ok {
			continue
		}
		basic, ok := ptr.Elem().(*types.Basic)
		if !ok || basic.Kind() != types.Bool {
			continue
		}
		// Infer polarity from the initialization constant store (same block as Alloc).
		// `rollback := true` → init=true → disarm=false.
		// `committed := false` → init=false → disarm=true.
		// For param-init (`rollback := isOwner`), fall back to checking explicit disarm stores.
		if initVal, initOk := initConstBoolStore(outerFn, alloc); initOk {
			return alloc, !initVal
		}
		// Param-initialized guard: look for an explicit false store (rollback=false disarm).
		if hasFalseStore(outerFn, alloc) {
			return alloc, false
		}
	}
	return nil, false
}

// closureConditionsOnNamedReturnError reports whether the closure captures a
// named-return *error free variable and conditions a close call on it.
func closureConditionsOnNamedReturnError(closure *ssa.MakeClosure, anonFn *ssa.Function) bool {
	for i, b := range closure.Bindings {
		if i >= len(anonFn.FreeVars) {
			break
		}
		ptr, ok := anonFn.FreeVars[i].Type().(*types.Pointer)
		if !ok {
			continue
		}
		if types.Identical(ptr.Elem(), types.Universe.Lookup("error").Type()) {
			if _, isAlloc := b.(*ssa.Alloc); isAlloc {
				return true
			}
		}
	}
	return false
}

// initConstBoolStore returns the first boolean constant stored to alloc in fn
// (the initialization value, e.g. `rollback := true` or `committed := false`).
func initConstBoolStore(fn *ssa.Function, alloc *ssa.Alloc) (val bool, ok bool) {
	// The init store immediately follows the Alloc instruction in the same block.
	for _, block := range fn.Blocks {
		allocFound := false
		for _, instr := range block.Instrs {
			if instr == alloc {
				allocFound = true
				continue
			}
			if !allocFound {
				continue
			}
			store, isStore := instr.(*ssa.Store)
			if !isStore || store.Addr != alloc {
				continue
			}
			c, isConst := store.Val.(*ssa.Const)
			if !isConst || c.Value == nil || c.Value.Kind() != constant.Bool {
				continue
			}
			return constant.BoolVal(c.Value), true
		}
	}
	return false, false
}

func hasTrueConstStore(fn *ssa.Function, alloc *ssa.Alloc) bool {
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			store, ok := instr.(*ssa.Store)
			if !ok || store.Addr != alloc {
				continue
			}
			if isConstTrue(store.Val) {
				return true
			}
		}
	}
	return false
}

// hasFalseStore reports whether fn contains any store of false to alloc.
func hasFalseStore(fn *ssa.Function, alloc *ssa.Alloc) bool {
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			store, ok := instr.(*ssa.Store)
			if !ok || store.Addr != alloc {
				continue
			}
			c, ok := store.Val.(*ssa.Const)
			if !ok {
				continue
			}
			if c.Value != nil && c.Value.Kind() == constant.Bool && !constant.BoolVal(c.Value) {
				return true
			}
		}
	}
	return false
}

// isTier2DisarmSound verifies that every disarm store to boolAlloc co-occurs with
// a close of tx (same block or straight-line predecessor).
func isTier2DisarmSound(fn *ssa.Function, boolAlloc *ssa.Alloc, disarmValue bool, tx transaction) bool {
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			store, ok := instr.(*ssa.Store)
			if !ok || store.Addr != boolAlloc {
				continue
			}
			c, ok := store.Val.(*ssa.Const)
			if !ok {
				continue
			}
			if c.Value == nil || c.Value.Kind() != constant.Bool {
				continue
			}
			if constant.BoolVal(c.Value) != disarmValue {
				continue
			}
			if !blockOrPredClosesTx(block, tx) {
				return false
			}
		}
	}
	return true
}

// collectClosureTxLoads finds all SSA values in the closure that represent loads
// of the transaction. Supports two patterns:
//   - tx stored to a local alloc (heap-promoted): closure captures the alloc,
//     loads are *freeVar.
//   - tx stored to a struct field (c.tx): closure captures the struct pointer's
//     alloc (fieldBase), loads are *(&(*freeVar).field).
func collectClosureTxLoads(closure *ssa.MakeClosure, anonFn *ssa.Function, tx transaction) map[ssa.Value]struct{} {
	txLoads := make(map[ssa.Value]struct{})

	// Case 1: local alloc (heap-promoted tx variable).
	if tx.alloc != nil {
		bindingIdx := -1
		for i, b := range closure.Bindings {
			if b == tx.alloc {
				bindingIdx = i
				break
			}
		}
		if bindingIdx >= 0 && bindingIdx < len(anonFn.FreeVars) {
			freeVar := anonFn.FreeVars[bindingIdx]
			for _, block := range anonFn.Blocks {
				for _, instr := range block.Instrs {
					if unop, ok := instr.(*ssa.UnOp); ok && unop.Op == token.MUL && unop.X == freeVar {
						txLoads[unop] = struct{}{}
					}
				}
			}
		}
	}

	// Case 2: struct field (c.tx = db.Begin()). The closure captures fieldBase
	// (the struct pointer — could be a Parameter's heap-promoted Alloc).
	// Inside the closure, tx is accessed via *(&(*freeVar).fieldIndex).
	if tx.fieldBase != nil {
		bindingIdx := -1
		for i, b := range closure.Bindings {
			if b == tx.fieldBase {
				bindingIdx = i
				break
			}
		}
		if bindingIdx >= 0 && bindingIdx < len(anonFn.FreeVars) {
			freeVar := anonFn.FreeVars[bindingIdx]
			for _, block := range anonFn.Blocks {
				for _, instr := range block.Instrs {
					// Match: unop = *fa where fa = &base.field, and base
					// is a load from freeVar (*freeVar).
					unop, ok := instr.(*ssa.UnOp)
					if !ok || unop.Op != token.MUL {
						continue
					}
					fa, ok := unop.X.(*ssa.FieldAddr)
					if !ok || fa.Field != tx.fieldIndex {
						continue
					}
					// fa.X should be *freeVar (a load of the struct pointer from the captured alloc)
					if load, ok := fa.X.(*ssa.UnOp); ok && load.Op == token.MUL && load.X == freeVar {
						txLoads[unop] = struct{}{}
					} else if fa.X == freeVar {
						// Direct use of freeVar (e.g. when freeVar is already a pointer)
						txLoads[unop] = struct{}{}
					}
				}
			}
		}
	}

	return txLoads
}

// closureClosesTxOnAnyPath returns true when at least one instruction in the
// closure body calls Commit or Rollback on a tx load.
func closureClosesTxOnAnyPath(fn *ssa.Function, txLoads map[ssa.Value]struct{}) bool {
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			if call, ok := instr.(*ssa.Call); ok {
				if isTxCloseCallOnLoads(call.Common(), txLoads) {
					return true
				}
			}
		}
	}
	return false
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

// nilCompBranchIndex inspects the terminating `if` of block and returns the
// successor index for the path where the matched value is nil, and the index
// for the path where it is non-nil.
// isMember must return true for the SSA value being compared against nil.
// Returns (nilIdx, nonNilIdx, true) on success, (0, 0, false) otherwise.
func nilCompBranchIndex(block *ssa.BasicBlock, isMember func(ssa.Value) bool) (nilIdx, nonNilIdx int, ok bool) {
	if len(block.Instrs) == 0 || len(block.Succs) != 2 {
		return 0, 0, false
	}
	ifInstr, isIf := block.Instrs[len(block.Instrs)-1].(*ssa.If)
	if !isIf {
		return 0, 0, false
	}
	binOp, isBinOp := ifInstr.Cond.(*ssa.BinOp)
	if !isBinOp {
		return 0, 0, false
	}
	isNilConst := func(v ssa.Value) bool {
		c, ok := v.(*ssa.Const)
		return ok && c.IsNil()
	}
	if !(isMember(binOp.X) && isNilConst(binOp.Y)) && !(isMember(binOp.Y) && isNilConst(binOp.X)) {
		return 0, 0, false
	}
	switch binOp.Op {
	case token.EQL: // v == nil → succ[0] is the nil path
		return 0, 1, true
	case token.NEQ: // v != nil → succ[0] is the non-nil path
		return 1, 0, true
	default:
		return 0, 0, false
	}
}

// isTxNilBranch returns the successor index for the path where the captured tx
// is nil (no close needed). Uses nilCompBranchIndex.
func isTxNilBranch(block *ssa.BasicBlock, txLoads map[ssa.Value]struct{}) (int, bool) {
	nilIdx, _, ok := nilCompBranchIndex(block, func(v ssa.Value) bool {
		_, is := txLoads[v]
		return is
	})
	return nilIdx, ok
}

// isGuardNonNilBranch returns the successor index for the path where the
// Begin-guard is non-nil (meaning Begin did NOT happen, so no close needed).
func isGuardNonNilBranch(block *ssa.BasicBlock, guardLoads map[ssa.Value]struct{}) (int, bool) {
	if len(guardLoads) == 0 {
		return 0, false
	}
	_, nonNilIdx, ok := nilCompBranchIndex(block, func(v ssa.Value) bool {
		_, is := guardLoads[v]
		return is
	})
	return nonNilIdx, ok
}

// isTxCloseCallOnLoads checks whether common calls any recognized close method on any value in txLoads.
func isTxCloseCallOnLoads(common *ssa.CallCommon, txLoads map[ssa.Value]struct{}) bool {
	if common == nil {
		return false
	}
	fn := common.StaticCallee()
	if fn == nil || len(common.Args) == 0 {
		return false
	}
	if _, ok := txLoads[common.Args[0]]; !ok {
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

// beginCallPackage returns the package path if the call is a Begin/BeginTx on a recognised type.
// Handles both static calls (StaticCallee != nil) and interface dispatch (IsInvoke).
func beginCallPackage(call *ssa.Call) string {
	common := call.Common()
	if common == nil {
		return ""
	}

	// Static call: Begin on a concrete type.
	if fn := common.StaticCallee(); fn != nil {
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

	// Interface dispatch: Begin called through an interface value.
	if common.IsInvoke() && common.Method != nil {
		return beginMethodPackageFromReturnType(common.Method)
	}

	return ""
}

// beginMethodPackageFromReturnType checks whether method is a Begin/BeginTx
// whose return type is a recognised transaction type. Returns the package path
// or "" if not recognised.
func beginMethodPackageFromReturnType(method *types.Func) string {
	sig, ok := method.Type().(*types.Signature)
	if !ok || sig.Results().Len() == 0 {
		return ""
	}
	result := sig.Results().At(0).Type()
	if ptr, ok := result.(*types.Pointer); ok {
		result = ptr.Elem()
	}
	named, ok := result.(*types.Named)
	if !ok {
		return ""
	}
	obj := named.Obj()
	if obj.Pkg() == nil {
		return ""
	}
	for pkgPath, info := range txPackages {
		for _, m := range info.beginMethods {
			if method.Name() == m && obj.Pkg().Path() == pkgPath && obj.Name() == info.closeType {
				return pkgPath
			}
		}
	}
	return ""
}

// isAnyCloseCall checks whether common calls any recognized close method
// (Commit, Rollback, RollbackUnlessCommitted, etc.) on the transaction.
// Handles both static calls and bound method-value calls (f := tx.Commit; f()).
func isAnyCloseCall(common *ssa.CallCommon, tx transaction) bool {
	if common == nil {
		return false
	}

	// Static method call: tx.Commit() / tx.Rollback()
	// Args[0] is the receiver for direct static calls.
	if fn := common.StaticCallee(); fn != nil {
		if isCloseMethod(fn) && len(common.Args) > 0 && isTxValue(common.Args[0], tx) {
			return true
		}
	}

	// Bound method-value call: f := tx.Commit; f()
	// SSA: MakeClosure{Fn: Commit$bound, Bindings: [receiver]}
	// The thunk has no receiver in its signature — check by name and binding type.
	if closure, ok := common.Value.(*ssa.MakeClosure); ok {
		if isBoundCloseThunk(closure, tx) {
			return true
		}
	}

	return false
}

// isCloseMethod reports whether fn is a recognized close method on any tracked type.
func isCloseMethod(fn *ssa.Function) bool {
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
	if sig == nil || sig.Recv() == nil {
		return false
	}

	recvType := sig.Recv().Type()
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

// isBoundCloseThunk detects a bound method-value call to a close method.
// SSA represents f := tx.Commit as MakeClosure{Fn: "Commit$bound", Bindings: [tx]}.
// The thunk has no receiver in its signature, so we match by name suffix and
// verify the binding (the captured receiver) is the tracked transaction.
func isBoundCloseThunk(closure *ssa.MakeClosure, tx transaction) bool {
	boundFn, ok := closure.Fn.(*ssa.Function)
	if !ok || len(closure.Bindings) == 0 {
		return false
	}
	methodName, ok := strings.CutSuffix(boundFn.Name(), "$bound")
	if !ok {
		return false
	}
	for _, info := range txPackages {
		for _, m := range info.closeMethods {
			if methodName == m && isTxValue(closure.Bindings[0], tx) {
				return true
			}
		}
	}
	return false
}

// txValueFlowsIntoPhi reports whether the transaction value flows into a loop Phi
// — a Phi node where one of the predecessors is a back-edge (loop iteration).
// A conditional-merge Phi (if-else) is NOT a loop and should not be flagged.
// We detect loop Phis by checking if any predecessor of the Phi's block has a
// higher index than the Phi's block itself (back-edge heuristic in SSA).
func txValueFlowsIntoPhi(tx transaction) bool {
	if tx.value == nil {
		return false
	}
	refs := tx.value.Referrers()
	if refs == nil {
		return false
	}
	for _, ref := range *refs {
		phi, ok := ref.(*ssa.Phi)
		if !ok {
			continue
		}
		// Check if this Phi is a loop Phi: any predecessor has index >= phi's block.
		phiBlock := phi.Block()
		for _, pred := range phiBlock.Preds {
			if pred.Index >= phiBlock.Index {
				return true // back-edge → loop Phi
			}
		}
	}
	return false
}

func calleeClosesTxParam(common *ssa.CallCommon, tx transaction, visitedFuncs map[*ssa.Function]struct{}) bool {
	if common == nil {
		return false
	}
	callee := common.StaticCallee()
	if callee == nil || callee.Blocks == nil {
		return false
	}

	// Prevent infinite recursion for recursive callees (e.g. retry(tx, n-1)).
	// If we've already entered this function in the current interprocedural
	// chain, conservatively assume it does not close the transaction.
	if visitedFuncs != nil {
		if _, seen := visitedFuncs[callee]; seen {
			return false
		}
	}

	paramIdx := -1
	for i, arg := range common.Args {
		if isTxValue(arg, tx) {
			paramIdx = i
			break
		}
	}
	if paramIdx < 0 || paramIdx >= len(callee.Params) {
		return false
	}

	param := callee.Params[paramIdx]
	paramAlloc := findAllocFor(callee, param)
	synTx := transaction{
		value: param,
		alloc: paramAlloc,
		pos:   common.Pos(),
	}

	if !hasAnyLocalClose(callee, synTx, true) {
		return false
	}

	_, closed := transactionClosedOnAllPaths(callee, synTx, txOpen, visitedFuncs)
	return closed
}