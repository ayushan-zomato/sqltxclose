package gorm

type DB struct {
	Error error
}

func (db *DB) Begin() *DB {
	return &DB{}
}

// BeginTx starts a transaction with context and options (jinzhu/gorm v1 API).
func (db *DB) BeginTx(ctx interface{}, opts interface{}) *DB {
	return &DB{}
}

func (db *DB) Commit() *DB {
	return &DB{}
}

func (db *DB) Rollback() *DB {
	return &DB{}
}

func (db *DB) RollbackUnlessCommitted() *DB {
	return &DB{}
}
