package sqltxclose

import (
	"go/token"

	"golang.org/x/tools/go/ssa"
)

type txState uint8

const (
	txNotStarted txState = iota
	txOpen
	txClosed
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
) (token.Pos, bool) {
	if len(fn.Blocks) == 0 {
		return token.NoPos, true
	}

	queue := []analysisState{{block: fn.Blocks[0], state: txNotStarted}}
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
			state = transfer(instr, tx.value, state)

			if ret, ok := instr.(*ssa.Return); ok {
				handledReturn = true
				if state == txOpen {
					return ret.Pos(), false
				}
			}
		}

		if !handledReturn && len(current.block.Succs) == 0 {
			if state == txOpen {
				if len(current.block.Instrs) > 0 {
					return current.block.Instrs[len(current.block.Instrs)-1].Pos(), false
				}
				return token.NoPos, false
			}
			continue
		}

		// At an `if err != nil` branching on the Begin error, the true-branch
		// means Begin failed — reset to txNotStarted on that path.
		if errBranch, ok := isBeginErrorBranch(current.block, tx.errVal); ok {
			for i, succ := range current.block.Succs {
				s := state
				if i == errBranch {
					s = txNotStarted
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

func transfer(instr ssa.Instruction, tx ssa.Value, state txState) txState {
	switch x := instr.(type) {
	case *ssa.Call:
		if isCommitCall(x.Common(), tx) || isRollbackCall(x.Common(), tx) {
			return txClosed
		}
	case *ssa.Defer:
		if isCommitCall(&x.Call, tx) || isRollbackCall(&x.Call, tx) {
			return txClosed
		}
	}

	// Transition when the instruction defines (produces) the tx value —
	// catches the Extract that yields *sql.Tx from Begin.
	if state == txNotStarted {
		if v, ok := instr.(ssa.Value); ok && sameValue(v, tx) {
			return txOpen
		}
	}

	return state
}

// isBeginErrorBranch detects `if err != nil` where err is the Begin error.
// Returns the successor index for the error path.
func isBeginErrorBranch(block *ssa.BasicBlock, errVal ssa.Value) (int, bool) {
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

	if !sameValue(binOp.X, errVal) && !sameValue(binOp.Y, errVal) {
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
