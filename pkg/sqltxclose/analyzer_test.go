package sqltxclose

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestTransactions(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, Analyzer, "transactions")
}

func TestGormTransactions(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, Analyzer, "gormtx")
}

func TestJinzhuGormTransactions(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, Analyzer, "jinzhutx")
}

// TestGaps documents known analyzer limitations: false positives (patterns
// incorrectly flagged) and false negatives (leaks silently missed).
// Unskip individual cases and move them to an active package as each gap is fixed.
func TestGaps(t *testing.T) {
	t.Skip("known gaps: helper-commits (interprocedural), tx reassignment")
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, Analyzer, "gaps")
}