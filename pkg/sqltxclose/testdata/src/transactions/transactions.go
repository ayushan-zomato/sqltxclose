package transactions

import (
	"database/sql"
	"log"
	"os"
	"testing"
)

func goodCommit(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	return tx.Commit()
}

func goodRollback(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	return tx.Rollback()
}

func goodDefer(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	defer tx.Rollback()

	return doSomething()
}

func bad(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	_ = tx

	return nil // want "transaction started here is not guaranteed"
}

func badBranch(db *sql.DB, condition bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	if condition {
		return tx.Commit()
	}

	return nil // want "transaction started here is not guaranteed"
}

func goodBranch(db *sql.DB, condition bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	if condition {
		return tx.Commit()
	}

	return tx.Rollback()
}

func badEarlyReturn(db *sql.DB, condition bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	if condition {
		return nil // want "transaction started here is not guaranteed"
	}

	return tx.Commit()
}

func goodEarlyReturn(db *sql.DB, condition bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	defer tx.Rollback()

	if condition {
		return nil
	}

	return tx.Commit()
}

func badLoop(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	for i := 0; i < 10; i++ {
		if err := doSomething(); err != nil {
			return err // want "transaction started here is not guaranteed"
		}
	}

	return tx.Commit()
}

func goodLoop(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	for i := 0; i < 10; i++ {
		if err := doSomething(); err != nil {
			tx.Rollback()
			return err
		}
	}

	return tx.Commit()
}

func doSomething() error {
	return nil
}

// goodNilGuardNeq: direct nil guard on tx — tx != nil wraps commit.
func goodNilGuardNeq(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if tx != nil {
		return tx.Commit()
	}
	return nil
}

// goodNilGuardEq: direct nil guard on tx — tx == nil early return.
func goodNilGuardEq(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if tx == nil {
		return nil
	}
	return tx.Commit()
}

// goodDeferNilGuardNeq: nil guard in deferred closure — tx != nil wraps rollback.
func goodDeferNilGuardNeq(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()
	return doSomething()
}

// goodDeferNilGuardEq: nil guard via early return in deferred closure — tx == nil.
func goodDeferNilGuardEq(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if tx == nil {
			return
		}
		tx.Rollback()
	}()
	return doSomething()
}

// goodBooleanGuardRollback: boolean guard pattern — defer conditionally rolls back,
// direct Commit on the success path disarms the guard.
func goodBooleanGuardRollback(db *sql.DB, condition bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	rollback := true
	defer func() {
		if rollback {
			tx.Rollback()
		}
	}()

	if condition {
		return err
	}

	if err := doSomething(); err != nil {
		return err
	}

	tx.Commit()
	rollback = false
	return nil
}

// goodBooleanGuardCommit: inverted boolean guard — defer conditionally commits,
// direct Rollback on error paths, direct Commit via defer on success.
func goodBooleanGuardCommit(db *sql.DB, condition bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	shouldCommit := false
	defer func() {
		if shouldCommit {
			tx.Commit()
		} else {
			tx.Rollback()
		}
	}()

	if condition {
		return err
	}

	if err := doSomething(); err != nil {
		return err
	}

	shouldCommit = true
	return nil
}

// goodConditionalBeginWithParentTx: ownership tracked via a local bool that is
// disarmed (set false) on the direct-commit path, so the defer unconditionally
// covers all remaining paths.
func goodConditionalBeginWithParentTx(db *sql.DB, isOwner bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	rollback := isOwner
	defer func() {
		if rollback {
			tx.Rollback()
		}
	}()

	if err := doSomething(); err != nil {
		return err
	}

	tx.Commit()
	rollback = false
	return nil
}

