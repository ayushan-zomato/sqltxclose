# sqltxclose
> static analyzer in golang for database/sql tx begins but no closure

* currently supports `database/sql`, `github.com/jinzhu/gorm` packages

## how to use
1. clone the `repo` and cd inside the repo root
2. execute the following commands
```sh
go build -o sqltxclose ./cmd/sqltxclose
./sqltxclose ./pkg/sqltxclose/testdata/src/transactions/ # or any package you want to run static checks on
```

## benchmarks
```s
3000 LcC

0.08s user 0.16s system 87% cpu 0.280 total
```
