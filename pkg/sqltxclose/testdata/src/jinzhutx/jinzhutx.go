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

// goodDeferClosure: named return; defer commits or rolls back based on err.
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

// badDeferClosureRecoverOnly: defer only runs on panic, not on normal return.
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

// badDeferClosurePartialNonNamed: defer only conditionally closes tx.
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

// goodDeferClosureNamedReturnCommitAndRollback: named return; defer rolls back
// on error, commits on success, then does post-commit work.
func goodDeferClosureNamedReturnCommitAndRollback(db *gorm.DB) (err error) {
	tx := db.Begin()
	_ = "some intermediate work"
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
		_ = "post-commit work"
	}()
	return doSomething()
}

// goodDeferClosureNamedReturnRollbackOnly: defer rolls back on error;
// commit is done directly in the function body.
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

// goodDeferClosureMultipleReturnsInClosure: multiple return points inside the
// closure, all paths after open have a close.
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
			return
		}
		if err = doSomething(); err != nil {
			return
		}
	}()
	return doSomething()
}

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

func goodDeferRollbackUnlessCommittedNoCommit(db *gorm.DB) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.RollbackUnlessCommitted()
	return doSomething()
}

// goodLoopBooleanGuard: tx created per iteration; all error paths rollback,
// success path commits. errorInInner guards the continue after a rollback.
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

func (c *controller) goodStructFieldBoolGuardWithExtraLogic(db *gorm.DB) error {
	c.tx = db.Begin()
	c.rollBack = true
	if c.tx.Error != nil {
		return c.tx.Error
	}
	defer func() {
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

// beginTx starts a transaction on the struct; closing is delegated to the caller.
func (b *txBuilder) beginTx(db *gorm.DB) *txBuilder {
	b.tx = db.Begin()
	if b.tx.Error != nil {
		b.err = b.tx.Error
	}
	return b
}

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

func goodCallerClosesViaChain(db *gorm.DB) error {
	b := (&txBuilder{}).beginTx(db)
	if b.err != nil {
		return b.err
	}
	defer b.tx.Rollback()
	return b.tx.Commit().Error
}

func badCallerNeverCloses(db *gorm.DB) error {
	b := &txBuilder{}
	b.beginTx(db)
	if b.err != nil {
		return b.err
	}
	_ = b.tx
	return nil // want "transaction opened via beginTx is not closed on all paths"
}

// ── BeginTx (false-negative, issue #3) ──
// BeginTx is not in jinzhu's beginMethods yet; these cases document the gap.

func badBeginTxNoClose(db *gorm.DB) error {
	tx := db.BeginTx(nil, nil)
	if tx.Error != nil {
		return tx.Error
	}
	_ = tx
	return nil // want "transaction started here is not guaranteed"
}

func badBeginTxBranch(db *gorm.DB, condition bool) error {
	tx := db.BeginTx(nil, nil)
	if tx.Error != nil {
		return tx.Error
	}
	if condition {
		return tx.Commit().Error
	}
	return nil // want "transaction started here is not guaranteed"
}

func goodBeginTxDefer(db *gorm.DB) error {
	tx := db.BeginTx(nil, nil)
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback()
	return doSomething()
}
// ── Regression: nil-Referrers panic (issue #1) ──
// A package-level var whose first field sits at the same index as svc.tx used
// to trigger a nil dereference in sameFieldBase when calling v.Referrers().

var gStats struct {
	tx   *gorm.DB
	hits int
}

type svc struct {
	tx *gorm.DB
}

// regressionNilReferrers must not crash. defer closes s.tx on every path.
func (s *svc) regressionNilReferrers(db *gorm.DB) error {
	s.tx = db.Begin()
	defer func() { s.tx.Rollback() }()
	gStats.tx.Commit()
	return nil
}

// ── Void functions: diagnostic must point at Begin, not implicit return ──

// badVoidWorker: no return value, tx never closed.
func badVoidWorker(db *gorm.DB) {
	tx := db.Begin() // want "transaction started here is not guaranteed"
	_ = tx
}

// badVoidHandler: Begin succeeds but no close on the happy path.
func badVoidHandler(db *gorm.DB) {
	tx := db.Begin() // want "transaction started here is not guaranteed"
	if tx.Error != nil {
		return
	}
	doWork(tx)
}

// goodVoidWorker: tx closed unconditionally via defer — no diagnostic.
func goodVoidWorker(db *gorm.DB) {
	tx := db.Begin()
	defer tx.Rollback()
	doWork(tx)
}

func doWork(_ *gorm.DB) {}

// ── Fixed false positives ──

// goodMethodValueClose closes tx via a bound method value.
func goodMethodValueClose(db *gorm.DB) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	commit := tx.Commit
	return commit().Error
}

// goodMethodValueDefer closes tx via a deferred bound method value.
func goodMethodValueDefer(db *gorm.DB) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	rollback := tx.Rollback
	defer rollback()
	return doSomething()
}

