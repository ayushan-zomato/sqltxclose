package jinzhutx

import "github.com/jinzhu/gorm"

func goodCommit(db *gorm.DB) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}

	return tx.Commit().Error
}

func goodRollback(db *gorm.DB) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}

	return tx.Rollback().Error
}

func goodDefer(db *gorm.DB) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}

	defer tx.Rollback()

	return doSomething()
}

func bad(db *gorm.DB) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}

	_ = tx

	return nil // want "transaction started here is not guaranteed"
}

func badBranch(db *gorm.DB, condition bool) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}

	if condition {
		return tx.Commit().Error
	}

	return nil // want "transaction started here is not guaranteed"
}

func goodBranch(db *gorm.DB, condition bool) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}

	if condition {
		return tx.Commit().Error
	}

	return tx.Rollback().Error
}

// goodDeferClosure: defer func with named return, closes tx on all paths.
func goodDeferClosure(db *gorm.DB) (err error) {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()
	return doSomething()
}

// badDeferClosureRecoverOnly: defer only rolls back on panic, not on normal return.
func badDeferClosureRecoverOnly(db *gorm.DB) (err error) {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()
	return nil // want "transaction started here is not guaranteed"
}

// goodDeferClosureNonNamed: non-named return; defer always closes tx.
func goodDeferClosureNonNamed(db *gorm.DB) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer func() {
		tx.Rollback()
	}()
	return doSomething()
}

// badDeferClosurePartialNonNamed: defer only conditionally closes tx, non-named return.
func badDeferClosurePartialNonNamed(db *gorm.DB, cond bool) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer func() {
		if cond {
			tx.Rollback()
		}
	}()
	return nil // want "transaction started here is not guaranteed"
}

func doSomething() error {
	return nil
}

// goodDeferClosureNilGuardNeq: nil guard in deferred closure — tx != nil wraps close.
func goodDeferClosureNilGuardNeq(db *gorm.DB) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()
	return doSomething()
}

// goodDeferClosureNilGuardEq: nil guard via early return — tx == nil.
func goodDeferClosureNilGuardEq(db *gorm.DB) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer func() {
		if tx == nil {
			return
		}
		tx.Rollback()
	}()
	return doSomething()
}

// goodConditionalBegin: tx is only Begin'd when parentTx == nil.
func goodConditionalBegin(db *gorm.DB, parentTx *gorm.DB) (err error) {
	var tx *gorm.DB
	if parentTx == nil {
		tx = db.Begin()
	} else {
		tx = parentTx
	}

	defer func() {
		if err != nil {
			if parentTx == nil {
				tx.Rollback()
			}
		} else {
			if parentTx == nil {
				tx.Commit()
			}
		}
	}()

	return doSomething()
}

// badConditionalBeginNoClose: tx is Begin'd conditionally but closure never closes.
func badConditionalBeginNoClose(db *gorm.DB, parentTx *gorm.DB) (err error) {
	var tx *gorm.DB
	if parentTx == nil {
		tx = db.Begin()
	} else {
		tx = parentTx
	}

	defer func() {
		_ = tx
	}()

	return doSomething() // want "transaction started here is not guaranteed"
}
