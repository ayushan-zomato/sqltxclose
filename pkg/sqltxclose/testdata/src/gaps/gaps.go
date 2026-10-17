// Package gaps collects known analyzer limitations: patterns incorrectly flagged
// (false positives) and leaks silently missed (false negatives).
// Move cases out to an active package as each gap is fixed.
package gaps

import "github.com/jinzhu/gorm"

// ── False positive ───────────────────────────────────────────────────────────

// goodHelperCommits passes tx to a helper that commits it.
// Flagged because the analyzer does not follow tx into the callee.
// Fix requires interprocedural analysis.
func commitHelper(tx *gorm.DB) error {
	return tx.Commit().Error
}

func goodHelperCommits(db *gorm.DB) error {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	return commitHelper(tx)
}

// ── False negatives ──────────────────────────────────────────────────────────

// badTxReassign: first Begin is leaked; defer only closes the second tx.
// Also causes a spurious diagnostic on the second Begin's error branch
// because the analyzer cannot distinguish the two transactions sharing the same alloc.
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

// badTxReassignLoop: only the last iteration's tx gets the defer.
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

func gapsDoSomething() error { return nil }