// goodLoopBooleanGuard: boolean variable correlates Rollback with continue.
func goodLoopBooleanGuard(db *sql.DB, items []int) error {
	for _, item := range items {
		tx, err := db.Begin()
		if err != nil {
			continue
		}

		if err := doSomething(); err != nil {
			tx.Rollback()
			continue
		}

		_ = item
		errorInInner := false
		for j := 0; j < 3; j++ {
			if err := doSomething(); err != nil {
				tx.Rollback()
				errorInInner = true
				break
			}
		}

		if errorInInner {
			continue
		}

		tx.Commit()
	}
	return nil
}

// badDiscardBegin: result of Begin is completely discarded — no assignment at all.
func badDiscardBegin(db *sql.DB) error {
	db.Begin() // want "transaction started here is not guaranteed"
	return nil
}

// badBlankBegin: tx assigned to blank identifiers — never closed.
func badBlankBegin(db *sql.DB) error {
	_, _ = db.Begin() // want "transaction started here is not guaranteed"
	return nil
}

// --- Recursive helper regression tests (must not cause stack overflow) ---

// badRecursiveHelperPartialClose: recursive helper that commits tx in the base
// case but delegates to itself on the error path. The recursive call cannot be
// statically proven to close, so the analyzer conservatively reports it.
// Critically, this must NOT stack-overflow — it must terminate and report.
func badRecursiveHelperPartialClose(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	return retryCommit(tx, 3) // want "transaction started here is not guaranteed"
}

func retryCommit(tx *sql.Tx, n int) error {
	if n == 0 {
		return tx.Commit()
	}
	if err := doSomething(); err != nil {
		return retryCommit(tx, n-1)
	}
	return tx.Commit()
}

// badRecursiveHelperNeverCloses: recursive helper that never closes the tx.
// Should still report a diagnostic (not stack-overflow).
func badRecursiveHelperNeverCloses(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	return recurseForever(tx, 3) // want "transaction started here is not guaranteed"
}

func recurseForever(tx *sql.Tx, n int) error {
	if n == 0 {
		return nil
	}
	return recurseForever(tx, n-1)
}

// --- Return-delegation tests (tx returned = closing delegated to caller) ---

// goodReturnTx: tx is returned on every path — caller is responsible for closing.
func goodReturnTx(db *sql.DB) (*sql.Tx, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	return tx, nil
}

// goodReturnTxWithWork: tx is returned after some setup work;
// caller is responsible for closing. No diagnostic expected.
func goodReturnTxWithWork(db *sql.DB) (*sql.Tx, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	if err := doSomething(); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

// goodCallerClosesReturnedTx: caller of a Begin-wrapper closes the tx.
// The linter should not flag either function.
func goodCallerClosesReturnedTx(db *sql.DB) error {
	tx, err := goodReturnTxWithWork(db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return tx.Commit()
}

// badReturnTxPartial: tx is returned on one path but leaked on the early-exit path.
func badReturnTxPartial(db *sql.DB, early bool) (*sql.Tx, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	if early {
		return nil, nil // want "transaction started here is not guaranteed"
	}
	return tx, nil
}

// --- Terminator function tests (t.Fatal, os.Exit, log.Fatal) ---

// goodFatalAfterBeginError: t.Fatal on Begin error — no leak.
func goodFatalAfterBeginError(t *testing.T, db *sql.DB) {
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	tx.Commit()
}

// goodFatalOnlyClose: t.Fatal is the only "close" — process terminates,
// DB server rolls back. No diagnostic expected.
func goodFatalOnlyClose(t *testing.T, db *sql.DB) {
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := doSomething(); err != nil {
		t.Fatalf("operation failed: %v", err)
	}
	_ = tx
	t.Fatal("always fatal")
}

// goodOsExit: os.Exit terminates the process — no leak.
func goodOsExit(db *sql.DB) {
	tx, err := db.Begin()
	if err != nil {
		os.Exit(1)
	}
	_ = tx
	os.Exit(0)
}

// goodLogFatal: log.Fatal terminates the process — no leak.
func goodLogFatal(db *sql.DB) {
	tx, err := db.Begin()
	if err != nil {
		log.Fatal(err)
	}
	_ = tx
	log.Fatal("done")
}
