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

// Определение структуры Message
type Message struct {
	MessageID int    `json:"message_id"`
	Username  string `json:"username"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

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

func getMessagesByRoomName(c *fiber.Ctx) error {
	roomName := c.Params("name")
	var messages []Message

	rows, err := db.Query(`
		SELECT m.message_id, u.username, m.message, m.timestamp 
		FROM messages m 
		JOIN users u ON m.user_id = u.user_id 
		JOIN rooms r ON m.room_id = r.room_id 
		WHERE r.name = $1
	`, roomName)
	if err != nil {
		return c.Status(500).SendString("Error querying messages: " + err.Error())
	}
	defer rows.Close()

	for rows.Next() {
		var msg Message
		if err := rows.Scan(&msg.MessageID, &msg.Username, &msg.Message, &msg.Timestamp); err != nil {
			return c.Status(500).SendString("Error scanning message: " + err.Error())
		}
		messages = append(messages, msg)
	}

	if err := rows.Err(); err != nil {
		return c.Status(500).SendString("Error with rows: " + err.Error())
	}

	if len(messages) == 0 {
		return c.Status(404).SendString("No messages found for this room")
	}

	return c.JSON(messages)
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
	app.Get("/rooms/:name/messages", getMessagesByRoomName)

	err = app.Listen(":8080")
	if err != nil {
		fmt.Println(err)
		return
	}
}
