// Copyright 2017-2018 New Vector Ltd
// Copyright 2019-2020 The Matrix.org Foundation C.I.C.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sqlite3

import (
	"context"
	"database/sql"

	"github.com/matrix-org/dendrite/common"
	"github.com/matrix-org/dendrite/roomserver/types"
)

const inviteSchema = `
	CREATE TABLE IF NOT EXISTS roomserver_invites (
		invite_event_id TEXT PRIMARY KEY,
		room_nid INTEGER NOT NULL,
		target_nid INTEGER NOT NULL,
		sender_nid INTEGER NOT NULL DEFAULT 0,
		retired BOOLEAN NOT NULL DEFAULT FALSE,
		invite_event_json TEXT NOT NULL
	);

	CREATE INDEX IF NOT EXISTS roomserver_invites_active_idx ON roomserver_invites (target_nid, room_nid)
		WHERE NOT retired;
`
const insertInviteEventSQL = "" +
	"INSERT INTO roomserver_invites (invite_event_id, room_nid, target_nid," +
	" sender_nid, invite_event_json) VALUES ($1, $2, $3, $4, $5)" +
	" ON CONFLICT DO NOTHING"

const selectInviteActiveForUserInRoomSQL = "" +
	"SELECT sender_nid FROM roomserver_invites" +
	" WHERE target_nid = $1 AND room_nid = $2" +
	" AND NOT retired"

// Retire every active invite for a user in a room.
// Ideally we'd know which invite events were retired by a given update so we
// wouldn't need to remove every active invite.
// However the matrix protocol doesn't give us a way to reliably identify the
// invites that were retired, so we are forced to retire all of them.
const updateInviteRetiredSQL = `
	UPDATE roomserver_invites SET retired = TRUE WHERE room_nid = $1 AND target_nid = $2 AND NOT retired
`

const selectInvitesAboutToRetireSQL = `
SELECT invite_event_id FROM roomserver_invites WHERE room_nid = $1 AND target_nid = $2 AND NOT retired
`

type inviteStatements struct {
	insertInviteEventStmt               *sql.Stmt
	selectInviteActiveForUserInRoomStmt *sql.Stmt
	updateInviteRetiredStmt             *sql.Stmt
	selectInvitesAboutToRetireStmt      *sql.Stmt
}

func (s *inviteStatements) prepare(db *sql.DB) (err error) {
	_, err = db.Exec(inviteSchema)
	if err != nil {
		return
	}

	return statementList{
		{&s.insertInviteEventStmt, insertInviteEventSQL},
		{&s.selectInviteActiveForUserInRoomStmt, selectInviteActiveForUserInRoomSQL},
		{&s.updateInviteRetiredStmt, updateInviteRetiredSQL},
		{&s.selectInvitesAboutToRetireStmt, selectInvitesAboutToRetireSQL},
	}.prepare(db)
}

func (s *inviteStatements) insertInviteEvent(
	ctx context.Context,
	txn *sql.Tx, inviteEventID string, roomNID types.RoomNID,
	targetUserNID, senderUserNID types.EventStateKeyNID,
	inviteEventJSON []byte,
) (bool, error) {
	stmt := common.TxStmt(txn, s.insertInviteEventStmt)
	defer stmt.Close() // nolint: errcheck
	result, err := stmt.ExecContext(
		ctx, inviteEventID, roomNID, targetUserNID, senderUserNID, inviteEventJSON,
	)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return count != 0, nil
}

func (s *inviteStatements) updateInviteRetired(
	ctx context.Context,
	txn *sql.Tx, roomNID types.RoomNID, targetUserNID types.EventStateKeyNID,
) (eventIDs []string, err error) {
	// gather all the event IDs we will retire
	stmt := txn.Stmt(s.selectInvitesAboutToRetireStmt)
	rows, err := stmt.QueryContext(ctx, roomNID, targetUserNID)
	if err != nil {
		return nil, err
	}
	defer (func() { err = rows.Close() })()
	for rows.Next() {
		var inviteEventID string
		if err = rows.Scan(&inviteEventID); err != nil {
			return nil, err
		}
		eventIDs = append(eventIDs, inviteEventID)
	}

	// now retire the invites
	stmt = txn.Stmt(s.updateInviteRetiredStmt)
	_, err = stmt.ExecContext(ctx, roomNID, targetUserNID)
	return
}

// selectInviteActiveForUserInRoom returns a list of sender state key NIDs
func (s *inviteStatements) selectInviteActiveForUserInRoom(
	ctx context.Context,
	targetUserNID types.EventStateKeyNID, roomNID types.RoomNID,
) ([]types.EventStateKeyNID, error) {
	rows, err := s.selectInviteActiveForUserInRoomStmt.QueryContext(
		ctx, targetUserNID, roomNID,
	)
	if err != nil {
		return nil, err
	}
	defer common.CloseAndLogIfError(ctx, rows, "selectInviteActiveForUserInRoom: rows.close() failed")
	var result []types.EventStateKeyNID
	for rows.Next() {
		var senderUserNID int64
		if err := rows.Scan(&senderUserNID); err != nil {
			return nil, err
		}
		result = append(result, types.EventStateKeyNID(senderUserNID))
	}
	return result, nil
}
