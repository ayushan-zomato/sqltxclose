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
