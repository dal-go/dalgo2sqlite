module github.com/dal-go/dalgo2sqlite

go 1.24.0

//replace github.com/dal-go/dalgo => ../dalgo

//replace github.com/dal-go/dalgo2sql => ../dalgo2sql

require (
	github.com/dal-go/dalgo v0.43.1
	github.com/dal-go/dalgo2sql v0.5.0
	github.com/mattn/go-sqlite3 v1.14.44
)

require (
	github.com/RoaringBitmap/roaring/v2 v2.18.0 // indirect
	github.com/bits-and-blooms/bitset v1.24.4 // indirect
	github.com/georgysavva/scany/v2 v2.1.4 // indirect
	github.com/mschoch/smat v0.2.0 // indirect
	github.com/strongo/random v0.0.1 // indirect
)
