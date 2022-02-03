package sqlstore

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/mattermost/focalboard/server/services/store"
	"github.com/mattermost/focalboard/server/utils"

	sq "github.com/Masterminds/squirrel"
	_ "github.com/lib/pq" // postgres driver
	"github.com/mattermost/focalboard/server/model"
	_ "github.com/mattn/go-sqlite3" // sqlite driver

	"github.com/mattermost/mattermost-server/v6/shared/mlog"
)

const (
	legacyTypeBoard = "board"
	maxSearchDepth  = 50
)

type RootIDNilError struct{}

func (re RootIDNilError) Error() string {
	return "rootId is nil"
}

type BoardIDNilError struct{}

func (re BoardIDNilError) Error() string {
	return "boardID is nil"
}

type BlockNotFoundErr struct {
	blockID string
}

func (be BlockNotFoundErr) Error() string {
	return fmt.Sprintf("block not found (block id: %s", be.blockID)
}

func (s *SQLStore) timestampToCharField(name string, as string) string {
	switch s.dbType {
	case mysqlDBType:
		return fmt.Sprintf("date_format(%s, '%%Y-%%m-%%d %%H:%%i:%%S') AS %s", name, as)
	case postgresDBType:
		return fmt.Sprintf("to_char(%s, 'YYYY-MM-DD HH:MI:SS.MS') AS %s", name, as)
	default:
		return fmt.Sprintf("%s AS %s", name, as)
	}
}

func (s *SQLStore) blockFields() []string {
	return []string{
		"id",
		"parent_id",
		"root_id",
		"created_by",
		"modified_by",
		s.escapeField("schema"),
		"type",
		"title",
		"COALESCE(fields, '{}')",
		s.timestampToCharField("insert_at", "insertAt"),
		"create_at",
		"update_at",
		"delete_at",
		"COALESCE(board_id, '0')",
	}
}

func (s *SQLStore) getBlocksWithParentAndType(db sq.BaseRunner, boardID, parentID string, blockType string) ([]model.Block, error) {
	query := s.getQueryBuilder(db).
		Select(s.blockFields()...).
		From(s.tablePrefix + "blocks").
		Where(sq.Eq{"board_id": boardID}).
		Where(sq.Eq{"parent_id": parentID}).
		Where(sq.Eq{"type": blockType})

	rows, err := query.Query()
	if err != nil {
		s.logger.Error(`getBlocksWithParentAndType ERROR`, mlog.Err(err))

		return nil, err
	}
	defer s.CloseRows(rows)

	return s.blocksFromRows(rows)
}

func (s *SQLStore) getBlocksWithParent(db sq.BaseRunner, boardID, parentID string) ([]model.Block, error) {
	query := s.getQueryBuilder(db).
		Select(s.blockFields()...).
		From(s.tablePrefix + "blocks").
		Where(sq.Eq{"parent_id": parentID}).
		Where(sq.Eq{"board_id": boardID})

	rows, err := query.Query()
	if err != nil {
		s.logger.Error(`getBlocksWithParent ERROR`, mlog.Err(err))

		return nil, err
	}
	defer s.CloseRows(rows)

	return s.blocksFromRows(rows)
}

func (s *SQLStore) getBlocksWithRootID(db sq.BaseRunner, boardID, rootID string) ([]model.Block, error) {
	query := s.getQueryBuilder(db).
		Select(s.blockFields()...).
		From(s.tablePrefix + "blocks").
		Where(sq.Eq{"root_id": rootID}).
		Where(sq.Eq{"board_id": boardID})

	rows, err := query.Query()
	if err != nil {
		s.logger.Error(`GetBlocksWithRootID ERROR`, mlog.Err(err))

		return nil, err
	}
	defer s.CloseRows(rows)

	return s.blocksFromRows(rows)
}

func (s *SQLStore) getBlocksWithType(db sq.BaseRunner, boardID, blockType string) ([]model.Block, error) {
	query := s.getQueryBuilder(db).
		Select(s.blockFields()...).
		From(s.tablePrefix + "blocks").
		Where(sq.Eq{"type": blockType}).
		Where(sq.Eq{"board_id": boardID})

	rows, err := query.Query()
	if err != nil {
		s.logger.Error(`getBlocksWithParentAndType ERROR`, mlog.Err(err))

		return nil, err
	}
	defer s.CloseRows(rows)

	return s.blocksFromRows(rows)
}