// goodReturnDelegated wraps Begin and returns tx; closing is the caller's job.
func goodReturnDelegated(db *gorm.DB) *gorm.DB {
	return db.Begin()
}

func goodCallerOfDelegated(db *gorm.DB) error {
	tx := goodReturnDelegated(db)
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback()
	return tx.Commit().Error
}

// goodDeferBeforeBegin: nil-guarded defer registered before Begin.
func goodDeferBeforeBegin(db *gorm.DB, skip bool) error {
	var tx *gorm.DB
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()
	if skip {
		return nil
	}
	tx = db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	return tx.Commit().Error
}

// ── Interface dispatch ──

type beginIface interface {
	Begin() *gorm.DB
}

// badIfaceBegin: Begin via interface — leak not reported until interface dispatch fix.
func badIfaceBegin(db beginIface) error {
	tx := db.Begin()
	_ = tx
	return nil // want "transaction started here is not guaranteed"
}

func badIfaceBeginBranch(db beginIface, ok bool) error {
	tx := db.Begin()
	if ok {
		return tx.Commit().Error
	}
	return nil // want "transaction started here is not guaranteed"
}

// badDeferClosureCondUnrelated: defer closes only when unrelated condition is true;
// leaks when !a && !unrelated.
func badDeferClosureCondUnrelated(db *gorm.DB, a bool, unrelated bool) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer func() {
		if unrelated {
			tx.Rollback()
		}
	}()
	if a {
		return tx.Commit().Error
	}
	return nil // want "transaction started here is not guaranteed"
}

// ── Tx reassignment ──

// badTxReassign: first Begin is leaked when tx is reassigned.
func badTxReassign(db *gorm.DB) error {
	tx := db.Begin() // want "transaction started here is not guaranteed"
	if tx.Error != nil {
		return tx.Error
	}
	tx = db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback()
	return tx.Commit().Error
}

// badTxReassignLoop: only the last iteration's tx is closed by the defer.
func badTxReassignLoop(db *gorm.DB, n int) error {
	var tx *gorm.DB
	for i := 0; i < n; i++ {
		tx = db.Begin() // want "transaction started here is not guaranteed"
		if tx.Error != nil {
			return tx.Error
		}
	}
	defer tx.Rollback()
	return tx.Commit().Error
}

// ── Helper-commits (interprocedural) ──

func commitViaHelper(tx *gorm.DB) error {
	return tx.Commit().Error
}

func rollbackViaHelper(tx *gorm.DB) error {
	return tx.Rollback().Error
}

// goodHelperCommits: tx passed to a helper that commits it.
func goodHelperCommits(db *gorm.DB) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	return commitViaHelper(tx)
}

// goodHelperRollback: tx passed to a helper that rolls back.
func goodHelperRollback(db *gorm.DB) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	return rollbackViaHelper(tx)
}

// badHelperNoClose: helper receives tx but never closes it.
func helperNoClose(tx *gorm.DB) error {
	_ = tx
	return nil
}

func badHelperNoClose(db *gorm.DB) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	return helperNoClose(tx) // want "transaction started here is not guaranteed"
}
