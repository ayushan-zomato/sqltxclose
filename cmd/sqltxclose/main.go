package main

import (
	"github.com/ayushanand18/sqltxclose/pkg/sqltxclose"

	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	singlechecker.Main(sqltxclose.Analyzer)
}