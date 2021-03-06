/*
 *******************************************************************************
 * Copyright (c) 2018 Edgeworx, Inc.
 *
 * This program and the accompanying materials are made available under the
 * terms of the Eclipse Public License v. 2.0 which is available at
 * http://www.eclipse.org/legal/epl-2.0
 *
 * SPDX-License-Identifier: EPL-2.0
 *******************************************************************************
*/

package iofog_log

import (
	"database/sql"
	"errors"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"strings"
	"time"
)

type DBManager struct {
	db             *sql.DB
	cleanTicker    *time.Ticker
	stopChannel    chan int
	preparedInsert *sql.Stmt
}

func newDBManager() (*DBManager, error) {
	db, err := sql.Open("sqlite3", "file:"+DB_LOCATION+DB_NAME+"?cache=shared&mode=rwc")
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)
	manager := new(DBManager)
	manager.db = db
	manager.stopChannel = make(chan int)

	// create table if it doesn't exist yet
	if _, err = db.Exec(PREPARED_CREATE_TABLE); err != nil {
		db.Close()
		return nil, err
	}

	// define pragmas for performance increase
	if _, err = db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		logger.Println("Error while setting journal_mode=WAL:", err.Error())
	}
	if _, err = db.Exec("PRAGMA synchronous=OFF;"); err != nil {
		logger.Println("Error while setting synchronous=OFF:", err.Error())
	}
	if _, err = db.Exec("PRAGMA temp_store=MEMORY;"); err != nil {
		logger.Println("Error while setting temp_store=MEMORY:", err.Error())
	}
	if _, err = db.Exec("PRAGMA locking_mode=EXCLUSIVE;"); err != nil {
		logger.Println("Error while setting locking_mode=EXCLUSIVE:", err.Error())
	}

	// create indexes if they don't exist yet
	for _, cover := range COVERING_INDEX_FIELDS {
		if _, err = db.Exec(fmt.Sprintf(PREPARED_CREATE_COVERING_INDEX, cover[0], cover[1], cover[2], cover[3],
			TABLE_NAME,  cover[0], cover[1], cover[2], cover[3])); err != nil {
			logger.Printf("Error while creatung three coluimn index on %s_%s_%s_%s: %s",
				cover[0], cover[1], cover[2], cover[3], err.Error())
		}
	}

	// prepare insert statement
	if stmt, err := manager.db.Prepare(PREPARED_INSERT); err != nil {
		db.Close()
		return nil, errors.New("Error while preparing instert:" + err.Error())
	} else {
		manager.preparedInsert = stmt
	}

	return manager, nil
}

func (manager *DBManager) Close() {
	if manager.preparedInsert != nil {
		if err := manager.preparedInsert.Close(); err != nil {
			logger.Println("Error while closing prepared insert statement", err)
		} else {
			logger.Println("Prepared insert statement successfully closed")
		}
	}
	if manager.db != nil {
		if err := manager.db.Close(); err != nil {
			logger.Println("Error while closing db", err)
		} else {
			logger.Println("DB successfully closed")
		}
	}
}

func (manager *DBManager) clearDB(ttl time.Duration) (int64, error) {
	timestamp_end := (time.Now().Add(-ttl)).UnixNano() / 1000000
	logger.Printf("Edge timestamp for deletion is %d\n", timestamp_end)
	delete_stmt := fmt.Sprint(PREPARED_DELETE, timestamp_end)
	result, err := manager.db.Exec(delete_stmt)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (manager *DBManager) insert(msg *LogMessage) (int64, error) {
	level, ok := levelNames[strings.ToUpper(msg.Level)]
	if !ok {
		level = NOTSET
	}
	result, err := manager.preparedInsert.Exec(msg.Publisher, level, msg.Message, msg.TimeStamp)
	if err != nil {
		return 0, errors.New("Error while executing instert: " + err.Error())
	}
	return result.LastInsertId()
}

func (manager *DBManager) query(request *GetLogsRequest) (*GetLogsResponse, error) {
	select_stmt, err := buildQuery(request)
	if err != nil {
		return nil, err
	}

	// debug
	//logger.Println(select_stmt)
	//plan_rows, err := manager.db.Query("EXPLAIN QUERY PLAN " + select_stmt)
	//if err != nil {
	//	logger.Println(err.Error())
	//	return nil, errors.New("Error while executing query plan: " + err.Error())
	//}
	//defer plan_rows.Close()
	//for plan_rows.Next() {
	//	var foo int
	//	var detail string
	//	err = plan_rows.Scan(&foo, &foo, &foo, &detail)
	//	if err != nil {
	//		logger.Println(err)
	//	}
	//	logger.Printf("Plan : %s\n", detail)
	//}

	rows, err := manager.db.Query(select_stmt)
	if err != nil {
		logger.Println(err.Error())
		return nil, errors.New("Error while executing query: " + err.Error())
	}
	defer rows.Close()
	logs := make([]LogMessage, 0, 128)
	var response GetLogsResponse
	for rows.Next() {
		var lvl int
		var logMsg LogMessage
		err = rows.Scan(&logMsg.Publisher, &logMsg.Message, &lvl, &logMsg.TimeStamp)
		if err != nil {
			logger.Println(err)
		}
		logMsg.Level = levelNames[lvl].(string)
		logs = append(logs, logMsg)

	}
	response.Logs = logs
	response.Size = len(logs)
	response.PageNum = request.Page
	response.PageSize = request.PageSize
	err = rows.Err()
	if err != nil {
		logger.Println(err)
	}
	return &response, nil

}

func (manager *DBManager) cleanRoutine(ttl time.Duration) {
	defer func() {
		manager.stopChannel <- 0
	}()
	defer logger.Println("Clean watcher stopped")
	for {
		select {
		case <-manager.cleanTicker.C:
			if deleted, err := manager.clearDB(ttl); err != nil {
				logger.Println("Error while cleaning db: " + err.Error())
			} else {
				logger.Printf("Deleted rows: %d\n", deleted)
			}
		case <-manager.stopChannel:
			return
		}
	}
}

func (manager *DBManager) setCleanInterval(frequency, ttl time.Duration) {
	if manager.cleanTicker != nil {
		manager.cleanTicker.Stop()
		manager.stopChannel <- 0
		<-manager.stopChannel
	}
	logger.Printf("New cleaning frequency is %v", frequency)
	logger.Printf("New ttl is %v", ttl)
	manager.cleanTicker = time.NewTicker(frequency)
	go manager.cleanRoutine(ttl)
}
