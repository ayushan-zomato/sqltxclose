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

// goodDeferClosureNamedReturnCommitAndRollback: defer func with named return err,
// rolls back on error, commits on success, with additional logic after commit.
func goodDeferClosureNamedReturnCommitAndRollback(db *gorm.DB) (err error) {
	tx := db.Begin()
	_ = "some intermediate work" // code between Begin and error check
	if err = tx.Error; err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback()
			return
		}
		if err = tx.Commit().Error; err != nil {
			return
		}
		// additional work after commit (e.g. dynamo write)
		_ = "post-commit work"
	}()

	return doSomething()
}

// goodDeferClosureNamedReturnRollbackOnly: defer func with named return,
// only rolls back on error — commit is done directly in the function body.
func goodDeferClosureNamedReturnRollbackOnly(db *gorm.DB) (err error) {
	tx := db.Begin()
	if err = tx.Error; err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	if err = doSomething(); err != nil {
		return err
	}

	return tx.Commit().Error
}

// goodDeferClosureMultipleReturnsInClosure: defer func with multiple return
// points inside the closure — some close, some don't (early returns after close).
func goodDeferClosureMultipleReturnsInClosure(db *gorm.DB) (err error) {
	tx := db.Begin()
	if err = tx.Error; err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback()
			return
		}
		if err = tx.Commit().Error; err != nil {
			// commit failed — already handled by commit call itself
			return
		}
		if err = doSomething(); err != nil {
			// post-commit work failed
			return
		}
	}()

	return doSomething()
}

// goodDeferRollbackUnlessCommitted: RollbackUnlessCommitted is a valid close — covers
// both commit and rollback in a single call.
func goodDeferRollbackUnlessCommitted(db *gorm.DB) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.RollbackUnlessCommitted()

	if err := doSomething(); err != nil {
		return err
	}
	return tx.Commit().Error
}

// goodDeferRollbackUnlessCommittedNoCommit: RollbackUnlessCommitted alone is
// sufficient — it rolls back if no commit happened.
func goodDeferRollbackUnlessCommittedNoCommit(db *gorm.DB) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.RollbackUnlessCommitted()

	return doSomething()
}

// goodLoopBooleanGuard: tx created inside a loop, all error paths
// rollback+continue/break, success path commits. The boolean variable
// `errorInInner` is only set to true in blocks that also call Rollback,
// so the `if errorInInner { continue }` branch is recognized as tx-closed.
func goodLoopBooleanGuard(db *gorm.DB, items []int) error {
	for _, item := range items {
		tx := db.Begin()
		if tx.Error != nil {
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

type controller struct {
	tx       *gorm.DB
	rollBack bool
}

// goodStructFieldBoolGuardWithExtraLogic: tx and rollback flag on struct fields,
// defer has additional non-tx logic (e.g. lock release) before the close check.
func (c *controller) goodStructFieldBoolGuardWithExtraLogic(db *gorm.DB) error {
	c.tx = db.Begin()
	c.rollBack = true
	if c.tx.Error != nil {
		return c.tx.Error
	}

	defer func() {
		// extra non-tx work in the defer (e.g. lock release)
		_ = "releasing lock"

		if c.rollBack {
			c.tx.Rollback()
		} else {
			c.tx.Commit()
		}
	}()

	if err := doSomething(); err != nil {
		return err
	}

	c.rollBack = false
	return nil
}

// goodStructFieldBoolGuardSimple: tx and rollback flag on struct fields,
// defer branches between Rollback and Commit based on the flag.
func (c *controller) goodStructFieldBoolGuardSimple(db *gorm.DB) error {
	c.tx = db.Begin()
	c.rollBack = true
	if c.tx.Error != nil {
		return c.tx.Error
	}

	defer func() {
		if c.rollBack {
			c.tx.Rollback()
		} else {
			c.tx.Commit()
		}
	}()

	if err := doSomething(); err != nil {
		return err
	}

	c.rollBack = false
	return nil
}

// ── Interprocedural: Begin in one method, close in caller ──

type txBuilder struct {
	tx  *gorm.DB
	err error
}

// beginTx starts a transaction and stores it on the struct field.
// This should NOT be flagged — closing is delegated to the caller.
func (b *txBuilder) beginTx(db *gorm.DB) *txBuilder {
	b.tx = db.Begin()
	if b.tx.Error != nil {
		b.err = b.tx.Error
	}
	return b
}

// goodCallerClosesField: caller begins tx via method, closes via defer + commit.
func goodCallerClosesField(db *gorm.DB) error {
	b := &txBuilder{}
	b.beginTx(db)
	if b.err != nil {
		return b.err
	}
	defer b.tx.Rollback()
	if err := doSomething(); err != nil {
		return err
	}
	return b.tx.Commit().Error
}

// goodCallerClosesViaChain: method chain — beginTx returns the builder,
// caller uses it to close.
func goodCallerClosesViaChain(db *gorm.DB) error {
	b := (&txBuilder{}).beginTx(db)
	if b.err != nil {
		return b.err
	}
	defer b.tx.Rollback()
	return b.tx.Commit().Error
}

// badCallerNeverCloses: caller starts tx via method but never closes it.
func badCallerNeverCloses(db *gorm.DB) error {
	b := &txBuilder{}
	b.beginTx(db)
	if b.err != nil {
		return b.err
	}
	_ = b.tx
	return nil // want "transaction opened via beginTx is not closed on all paths"
}
