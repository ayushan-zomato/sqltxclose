package main

import (
	"github.com/ayushan-zomato/sqltxclose/pkg/sqltxclose"

	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	singlechecker.Main(sqltxclose.Analyzer)
}