// getSubTree2 returns blocks within 2 levels of the given blockID.
func (s *SQLStore) getSubTree2(db sq.BaseRunner, boardID string, blockID string, opts model.QuerySubtreeOptions) ([]model.Block, error) {
	query := s.getQueryBuilder(db).
		Select(s.blockFields()...).
		From(s.tablePrefix + "blocks").
		Where(sq.Or{sq.Eq{"id": blockID}, sq.Eq{"parent_id": blockID}}).
		Where(sq.Eq{"board_id": boardID})

	if opts.BeforeUpdateAt != 0 {
		query = query.Where(sq.LtOrEq{"update_at": opts.BeforeUpdateAt})
	}

	if opts.AfterUpdateAt != 0 {
		query = query.Where(sq.GtOrEq{"update_at": opts.AfterUpdateAt})
	}

	if opts.Limit != 0 {
		query = query.Limit(opts.Limit)
	}

	rows, err := query.Query()
	if err != nil {
		s.logger.Error(`getSubTree ERROR`, mlog.Err(err))

		return nil, err
	}
	defer s.CloseRows(rows)

	return s.blocksFromRows(rows)
}

// getSubTree3 returns blocks within 3 levels of the given blockID.
func (s *SQLStore) getSubTree3(db sq.BaseRunner, boardID string, blockID string, opts model.QuerySubtreeOptions) ([]model.Block, error) {
	// This first subquery returns repeated blocks
	query := s.getQueryBuilder(db).Select(
		"l3.id",
		"l3.parent_id",
		"l3.root_id",
		"l3.created_by",
		"l3.modified_by",
		"l3."+s.escapeField("schema"),
		"l3.type",
		"l3.title",
		"l3.fields",
		s.timestampToCharField("l3.insert_at", "insertAt"),
		"l3.create_at",
		"l3.update_at",
		"l3.delete_at",
		"l3.board_id",
	).
		From(s.tablePrefix + "blocks" + " as l1").
		Join(s.tablePrefix + "blocks" + " as l2 on l2.parent_id = l1.id or l2.id = l1.id").
		Join(s.tablePrefix + "blocks" + " as l3 on l3.parent_id = l2.id or l3.id = l2.id").
		Where(sq.Eq{"l1.id": blockID}).
		Where(sq.Eq{"l3.board_id": boardID})

	if opts.BeforeUpdateAt != 0 {
		query = query.Where(sq.LtOrEq{"update_at": opts.BeforeUpdateAt})
	}

	if opts.AfterUpdateAt != 0 {
		query = query.Where(sq.GtOrEq{"update_at": opts.AfterUpdateAt})
	}

	if s.dbType == postgresDBType {
		query = query.Options("DISTINCT ON (l3.id)")
	} else {
		query = query.Distinct()
	}

	if opts.Limit != 0 {
		query = query.Limit(opts.Limit)
	}

	rows, err := query.Query()
	if err != nil {
		s.logger.Error(`getSubTree3 ERROR`, mlog.Err(err))

		return nil, err
	}
	defer s.CloseRows(rows)

	return s.blocksFromRows(rows)
}

func (s *SQLStore) getBlocksForBoard(db sq.BaseRunner, boardID string) ([]model.Block, error) {
	query := s.getQueryBuilder(db).
		Select(s.blockFields()...).
		From(s.tablePrefix + "blocks").
		Where(sq.Eq{"board_id": boardID})

	rows, err := query.Query()
	if err != nil {
		s.logger.Error(`getAllBlocksForBoard ERROR`, mlog.Err(err))
		return nil, err
	}
	defer s.CloseRows(rows)

	return s.blocksFromRows(rows)
}

func getIsTemplateFilter(dbType string) (sq.Sqlizer, error) {
	switch dbType {
	case mysqlDBType:
		return sq.Expr("json_extract(fields, '$.isTemplate')"), nil
	case sqliteDBType:
		return sq.Expr("fields->'$.isTemplate'"), nil
	case postgresDBType:
		return sq.Expr("(fields->'isTemplate')::text::boolean"), nil
	default:
		return nil, fmt.Errorf("invalid dbType")
	}
}

