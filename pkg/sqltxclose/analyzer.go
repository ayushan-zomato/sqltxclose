package sqltxclose

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/ssa"
)

var Analyzer = &analysis.Analyzer{
	Name:     "sqltxclose",
	Doc:      "checks that SQL transactions are committed or rolled back",
	Requires: []*analysis.Analyzer{buildssa.Analyzer},
	Run:      run,
}

type transaction struct {
	value      ssa.Value  // The transaction value (Extract index 0 from Begin, or the Begin call for gorm)
	alloc      ssa.Value  // Non-nil when tx is captured in a closure: the heap-promoted **T alloc
	errVal     ssa.Value  // The error value used for Begin-error branch detection
	guardAlloc ssa.Value  // Non-nil when Begin is conditional (e.g., if parentTx == nil { tx = db.Begin() })
	fieldBase  ssa.Value  // Non-nil when tx is stored to a struct field: the Alloc of the struct pointer
	fieldIndex int        // Field index within the struct (valid when fieldBase != nil)
	discards   bool       // True when the Begin result is fully discarded (no Extract) — unconditional leak
	pos        token.Pos
}

// delegatedTx records a transaction that is opened in a method and stored to
// a struct field, with no local close — closing is delegated to callers.
type delegatedTx struct {
	method     *ssa.Function // the method that opens the tx
	fieldIndex int           // struct field index where tx is stored
	typeName   string        // receiver type name (e.g. "txBuilder")
	pkgPath    string        // package of the close method (for type matching)
	closeType  string        // type name for close calls
}

func run(pass *analysis.Pass) (any, error) {
	ssaResult := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA)

	var delegated []delegatedTx

	// Phase 1: Find local transactions, flag or record as delegated.
	for _, fn := range ssaResult.SrcFuncs {
		if fn == nil || fn.Blocks == nil {
			continue
		}

		txs := findTransactions(fn)

		for _, tx := range txs {
			// If tx is stored to a struct field and not closed locally,
			// record it as delegated — the caller is responsible.
			if tx.fieldBase != nil && !hasAnyLocalClose(fn, tx, true) {
				if dt, ok := makeDelegatedTx(fn, tx); ok {
					delegated = append(delegated, dt)
					continue
				}
			}

			// Unconditional leak: Begin result was fully discarded (no Extract).
			if tx.discards {
				pass.Reportf(tx.pos,
					"transaction started here is not guaranteed to be committed or rolled back")
				continue
			}

			// If tx is returned from this function with no local close,
			// closing is delegated to the caller — skip the diagnostic.
			if txIsReturned(fn, tx) && !hasAnyLocalClose(fn, tx, true) {
				continue
			}

			// If tx flows into a Phi node, it is opened inside a loop.
			// All iterations except the last leak their transaction.
			if txValueFlowsIntoPhi(tx) {
				pass.Reportf(tx.pos,
					"transaction started here is not guaranteed to be committed or rolled back")
				continue
			}

			pos, closed := transactionClosedOnAllPaths(fn, tx)
			if !closed {
				pass.Reportf(
					pos,
					"transaction started here is not guaranteed to be committed or rolled back",
				)
			}
		}
	}

	// Phase 2: For each function, check calls to delegated-tx methods.
	if len(delegated) > 0 {
		for _, fn := range ssaResult.SrcFuncs {
			if fn == nil || fn.Blocks == nil {
				continue
			}
			checkDelegatedCallers(pass, fn, delegated)
		}
	}

	return nil, nil
}

// hasAnyLocalClose returns true if fn contains a close call on tx.
// When withDefers is true, deferred calls and closure defers are also checked.
func hasAnyLocalClose(fn *ssa.Function, tx transaction, withDefers bool) bool {
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			switch x := instr.(type) {
			case *ssa.Call:
				if isAnyCloseCall(x.Common(), tx) {
					return true
				}
			case *ssa.Defer:
				if !withDefers {
					continue
				}
				if isAnyCloseCall(&x.Call, tx) {
					return true
				}
				if deferredClosureClosesTx(&x.Call, tx, false, fn) {
					return true
				}
			}
		}
	}
	return false
}

