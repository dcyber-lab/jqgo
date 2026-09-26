# jqgo vs gojq

Same data (10,000 generated user records, see `records`), same queries,
results checked equal by `TestSameResults`.

```sh
cd bench
go test -run '^$' -bench . -count 6 | tee out.txt
benchstat -col /impl out.txt
```

`baseline.txt` is the raw output of the run the numbers in the main
README come from. Rerun on your own hardware before drawing conclusions.
