package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"sync"

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

type Hub struct {
	rooms      map[string]*Room
	register   chan *Client
	unregister chan *Client
	db         *Database
	mutex      sync.RWMutex
}

func newHub(db *Database) *Hub {
	return &Hub{
		rooms:      make(map[string]*Room),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		db:         db,
		mutex:      sync.RWMutex{},
	}
}

func (h *Hub) run() {
	for {
		select {
		case client := <-h.register:
			h.mutex.Lock()
			client.room.clients[client] = true
			h.mutex.Unlock()
		case client := <-h.unregister:
			h.mutex.Lock()
			if _, ok := client.room.clients[client]; ok {
				delete(client.room.clients, client)
				close(client.send)
			}
			h.mutex.Unlock()
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

type Room struct {
	id        string
	clients   map[*Client]bool
	broadcast chan []byte
	mutex     sync.RWMutex
}

func (r *Room) broadcastMessage(message []byte) {
	r.mutex.Lock()
	for client := range r.clients {
		select {
		case client.send <- message:
		default:
			close(client.send)
			delete(r.clients, client)
		}
	}
	r.mutex.Unlock()
}

type Client struct {
	hub   *Hub
	room  *Room
	conn  *websocket.Conn
	send  chan []byte
	id    int
	mutex sync.Mutex
}

func (c *Client) createClientInDatabase() (int, error) {
	result, err := c.hub.db.db.Exec(`
        INSERT INTO clients (room_id, username, connected)
        VALUES ($1, $2, $ 3)
        RETURNING id
    `, c.room.id, "username", true)
	if err != nil {
		return 0, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	return int(id), nil
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.id, _ = c.createClientInDatabase()
	// if err != nil {
	//  log.Printf("error: %v", err)
	//  return
	// }

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}

		log.Printf("Received message from client %d in room %s: %s", c.id, c.room.id, message)

		// Check if the room exists in the rooms table
		var roomExists bool
		err = c.hub.db.db.QueryRow("SELECT EXISTS (SELECT 1 FROM rooms WHERE id = $1)", c.room.id).Scan(&roomExists)
		if err != nil {
			log.Printf("error: %v", err)
			break
		}

		log.Printf("Room %s exists: %t", c.room.id, roomExists)

		if !roomExists {
			// Create the room if it doesn't exist
			_, err = c.hub.db.db.Exec("INSERT INTO rooms (id, name) VALUES ($1, $2)", c.room.id, "my_room")
			if err != nil {
				log.Printf("error: %v", err)
				break
			}
		}

		log.Printf(" Broadcasting message to room %s", c.room.id)

		// Update the client's connected status
		_, err = c.hub.db.db.Exec("UPDATE clients SET connected = TRUE WHERE id = $1", c.id)
		if err != nil {
			log.Printf("error: %v", err)
			break
		}

		c.room.mutex.Lock()
		c.room.broadcast <- message
		c.room.mutex.Unlock()
		_, err = c.hub.db.db.Exec("INSERT INTO messages (room_id, client_id, text) VALUES ($1, $2, $3)", c.room.id, c.id, message)
		if err != nil {
			log.Printf("error: %v", err)
		}
	}

	// Mark the client as disconnected
	c.hub.mutex.Lock()
	delete(c.room.clients, c)
	c.hub.mutex.Unlock()
	_, err := c.hub.db.db.Exec("UPDATE clients SET connected = FALSE WHERE id = $1", c.id)
	if err != nil {
		log.Printf("error: %v", err)
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

	hub.mutex.RLock()
	room, ok := hub.rooms[roomID]
	hub.mutex.RUnlock()
	if !ok {
		room = &Room{id: roomID, clients: make(map[*Client]bool), broadcast: make(chan []byte)}
		hub.mutex.Lock()
		hub.rooms[roomID] = room
		hub.mutex.Unlock()
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