// getAllBoardTemplateBlocks is used to run a data migration. After
// the migration is run, the board templates should live on the boards
// table and not on the blocks table.
func (s *SQLStore) getAllBoardTemplateBlocks(db sq.BaseRunner) ([]model.Block, []model.Block, error) {
	query := s.getQueryBuilder(db).
		Select(s.blockFields()...).
		From(s.tablePrefix + "blocks").
		Where(sq.Eq{"type": legacyTypeBoard})

	filter, err := getIsTemplateFilter(s.dbType)
	if err != nil {
		return nil, nil, err
	}
	query = query.Where(filter)

	rows, err := query.Query()
	if err != nil {
		s.logger.Error(`getAllBoardTemplateBlocks ERROR`, mlog.Err(err))
		return nil, nil, err
	}
	defer s.CloseRows(rows)

	boardTemplateBlocks, err := s.blocksFromRows(rows)
	if err != nil {
		return nil, nil, err
	}

	historyQuery := s.getQueryBuilder(db).
		Select(s.blockFields()...).
		From(s.tablePrefix + "blocks_history").
		Where(sq.Eq{"type": legacyTypeBoard}).
		Where(filter)

	historyRows, err := historyQuery.Query()
	if err != nil {
		s.logger.Error(`getAllBoardTemplateBlocks ERROR`, mlog.Err(err))

		return nil, nil, err
	}
	defer s.CloseRows(historyRows)

	boardTemplateHistoryBlocks, err := s.blocksFromRows(historyRows)
	if err != nil {
		return nil, nil, err
	}

	return boardTemplateBlocks, boardTemplateHistoryBlocks, nil
}

// deleteAllBoardTemplateBlocks is used to run a data migration, and
// it's related to the getAllBoardTemplateBlocks (see its docstring)
func (s *SQLStore) deleteAllBoardTemplateBlocks(db sq.BaseRunner) error {
	query := s.getQueryBuilder(db).
		Delete(s.tablePrefix + "blocks").
		Where(sq.Eq{"type": legacyTypeBoard})

	filter, err := getIsTemplateFilter(s.dbType)
	if err != nil {
		return err
	}
	query = query.Where(filter)

	if _, err := query.Exec(); err != nil {
		return err
	}

	historyQuery := s.getQueryBuilder(db).
		Delete(s.tablePrefix + "blocks").
		Where(sq.Eq{"type": legacyTypeBoard}).
		Where(filter)

	if _, err := historyQuery.Exec(); err != nil {
		return err
	}

	return nil
}

func (s *SQLStore) blocksFromRows(rows *sql.Rows) ([]model.Block, error) {
	results := []model.Block{}

	for rows.Next() {
		var block model.Block
		var fieldsJSON string
		var modifiedBy sql.NullString
		var insertAt sql.NullString

		err := rows.Scan(
			&block.ID,
			&block.ParentID,
			&block.RootID,
			&block.CreatedBy,
			&modifiedBy,
			&block.Schema,
			&block.Type,
			&block.Title,
			&fieldsJSON,
			&insertAt,
			&block.CreateAt,
			&block.UpdateAt,
			&block.DeleteAt,
			&block.BoardID)
		if err != nil {
			// handle this error
			s.logger.Error(`ERROR blocksFromRows`, mlog.Err(err))

			return nil, err
		}

		if modifiedBy.Valid {
			block.ModifiedBy = modifiedBy.String
		}

		err = json.Unmarshal([]byte(fieldsJSON), &block.Fields)
		if err != nil {
			// handle this error
			s.logger.Error(`ERROR blocksFromRows fields`, mlog.Err(err))

			return nil, err
		}

		results = append(results, block)
	}

	return results, nil
}

func (s *SQLStore) getRootID(db sq.BaseRunner, blockID string) (string, error) {
	query := s.getQueryBuilder(db).Select("root_id").
		From(s.tablePrefix + "blocks").
		Where(sq.Eq{"id": blockID})

	row := query.QueryRow()

	var rootID string

	err := row.Scan(&rootID)
	if err != nil {
		return "", err
	}

	return rootID, nil
}

