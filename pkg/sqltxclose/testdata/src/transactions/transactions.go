package transactions

import "database/sql"

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

// goodConditionalBeginWithParentTx: tx is only Begin'd when parentTx is nil.
// The deferred closure only closes when parentTx was nil (i.e. we own the tx).
// NOTE: For database/sql, the conditional-begin pattern with parentTx is tested
// in jinzhutx since GORM's single-return Begin is easier for the analyzer to
// detect. Here we test the simpler variant: always Begin, but defer guards
// with a boolean that indicates ownership.
func goodConditionalBeginWithParentTx(db *sql.DB, isOwner bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if isOwner {
			tx.Rollback()
		}
	}()

	if err := doSomething(); err != nil {
		return err
	}

	tx.Commit()
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
