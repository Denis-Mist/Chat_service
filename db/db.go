package db

import (
	"database/sql"
	"fmt"

	_ "github.com/lib/pq"
)

var DbData = map[string]string{
	"host":     "localhost",
	"port":     "5432",
	"user":     "postgres",
	"password": "ghbdtn",
}

func Connect() (*sql.DB, error) {
	connStr := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=websocket_db sslmode=disable",
		DbData["host"], DbData["port"], DbData["user"], DbData["password"])
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}
	err = db.Ping()
	if err != nil {
		return nil, err
	}
	return db, nil
}

func CreateDatabaseIfNotExists(db *sql.DB) error {
	_, err := db.Exec(`CREATE DATABASE IF NOT EXISTS websocket_db`)
	return err
}

func CreateTableIfNotExists(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS rooms (
			id SERIAL PRIMARY KEY,
			name VARCHAR(255) NOT NULL
		);

		CREATE TABLE IF NOT EXISTS messages (
			id SERIAL PRIMARY KEY,
			room_id INTEGER NOT NULL REFERENCES rooms(id),
			message BYTEA NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
	`)
	return err
}