func (s *SQLStore) getParentID(db sq.BaseRunner, blockID string) (string, error) {
	query := s.getQueryBuilder(db).Select("parent_id").
		From(s.tablePrefix + "blocks").
		Where(sq.Eq{"id": blockID})

	row := query.QueryRow()

	var parentID string

	err := row.Scan(&parentID)
	if err != nil {
		return "", err
	}

	return parentID, nil
}

func (s *SQLStore) insertBlock(db sq.BaseRunner, block *model.Block, userID string) error {
	if block.RootID == "" {
		return RootIDNilError{}
	}

	if block.BoardID == "" {
		return BoardIDNilError{}
	}

	fieldsJSON, err := json.Marshal(block.Fields)
	if err != nil {
		return err
	}

	existingBlock, err := s.getBlock(db, block.ID)
	if err != nil {
		return err
	}

	insertQuery := s.getQueryBuilder(db).Insert("").
		Columns(
			"channel_id",
			"id",
			"parent_id",
			"root_id",
			"created_by",
			"modified_by",
			s.escapeField("schema"),
			"type",
			"title",
			"fields",
			"create_at",
			"update_at",
			"delete_at",
			"board_id",
		)

	insertQueryValues := map[string]interface{}{
		"channel_id":            "",
		"id":                    block.ID,
		"parent_id":             block.ParentID,
		"root_id":               block.RootID,
		s.escapeField("schema"): block.Schema,
		"type":                  block.Type,
		"title":                 block.Title,
		"fields":                fieldsJSON,
		"delete_at":             block.DeleteAt,
		"created_by":            block.CreatedBy,
		"modified_by":           block.ModifiedBy,
		"create_at":             block.CreateAt,
		"update_at":             block.UpdateAt,
		"board_id":              block.BoardID,
	}

	block.UpdateAt = utils.GetMillis()
	block.ModifiedBy = userID

	if existingBlock != nil {
		// block with ID exists, so this is an update operation
		query := s.getQueryBuilder(db).Update(s.tablePrefix+"blocks").
			Where(sq.Eq{"id": block.ID}).
			Where(sq.Eq{"board_id": block.BoardID}).
			Set("parent_id", block.ParentID).
			Set("root_id", block.RootID).
			Set("modified_by", block.ModifiedBy).
			Set(s.escapeField("schema"), block.Schema).
			Set("type", block.Type).
			Set("title", block.Title).
			Set("fields", fieldsJSON).
			Set("update_at", block.UpdateAt).
			Set("delete_at", block.DeleteAt)

		if _, err := query.Exec(); err != nil {
			s.logger.Error(`InsertBlock error occurred while updating existing block`, mlog.String("blockID", block.ID), mlog.Err(err))

			return err
		}
	} else {
		block.CreatedBy = userID
		block.CreateAt = utils.GetMillis()
		block.ModifiedBy = userID
		block.UpdateAt = utils.GetMillis()

		insertQueryValues["created_by"] = block.CreatedBy
		insertQueryValues["create_at"] = block.CreateAt
		insertQueryValues["update_at"] = block.UpdateAt
		insertQueryValues["modified_by"] = block.ModifiedBy

		query := insertQuery.SetMap(insertQueryValues).Into(s.tablePrefix + "blocks")
		if _, err := query.Exec(); err != nil {
			return err
		}
	}

	// writing block history
	query := insertQuery.SetMap(insertQueryValues).Into(s.tablePrefix + "blocks_history")
	if _, err := query.Exec(); err != nil {
		return err
	}

	return nil
}

func (s *SQLStore) patchBlock(db sq.BaseRunner, blockID string, blockPatch *model.BlockPatch, userID string) error {
	existingBlock, err := s.getBlock(db, blockID)
	if err != nil {
		return err
	}
	if existingBlock == nil {
		return BlockNotFoundErr{blockID}
	}

	block := blockPatch.Patch(existingBlock)
	return s.insertBlock(db, block, userID)
}

