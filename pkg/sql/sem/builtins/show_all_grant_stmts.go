// Copyright 2025 The Cockroach Authors.
//
// Use of this software is governed by the CockroachDB Software License
// included in the /LICENSE file.

package builtins

import (
	"context"
	"fmt"
	"unsafe"

	"github.com/cockroachdb/cockroach/pkg/kv"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/descpb"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/eval"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/sessiondata"
	"github.com/cockroachdb/cockroach/pkg/util/mon"
	"github.com/cockroachdb/cockroach/pkg/util/protoutil"
	"github.com/cockroachdb/errors"
)

func getDescriptorIds(
	ctx context.Context, evalPlanner eval.Planner, txn *kv.Txn, acc *mon.BoundAccount,
) (descriptorIds []int64, retErr error) {
	query := `SELECT id FROM system.descriptor`

	it, err := evalPlanner.QueryIteratorEx(
		ctx,
		"crdb_internal.show_all_grant_stmts",
		sessiondata.NoSessionDataOverride,
		query,
	)

	if err != nil {
		return nil, err
	}
	defer func() {
		retErr = errors.CombineErrors(retErr, it.Close())
	}()

	var ok bool
	for ok, err = it.Next(ctx); ok; ok, err = it.Next(ctx) {
		descriptorId := tree.MustBeDInt(it.Cur()[0])
		descriptorIds = append(descriptorIds, int64(descriptorId))
		if err = acc.Grow(ctx, int64(unsafe.Sizeof(descriptorId))); err != nil {
			return nil, err
		}
	}
	if err != nil {
		return descriptorIds, err
	}
	return descriptorIds, nil
}

func buildGrantStatements(objDesc descpb.Descriptor, privJSON tree.DJSON) (tree.Datums, error) {
	var grantStmts tree.Datums
	usersArray, err := privJSON.FetchValKey("users")
	if err != nil {
		return nil, errors.Wrapf(err, "failed to fetch users from JSON %s", privJSON)
	}
	// TODO: This is not done yet!
	return nil, nil
}

func getGrantCreateStatements(
	ctx context.Context, evalPlanner eval.Planner, txn *kv.Txn, descriptorId int64,
) (ownerStmt tree.Datum, grantStmts tree.Datums, retErr error) {
	query := `
SELECT
  crdb_internal.pb_to_json('cockroach.sql.sqlbase.Descriptor', descriptor)->'table'->'privileges',
  descriptor
FROM
  system.descriptor
WHERE
  id = $1`

	row, err := evalPlanner.QueryRowEx(
		ctx,
		"crdb_internal.show_all_grant_stmts",
		sessiondata.NoSessionDataOverride,
		query,
		tree.NewDInt(tree.DInt(descriptorId)),
	)
	if err != nil {
		return nil, nil, err
	}

	privilegesJSON := tree.MustBeDJSON(row[0])
	descriptorBytes := tree.MustBeDBytes(row[1])
	var desc descpb.Descriptor
	if err := protoutil.Unmarshal([]byte(descriptorBytes), &desc); err != nil {
		return nil, nil, errors.Wrap(err, "unable to unmarshal descriptor")
	}

	// Below, we make the ownership statement. This is an important statement for this function
	// since it is necessary to ensure ownership authorization matches before customers run
	// the `SHOW ALL GRANT STMTS` command. At the moment, the ownership statement depends entirely on
	// ownership on the CockroachDB instance that the command is run on. Therefore, it is possible
	// that the ownership statement will not execute on a different instance, such as if the user
	// does not exist.
	owner, _ := privilegesJSON.FetchValKey("ownerProto")
	if err != nil {
		return nil, nil, errors.Wrapf(err, "failed to fetch owner from JSON %s", json)
	}

	var objectKind, objectName string
	switch t := desc.Union.(type) {
	case *descpb.Descriptor_Table:
		objectKind = "TABLE"
		objectName = t.Table.Name
	case *descpb.Descriptor_Database:
		objectKind = "DATABASE"
		objectName = t.Database.Name
	case *descpb.Descriptor_Schema:
		objectKind = "SCHEMA"
		objectName = t.Schema.Name
	case *descpb.Descriptor_Type:
		objectKind = "TYPE"
		objectName = t.Type.Name
	default:
		return nil, nil, errors.Errorf("unsupported descriptor type %T", t)
	}
	ownerStmtStr := tree.NewDString(
		fmt.Sprintf("ALTER %s %s OWNER TO %s", objectKind, objectName, owner))
	ownerStmt = tree.Datum(ownerStmtStr)

	// Now, we will construct the grant statements. Bit-unmasking will be used to
	// determine the privileges to grant and the grant will be formatted accordingly
	// and built by string builder.

	return ownerStmt, nil, nil
}
