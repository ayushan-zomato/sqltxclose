package sqltxclose

import (
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
				transactions = append(transactions, transaction{
					value:  tx,
					errVal: errVal,
					pos:    call.Pos(),
				})

			case "gorm.io/gorm", "github.com/jinzhu/gorm":
				errVal := gormErrorValue(fn, call)
				transactions = append(transactions, transaction{
					value:  call,
					errVal: errVal,
					pos:    call.Pos(),
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
// SSA pattern: t1 = &t0.Error → t2 = *t1
func gormErrorValue(fn *ssa.Function, beginCall *ssa.Call) ssa.Value {
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			fa, ok := instr.(*ssa.FieldAddr)
			if !ok || !sameValue(fa.X, beginCall) {
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

func isCommitCall(common *ssa.CallCommon, tx ssa.Value) bool {
	return isCloseCall(common, tx, "Commit")
}

func isRollbackCall(common *ssa.CallCommon, tx ssa.Value) bool {
	return isCloseCall(common, tx, "Rollback")
}

func isCloseCall(common *ssa.CallCommon, tx ssa.Value, method string) bool {
	if common == nil {
		return false
	}

	fn := common.StaticCallee()
	if fn == nil || fn.Name() != method || len(common.Args) == 0 {
		return false
	}

	if !sameValue(common.Args[0], tx) {
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
