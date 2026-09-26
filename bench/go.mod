module github.com/dcyber-lab/jqgo/bench

go 1.24.0

toolchain go1.24.7

require (
	github.com/dcyber-lab/jqgo v0.0.0
	github.com/itchyny/gojq v0.12.19
)

require github.com/itchyny/timefmt-go v0.1.8 // indirect

replace github.com/dcyber-lab/jqgo => ../
