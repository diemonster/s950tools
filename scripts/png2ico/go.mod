// Tool-only module: keeps golang.org/x/image out of the main go.mod
// since png→ico conversion is build tooling, not runtime code.
module github.com/bivers/s950/scripts/png2ico

go 1.24.2

require golang.org/x/image v0.20.0