func (s *SQLStore) patchBlocks(db sq.BaseRunner, blockPatches *model.BlockPatchBatch, userID string) error {
	for i, blockID := range blockPatches.BlockIDs {
		err := s.patchBlock(db, blockID, &blockPatches.BlockPatches[i], userID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLStore) insertBlocks(db sq.BaseRunner, blocks []model.Block, userID string) error {
	for i := range blocks {
		err := s.insertBlock(db, &blocks[i], userID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLStore) deleteBlock(db sq.BaseRunner, blockID string, modifiedBy string) error {
	block, err := s.getBlock(db, blockID)
	if err != nil {
		return err
	}

	if block == nil {
		return nil // deleting non-exiting block is not considered an error (for now)
	}

	fieldsJSON, err := json.Marshal(block.Fields)
	if err != nil {
		return err
	}

	now := utils.GetMillis()
	insertQuery := s.getQueryBuilder(db).Insert(s.tablePrefix+"blocks_history").
		Columns(
			"board_id",
			"id",
			"parent_id",
			s.escapeField("schema"),
			"type",
			"title",
			"fields",
			"root_id",
			"modified_by",
			"create_at",
			"update_at",
			"delete_at",
			"created_by",
		).
		Values(
			block.BoardID,
			block.ID,
			block.ParentID,
			block.Schema,
			block.Type,
			block.Title,
			fieldsJSON,
			block.RootID,
			modifiedBy,
			block.CreateAt,
			now,
			now,
			block.CreatedBy,
		)

	if _, err := insertQuery.Exec(); err != nil {
		return err
	}

	deleteQuery := s.getQueryBuilder(db).
		Delete(s.tablePrefix + "blocks").
		Where(sq.Eq{"id": blockID})

	if _, err := deleteQuery.Exec(); err != nil {
		return err
	}

	return nil
}

func (s *SQLStore) getBlockCountsByType(db sq.BaseRunner) (map[string]int64, error) {
	query := s.getQueryBuilder(db).
		Select(
			"type",
			"COUNT(*) AS count",
		).
		From(s.tablePrefix + "blocks").
		GroupBy("type")

	rows, err := query.Query()
	if err != nil {
		s.logger.Error(`GetBlockCountsByType ERROR`, mlog.Err(err))

		return nil, err
	}
	defer s.CloseRows(rows)

	m := make(map[string]int64)

	for rows.Next() {
		var blockType string
		var count int64

		err := rows.Scan(&blockType, &count)
		if err != nil {
			s.logger.Error("Failed to fetch block count", mlog.Err(err))
			return nil, err
		}
		m[blockType] = count
	}
	return m, nil
}

func (s *SQLStore) getBlock(db sq.BaseRunner, blockID string) (*model.Block, error) {
	query := s.getQueryBuilder(db).
		Select(s.blockFields()...).
		From(s.tablePrefix + "blocks").
		Where(sq.Eq{"id": blockID})

	rows, err := query.Query()
	if err != nil {
		s.logger.Error(`GetBlock ERROR`, mlog.Err(err))
		return nil, err
	}

	blocks, err := s.blocksFromRows(rows)
	if err != nil {
		return nil, err
	}

	if len(blocks) == 0 {
		return nil, nil
	}

	return &blocks[0], nil
}

func (s *SQLStore) getBlockHistory(db sq.BaseRunner, blockID string, opts model.QueryBlockHistoryOptions) ([]model.Block, error) {
	var order string
	if opts.Descending {
		order = " DESC "
	}

	query := s.getQueryBuilder(db).
		Select(s.blockFields()...).
		From(s.tablePrefix + "blocks_history").
		Where(sq.Eq{"id": blockID}).
		OrderBy("insert_at" + order)

	if opts.BeforeUpdateAt != 0 {
		query = query.Where(sq.Lt{"update_at": opts.BeforeUpdateAt})
	}

	if opts.AfterUpdateAt != 0 {
		query = query.Where(sq.Gt{"update_at": opts.AfterUpdateAt})
	}

	if opts.Limit != 0 {
		query = query.Limit(opts.Limit)
	}

	rows, err := query.Query()
	if err != nil {
		s.logger.Error(`GetBlockHistory ERROR`, mlog.Err(err))
		return nil, err
	}

	return s.blocksFromRows(rows)
}

// getBoardAndCardByID returns the first parent of type `card` and first parent of type `board` for the block specified by ID.
// `board` and/or `card` may return nil without error if the block does not belong to a board or card.
func (s *SQLStore) getBoardAndCardByID(db sq.BaseRunner, blockID string) (board *model.Board, card *model.Block, err error) {
	// use block_history to fetch block in case it was deleted and no longer exists in blocks table.
	opts := model.QueryBlockHistoryOptions{
		Limit:      1,
		Descending: true,
	}

	blocks, err := s.getBlockHistory(db, blockID, opts)
	if err != nil {
		return nil, nil, err
	}

	if len(blocks) == 0 {
		return nil, nil, store.NewErrNotFound(blockID)
	}

	return s.getBoardAndCard(db, &blocks[0])
}

// getBoardAndCard returns the first parent of type `card` and and the `board` for the specified block.
// `board` and/or `card` may return nil without error if the block does not belong to a board or card.
func (s *SQLStore) getBoardAndCard(db sq.BaseRunner, block *model.Block) (board *model.Board, card *model.Block, err error) {
	var count int // don't let invalid blocks hierarchy cause infinite loop.
	iter := block

	// use block_history to fetch blocks in case they were deleted and no longer exist in blocks table.
	opts := model.QueryBlockHistoryOptions{
		Limit:      1,
		Descending: true,
	}

	for {
		count++
		if card == nil && iter.Type == model.TypeCard {
			card = iter
		}

		if iter.ParentID == "" || card != nil || count > maxSearchDepth {
			break
		}

		blocks, err := s.getBlockHistory(db, iter.ParentID, opts)
		if err != nil {
			return nil, nil, err
		}
		if len(blocks) == 0 {
			return board, card, nil
		}
		iter = &blocks[0]
	}
	board, err = s.getBoard(db, block.BoardID)
	if err != nil {
		return nil, nil, err
	}
	return board, card, nil
}

func (s *SQLStore) replaceBlockID(db sq.BaseRunner, currentID, newID, workspaceID string) error {
	runUpdateForBlocksAndHistory := func(query sq.UpdateBuilder) error {
		if _, err := query.Table(s.tablePrefix + "blocks").Exec(); err != nil {
			return err
		}

		if _, err := query.Table(s.tablePrefix + "blocks_history").Exec(); err != nil {
			return err
		}

		return nil
	}

	baseQuery := s.getQueryBuilder(db).
		Where(sq.Eq{"workspace_id": workspaceID})

	// update ID
	updateIDQ := baseQuery.Update("").
		Set("id", newID).
		Where(sq.Eq{"id": currentID})

	if errID := runUpdateForBlocksAndHistory(updateIDQ); errID != nil {
		s.logger.Error(`replaceBlockID ERROR`, mlog.Err(errID))
		return errID
	}

	// update RootID
	updateRootIDQ := baseQuery.Update("").
		Set("root_id", newID).
		Where(sq.Eq{"root_id": currentID})

	if errRootID := runUpdateForBlocksAndHistory(updateRootIDQ); errRootID != nil {
		s.logger.Error(`replaceBlockID ERROR`, mlog.Err(errRootID))
		return errRootID
	}

	// update ParentID
	updateParentIDQ := baseQuery.Update("").
		Set("parent_id", newID).
		Where(sq.Eq{"parent_id": currentID})

	if errParentID := runUpdateForBlocksAndHistory(updateParentIDQ); errParentID != nil {
		s.logger.Error(`replaceBlockID ERROR`, mlog.Err(errParentID))
		return errParentID
	}

	// update parent contentOrder
	updateContentOrder := baseQuery.Update("")
	if s.dbType == postgresDBType {
		updateContentOrder = updateContentOrder.
			Set("fields", sq.Expr("REPLACE(fields::text, ?, ?)::json", currentID, newID)).
			Where(sq.Like{"fields->>'contentOrder'": "%" + currentID + "%"}).
			Where(sq.Eq{"type": model.TypeCard})
	} else {
		updateContentOrder = updateContentOrder.
			Set("fields", sq.Expr("REPLACE(fields, ?, ?)", currentID, newID)).
			Where(sq.Like{"fields": "%" + currentID + "%"}).
			Where(sq.Eq{"type": model.TypeCard})
	}

	if errParentID := runUpdateForBlocksAndHistory(updateContentOrder); errParentID != nil {
		s.logger.Error(`replaceBlockID ERROR`, mlog.Err(errParentID))
		return errParentID
	}

	return nil
}
