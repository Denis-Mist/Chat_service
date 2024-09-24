package main

import (
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

type Client struct {
	hub  *Hub
	room *Room
	conn *websocket.Conn
	send chan []byte
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
}

func newHub() *Hub {
	return &Hub{
		rooms:      make(map[string]*Room),
		register:   make(chan *Client),
		unregister: make(chan *Client),
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

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}
		c.room.broadcast <- message
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
	client.hub.register <- client

	go client.writePump()
	go client.readPump()
}

func main() {
	hub := newHub()
	go hub.run()

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWs(hub, w, r)
	})

	log.Fatal(http.ListenAndServe("localhost:8080", nil))
}
