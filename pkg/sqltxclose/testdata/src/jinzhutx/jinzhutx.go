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
