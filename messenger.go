package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	_ "github.com/lib/pq"
)

// Конфигурация базы данных
const (
	host     = "localhost"
	port     = 8080
	user     = "postgres"
	password = "ghbdtn"
	dbname   = "MyChat"
)

var db *sql.DB

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type Room struct {
	Name          string
	Clients       map[*websocket.Conn]bool
	Broadcast     chan Message
	EditBroadcast chan Message
	mu            sync.RWMutex
	Messages      []Message
}

var rooms = make(map[string]*Room)
var roomsMu sync.RWMutex

type User struct {
	UserID    int       `json:"user_id"`
	Username  string    `json:"username"`
	LastVisit time.Time `json:"last_visit"`
}

var users = make(map[string]*User)
var usersMu sync.RWMutex

type Message struct {
	MessageID int       `json:"message_id"`
	Username  string    `json:"username"`
	UserID    int       `json:"user_id"`
	Message   string    `json:"message"`
	Room      string    `json:"room"`
	Timestamp time.Time `json:"timestamp"`
	Edited    bool      `json:"edited"`
	File      string    `json:"file"` // Поле для имени файла
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

	// Создание таблиц в базе данных
	_, err = db.Exec(`
        CREATE TABLE IF NOT EXISTS users (
            user_id SERIAL PRIMARY KEY,
            username VARCHAR(255) NOT NULL,
            last_visit TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
        );

        CREATE TABLE IF NOT EXISTS rooms (
			room_id SERIAL PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			type INTEGER DEFAULT 0
		);

        CREATE TABLE IF NOT EXISTS messages (
            message_id SERIAL PRIMARY KEY,
            room_id INTEGER NOT NULL,
            user_id INTEGER NOT NULL,
            message TEXT NOT NULL,
            timestamp TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
            file VARCHAR(255),
			edited BOOLEAN NOT NULL DEFAULT FALSE,
            FOREIGN KEY (room_id) REFERENCES rooms (room_id),
            FOREIGN KEY (user_id) REFERENCES users (user_id)
        );
    `)
	if err != nil {
		fmt.Println(err)
		return
	}

	http.HandleFunc("/", homePage)
	http.HandleFunc("/ws", handleConnections)

	fmt.Println("Server started on :8080")
	err = http.ListenAndServe(":5050", nil)
	if err != nil {
		fmt.Println(err)
		return
	}

}

func homePage(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "Welcome to the Chat Room!")
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer conn.Close()

	// Получаем имя комнаты и имя пользователя из параметров запроса
	roomName := r.URL.Query().Get("room")
	username := r.URL.Query().Get("username")

	usersMu.Lock()
	user, ok := users[username]
	if !ok {
		user = &User{
			Username:  username,
			LastVisit: time.Now(),
		}
		users[username] = user

		// Добавление пользователя в базу данных
		var userID int
		err = db.QueryRow("INSERT INTO users (username, last_visit) VALUES ($1, $2) RETURNING user_id", username, time.Now()).Scan(&userID)
		if err != nil {
			fmt.Println(err)
			return
		}
		user.UserID = userID
	}
	usersMu.Unlock()

	roomsMu.RLock()
	room, ok := rooms[roomName]
	roomsMu.RUnlock()

	// Создаем новую комнату, если она не существует
	if !ok {
		room = &Room{
			Name:          roomName,
			Clients:       make(map[*websocket.Conn]bool),
			Broadcast:     make(chan Message),
			EditBroadcast: make(chan Message),
			mu:            sync.RWMutex{},
			Messages:      make([]Message, 0),
		}
		roomsMu.Lock()
		rooms[roomName] = room
		roomsMu.Unlock()
		go handleRoomBroadcast(room) // Запускаем цикл широковещательной рассылки

		// Добавление комнаты в базу данных
		var roomID int
		err = db.QueryRow("INSERT INTO rooms (name) VALUES ($1) RETURNING room_id", roomName).Scan(&roomID)
		if err != nil {
			fmt.Println(err)
			return
		}
	}

	room.mu.Lock()
	room.Clients[conn] = true
	room.mu.Unlock()

	go func() {
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				fmt.Println(err)
				room.mu.Lock()
				delete(room.Clients, conn)
				room.mu.Unlock()
				return
			}

			msg := Message{}
			err = json.Unmarshal(message, &msg)
			if err != nil {
				fmt.Println(err)
				return
			}

			if msg.File != "" {
				// Обработка отправки файла
				file, err := os.Open(msg.File)
				if err != nil {
					fmt.Println(err)
					return
				}
				defer file.Close()

				// Сохранение файла на сервере
				filename := fmt.Sprintf("%s_%s", time.Now().Format("2006-01-02_15-04-05"), filepath.Base(msg.File))
				newFile, err := os.Create(filename)
				if err != nil {
					fmt.Println(err)
					return
				}
				defer newFile.Close()

				_, err = io.Copy(newFile, file)
				if err != nil {
					fmt.Println(err)
					return
				}

				// Обновление сообщения с файлом
				msg.File = filename
			}

			// Обработка отправки текстового сообщения
			if msg.MessageID != 0 {
				if msg.Message == "" {
					// Обработка запроса на удаление сообщения
					err := handleDeleteMessage(conn, room, msg.MessageID)
					if err != nil {
						fmt.Println(err)
						return
					}
				} else {
					// Обработка запроса на редактирование сообщения
					err := handleEditMessage(conn, room, msg.MessageID, msg.Message)
					if err != nil {
						fmt.Println(err)
						return
					}
				}
			} else {
				// Обработка отправки нового сообщения
				msg.UserID = user.UserID
				msg.Username = user.Username
				msg.Room = roomName
				msg.Timestamp = time.Now()

				// Добавление сообщения в базу данных
				var messageID int
				err = db.QueryRow("INSERT INTO messages (room_id, user_id, message, timestamp, file) VALUES ((SELECT room_id FROM rooms WHERE name = $1), $2, $3, $4, $5) RETURNING message_id", roomName, user.UserID, msg.Message, time.Now(), msg.File).Scan(&messageID)
				if err != nil {
					fmt.Println(err)
					return
				}
				msg.MessageID = messageID

				room.mu.Lock()
				room.Messages = append(room.Messages, msg)
				room.mu.Unlock()

				room.Broadcast <- msg
			}
		}
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			fmt.Println(err)
			room.mu.Lock()
			delete(room.Clients, conn)
			room.mu.Unlock()
			return
		}

		// Обработка отправки сообщения
		msg := Message{}
		err = json.Unmarshal(message, &msg)
		if err != nil {
			fmt.Println(err)
			return
		}

		if msg.File == "" {
			// Обработка отправки текстового сообщения
			msg.Room = roomName
			msg.Username = username
			msg.Timestamp = time.Now()
			msg.UserID = user.UserID

			// Добавление сообщения в базу данных
			var messageID int
			err = db.QueryRow("INSERT INTO messages (room_id, user_id, message, timestamp) VALUES ($1, $2, $3, $4) RETURNING message_id", getRoomID(roomName), getUserID(username), msg.Message, msg.Timestamp).Scan(&messageID)
			if err != nil {
				fmt.Println(err)
				return
			}
			msg.MessageID = messageID

			room.mu.Lock()
			room.Messages = append(room.Messages, msg)
			room.mu.Unlock()

			room.Broadcast <- msg
		}
	}
}

