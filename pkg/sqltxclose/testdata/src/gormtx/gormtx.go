package gormtx

import "gorm.io/gorm"

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

func doSomething() error {
	return nil
}
