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