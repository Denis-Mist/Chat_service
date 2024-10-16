package main

import (
	"database/sql"
	"fmt"

	"github.com/gofiber/fiber/v2"
	_ "github.com/lib/pq"
)

var db *sql.DB

const (
	host     = "localhost"
	port     = 5432
	user     = "postgres"
	password = "ghbdtn"
	dbname   = "MyChat"
)

func getRooms(c *fiber.Ctx) error {
	var rooms []string

	rows, err := db.Query("SELECT name FROM rooms")
	if err != nil {
		return c.Status(500).SendString(err.Error())
	}
	defer rows.Close()

	for rows.Next() {
		var roomName string
		err := rows.Scan(&roomName)
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		rooms = append(rooms, roomName)
	}

	return c.JSON(rooms)
}

func deleteRoom(c *fiber.Ctx) error {
	roomName := c.Params("name")

	// Начинаем транзакцию
	tx, err := db.Begin()
	if err != nil {
		return c.Status(500).SendString(err.Error())
	}
	defer tx.Rollback() // Откат, если что-то пойдет не так

	// Получаем id комнаты для удаления сообщений
	var roomID int
	err = tx.QueryRow("SELECT room_id FROM rooms WHERE name = $1", roomName).Scan(&roomID)
	if err != nil {
		if err == sql.ErrNoRows {
			return c.Status(404).SendString("Room not found")
		}
		return c.Status(500).SendString(err.Error())
	}

	// Удаляем сообщения из таблицы messages
	_, err = tx.Exec("DELETE FROM messages WHERE room_id = $1", roomID)
	if err != nil {
		return c.Status(500).SendString(err.Error())
	}

	// Удаляем комнату
	_, err = tx.Exec("DELETE FROM rooms WHERE name = $1", roomName)
	if err != nil {
		return c.Status(500).SendString(err.Error())
	}

	// Завершаем транзакцию
	if err = tx.Commit(); err != nil {
		return c.Status(500).SendString(err.Error())
	}

	return c.SendString("Room and associated messages deleted successfully")
}

func main() {
	// Подключение к базе данных
	var err error
	db, err = sql.Open("postgres", fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, password, dbname))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer db.Close()

	app := fiber.New()

	app.Get("/rooms", getRooms)
	app.Delete("/rooms/:name", deleteRoom) // Добавление маршрута для удаления комнаты

	err = app.Listen(":8080")
	if err != nil {
		fmt.Println(err)
		return
	}
}
