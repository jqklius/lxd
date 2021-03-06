// +build linux,cgo,!agent

package db

import (
	"fmt"
)

// GetAppliedPatches returns the names of all patches currently applied on this node.
func (n *Node) GetAppliedPatches() ([]string, error) {
	inargs := []interface{}{}
	outfmt := []interface{}{""}

	query := fmt.Sprintf("SELECT name FROM patches")
	result, err := queryScan(n.db, query, inargs, outfmt)
	if err != nil {
		return []string{}, err
	}

	response := []string{}
	for _, r := range result {
		response = append(response, r[0].(string))
	}

	return response, nil
}

// MarkPatchAsApplied marks the patch with the given name as applied on this node.
func (n *Node) MarkPatchAsApplied(patch string) error {
	stmt := `INSERT INTO patches (name, applied_at) VALUES (?, strftime("%s"));`
	_, err := n.db.Exec(stmt, patch)
	return err
}
