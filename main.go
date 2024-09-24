package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
	_ "github.com/lib/pq"
)

var DbData = map[string]string{
	"host":     "localhost",
	"port":     "5432",
	"user":     "postgres",
	"password": "ghbdtn",
}

type Database struct {
	db *sql.DB
}

func NewDatabase() (*Database, error) {
	connStr := fmt.Sprintf("host=%s port=%s user=%s password=%s sslmode=disable",
		DbData["host"], DbData["port"], DbData["user"], DbData["password"])

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}

	err = db.Ping()
	if err != nil {
		return nil, err
	}

	_, err = db.Exec("CREATE DATABASE chat")
	if err != nil {
		fmt.Println(err)
		//return nil, err
	}

	err = db.Close()
	if err != nil {
		return nil, err
	}

	connStr = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=chat sslmode=disable",
		DbData["host"], DbData["port"], DbData["user"], DbData["password"])

	db, err = sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}

	err = db.Ping()
	if err != nil {
		return nil, err
	}

	return &Database{db: db}, nil
}

func (db *Database) Close() error {
	return db.db.Close()
}

func (db *Database) CreateTables() error {
	_, err := db.db.Exec(`
        CREATE TABLE IF NOT EXISTS rooms (
            id VARCHAR(50) PRIMARY KEY,
            name VARCHAR(50) NOT NULL
        )
    `)
	if err != nil {
		return err
	}

	_, err = db.db.Exec(`
        CREATE TABLE IF NOT EXISTS clients (
            id SERIAL PRIMARY KEY,
            room_id VARCHAR(50) NOT NULL REFERENCES rooms(id),
            username VARCHAR(50) NOT NULL,
            connected BOOLEAN NOT NULL DEFAULT FALSE
        )
    `)
	if err != nil {
		return err
	}

	_, err = db.db.Exec(`
        CREATE TABLE IF NOT EXISTS messages (
            id SERIAL PRIMARY KEY,
            room_id VARCHAR(50) NOT NULL REFERENCES rooms(id),
            client_id INTEGER NOT NULL REFERENCES clients(id),
            text VARCHAR(255) NOT NULL,
            created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
        )
    `)
	if err != nil {
		return err
	}

	return nil
}

type Client struct {
	hub  *Hub
	room *Room
	conn *websocket.Conn
	send chan []byte
	id   int
}

type Room struct {
	id        string
	clients   map[*Client]bool
	broadcast chan []byte
}

type Hub struct {
	rooms      map[string]*Room
	register   chan *Client
	unregister chan *Client
	db         *Database
}

func newHub(db *Database) *Hub {
	return &Hub{
		rooms:      make(map[string]*Room),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		db:         db,
	}
}

func (h *Hub) run() {
	for {
		select {
		case client := <-h.register:
			client.room.clients[client] = true
		case client := <-h.unregister:
			if _, ok := client.room.clients[client]; ok {
				delete(client.room.clients, client)
				close(client.send)
			}
		default:
			for _, room := range h.rooms {
				select {
				case message := <-room.broadcast:
					for client := range room.clients {
						select {
						case client.send <- message:
						default:
							close(client.send)
							delete(room.clients, client)
						}
					}
				default:
				}
			}
		}
	}
}

func (c *Client) createClientInDatabase() (int, error) {
	_, err := c.hub.db.db.Exec(`
        INSERT INTO clients (room_id, username, connected)
        VALUES ($1, $2, $3)
        RETURNING id
    `, c.room.id, "username", true)
	if err != nil {
		return 0, err
	}

	var clientId int
	err = c.hub.db.db.QueryRow(`
        SELECT id FROM clients
        WHERE room_id = $1 AND username = $2
    `, c.room.id, "username").Scan(&clientId)
	if err != nil {
		return 0, err
	}

	return clientId, nil
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.id, _ = c.createClientInDatabase()
	// if err != nil {
	// 	log.Printf("error: %v", err)
	// 	return
	// }

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}
		c.room.broadcast <- message
		_, err = c.hub.db.db.Exec("INSERT INTO messages (room_id, client_id, text) VALUES ($1, $2, $3)", c.room.id, c.id, message)
		if err != nil {
			log.Printf("error: %v", err)
		}
	}
}

func (c *Client) writePump() {
	defer func() {
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			c.conn.WriteMessage(websocket.TextMessage, message)
		}
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

func serveWs(hub *Hub, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}

	roomID := r.URL.Query().Get("room_id")
	if roomID == "" {
		log.Println("Error: room_id parameter is required")
		return
	}

	room, ok := hub.rooms[roomID]
	if !ok {
		room = &Room{id: roomID, clients: make(map[*Client]bool), broadcast: make(chan []byte)}
		hub.rooms[roomID] = room
	}

	client := &Client{hub: hub, room: room, conn: conn, send: make(chan []byte, 256)}
	room.clients[client] = true
	hub.register <- client

	go client.writePump()
	go client.readPump()
}

func main() {
	db, err := NewDatabase()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	err = db.CreateTables()
	if err != nil {
		log.Fatal(err)
	}

	hub := newHub(db)
	go hub.run()

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWs(hub, w, r)
	})

	log.Fatal(http.ListenAndServe("localhost:8080", nil))
}