// makeDelegatedTx creates a delegatedTx record from a transaction stored to
// a struct field in a method.
func makeDelegatedTx(fn *ssa.Function, tx transaction) (delegatedTx, bool) {
	sig := fn.Signature
	if sig == nil || sig.Recv() == nil {
		return delegatedTx{}, false
	}

	recvType := sig.Recv().Type()
	if ptr, ok := recvType.(*types.Pointer); ok {
		recvType = ptr.Elem()
	}
	named, ok := recvType.(*types.Named)
	if !ok {
		return delegatedTx{}, false
	}

	// Find which package/closeType this tx belongs to.
	pkgPath, closeType := txCloseInfo(tx)
	if pkgPath == "" {
		return delegatedTx{}, false
	}

	return delegatedTx{
		method:     fn,
		fieldIndex: tx.fieldIndex,
		typeName:   named.Obj().Name(),
		pkgPath:    pkgPath,
		closeType:  closeType,
	}, true
}

// txCloseInfo returns the package path and close type for the transaction's Begin call.
func txCloseInfo(tx transaction) (string, string) {
	call, ok := tx.value.(*ssa.Call)
	if !ok {
		// For database/sql, tx.value is an Extract — find the underlying Call.
		if ext, ok := tx.value.(*ssa.Extract); ok {
			call, ok = ext.Tuple.(*ssa.Call)
			if !ok {
				return "", ""
			}
		} else {
			return "", ""
		}
	}
	pkgPath := beginCallPackage(call)
	if pkgPath == "" {
		return "", ""
	}
	info, ok := txPackages[pkgPath]
	if !ok {
		return "", ""
	}
	return pkgPath, info.closeType
}

// checkDelegatedCallers scans fn for calls to delegated-tx methods and verifies
// that the struct field is properly closed after the call.
// Note: only SrcFuncs in the current package are scanned. Callers in other
// packages that call a delegated-tx method are not checked (cross-package
// multi-package analysis is out of scope).
func checkDelegatedCallers(pass *analysis.Pass, fn *ssa.Function, delegated []delegatedTx) {
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			call, ok := instr.(*ssa.Call)
			if !ok {
				continue
			}
			callee := call.Common().StaticCallee()
			if callee == nil {
				continue
			}

			for _, dt := range delegated {
				if callee != dt.method {
					continue
				}

				// Found a call to a delegated-tx method.
				// The receiver is Args[0]. Build a synthetic transaction
				// for the struct field and verify it's closed.
				if len(call.Common().Args) == 0 {
					continue
				}
				recv := call.Common().Args[0]
				recvBase := stableBase(fn, recv)
				if recvBase == nil {
					// If recv is already a direct alloc/parameter, use it.
					recvBase = recv
				}

				synTx := transaction{
					value:      nil, // nil — tx opens at first field load, not at the call
					fieldBase:  recvBase,
					fieldIndex: dt.fieldIndex,
					pos:        call.Pos(),
				}

				// Don't need errVal — the caller's error check pattern is
				// handled by the field-load open semantics: the tx is only
				// considered open when b.tx is first accessed.

				pos, closed := transactionClosedOnAllPaths(fn, synTx)
				if !closed {
					pass.Reportf(
						pos,
						"transaction opened via %s is not closed on all paths",
						callee.Name(),
					)
				}
			}
		}
	}
}

// txIsReturned reports whether the transaction value is returned from fn,
// indicating the caller is responsible for closing it.
func txIsReturned(_ *ssa.Function, tx transaction) bool {
	if tx.value == nil {
		return false
	}
	refs := tx.value.Referrers()
	if refs == nil {
		return false
	}
	for _, ref := range *refs {
		if _, ok := ref.(*ssa.Return); ok {
			return true
		}
	}
	return false
}
