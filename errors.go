package dalgo2sqlite

import "fmt"

// newCollectionNotFoundError formats the standard "collection not
// found" error. The contract is content-based per the Feature spec
// (REQ:describe-collection): the message MUST contain the substring
// "not found" and the collection name.
func newCollectionNotFoundError(name string) error {
	return fmt.Errorf("dalgo2sqlite: collection %q not found", name)
}
