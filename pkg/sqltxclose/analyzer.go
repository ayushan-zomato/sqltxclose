package sqltxclose

import (
	"go/token"

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
	value ssa.Value  // The transaction value (Extract index 0 from Begin)
	errVal ssa.Value // The error value (Extract index 1 from Begin)
	pos   token.Pos
}

func run(pass *analysis.Pass) (any, error) {
	ssaResult := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA)

	for _, fn := range ssaResult.SrcFuncs {
		if fn == nil || fn.Blocks == nil {
			continue
		}

		txs := findTransactions(fn)

		for _, tx := range txs {
			pos, closed := transactionClosedOnAllPaths(fn, tx)
			if !closed {
				pass.Reportf(
					pos,
					"transaction started here is not guaranteed to be committed or rolled back",
				)
			}
		}
	}

	return nil, nil
}