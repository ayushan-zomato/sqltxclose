package gorm

type DB struct {
	Error error
}

func (db *DB) Begin() *DB {
	return &DB{}
}

func (db *DB) Commit() *DB {
	return &DB{}
}

func (db *DB) Rollback() *DB {
	return &DB{}
}
