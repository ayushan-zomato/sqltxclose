package main

import (
	"fmt"
	"os"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
)

func main() {
	cfg := &packages.Config{
		Mode: packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
	}

	pkgs, err := packages.Load(cfg, "github.com/ayushanand18/sqltxclose/pkg/sqltxclose/testdata/src/transactions")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading packages: %v\n", err)
		os.Exit(1)
	}

	if len(pkgs) == 0 {
		fmt.Fprintf(os.Stderr, "No packages loaded\n")
		os.Exit(1)
	}

	pkg := pkgs[0]
	if len(pkg.Errors) > 0 {
		for _, e := range pkg.Errors {
			fmt.Fprintf(os.Stderr, "%v\n", e)
		}
		fmt.Fprintf(os.Stderr, "Package errors\n")
		os.Exit(1)
	}

	prog := ssa.NewProgram(pkg.Fset, ssa.PrintFunctions|ssa.SanityCheckFunctions)
	// Create database/sql package for imports
	for _, p := range pkgs {
		prog.CreatePackage(p.Types, p.Syntax, p.TypesInfo, false)
	}
	ssaPkg := prog.CreatePackage(pkg.Types, pkg.Syntax, pkg.TypesInfo, false)
	ssaPkg.Build()

	// Find and print goodCommit
	for _, member := range ssaPkg.Members {
		if fn, ok := member.(*ssa.Function); ok && fn.Name() == "goodCommit" {
			fmt.Println("=== goodCommit SSA ===")
			fn.WriteTo(os.Stdout)
			fmt.Println("\n=== goodCommit Call & Extract Analysis ===")

			for _, block := range fn.Blocks {
				fmt.Printf("Block %d:\n", block.Index)
				for i, instr := range block.Instrs {
					fmt.Printf("  [%d] %T: %v\n", i, instr, instr)
					if call, ok := instr.(*ssa.Call); ok {
						if common := call.Common(); common != nil {
							callee := common.StaticCallee()
							fmt.Printf("      StaticCallee: %v\n", callee)
							fmt.Printf("      Args count: %d\n", len(common.Args))
							for j, arg := range common.Args {
								fmt.Printf("        Args[%d]: %v (type: %T)\n", j, arg, arg)
							}
						}
					}
					if extract, ok := instr.(*ssa.Extract); ok {
						fmt.Printf("      Extract from: %v (index=%d)\n", extract.Tuple, extract.Index)
					}
				}
			}
		}
	}
}