func (r *Room) editMessage(messageID int, newMessage string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i, msg := range r.Messages {
		if msg.MessageID == messageID {
			r.Messages[i].Message = newMessage
			r.Messages[i].Edited = true // Обновляем поле edited

			// Обновляем сообщение в базе данных
			_, err := db.Exec("UPDATE messages SET message = $1, edited = TRUE WHERE message_id = $2", newMessage, messageID)
			if err != nil {
				return err
			}

			return nil
		}
	}

	return errors.New("сообщение не найдено")
}

func handleEditMessage(conn *websocket.Conn, room *Room, messageID int, newMessage string) error {
	err := room.editMessage(messageID, newMessage)
	if err != nil {
		return err
	}

	// Отправляем обновленное сообщение всем клиентам в комнате
	for conn := range room.Clients {
		err := conn.WriteJSON(room.Messages)
		if err != nil {
			return err
		}
	}

	return nil
}
func (r *Room) deleteMessage(messageID int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i, msg := range r.Messages {
		if msg.MessageID == messageID {
			r.Messages = append(r.Messages[:i], r.Messages[i+1:]...)

			// Удаляем сообщение из базы данных
			_, err := db.Exec("DELETE FROM messages WHERE message_id = $1", messageID)
			if err != nil {
				return err
			}

			return nil
		}
	}

	return errors.New("сообщение не найдено")
}

func handleDeleteMessage(conn *websocket.Conn, room *Room, messageID int) error {
	err := room.deleteMessage(messageID)
	if err != nil {
		return err
	}

	// Отправляем обновленный список сообщений всем клиентам в комнате
	for conn := range room.Clients {
		err := conn.WriteJSON(room.Messages)
		if err != nil {
			return err
		}
	}

	return nil
}

func handleRoomBroadcast(room *Room) {
	for {
		select {
		case msg := <-room.Broadcast:
			for conn := range room.Clients {
				err := conn.WriteJSON(msg)
				if err != nil {
					fmt.Println(err)
					room.mu.Lock()
					delete(room.Clients, conn)
					room.mu.Unlock()
					continue
				}
			}
		case editMsg := <-room.EditBroadcast:
			for conn := range room.Clients {
				// Отправляем обновленное сообщение как редактирование существующего сообщения
				err := conn.WriteJSON(map[string]interface{}{
					"action":  "edit",
					"message": editMsg,
				})
				if err != nil {
					fmt.Println(err)
					room.mu.Lock()
					delete(room.Clients, conn)
					room.mu.Unlock()
					continue
				}
			}
		}
	}
}

func getRoomID(roomName string) int {
	var roomID int
	err := db.QueryRow("SELECT room_id FROM rooms WHERE name = $1", roomName).Scan(&roomID)
	if err != nil {
		fmt.Println(err)
		return 0
	}
	return roomID
}

func getUserID(username string) int {
	var userID int
	err := db.QueryRow("SELECT user_id FROM users WHERE username = $1", username).Scan(&userID)
	if err != nil {
		fmt.Println(err)
		return 0
	}
	return userID
}
