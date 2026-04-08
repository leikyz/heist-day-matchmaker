package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Represents an Unreal Engine Dedicated Server
type GameServer struct {
	IP            string `json:"ip"`
	Port          string `json:"port"`
	ActivePlayers int    `json:"active_players"`
	IsAvailable   bool   `json:"is_available"`
}

// Global state protected by Mutexes for thread safety
var (
	serversMutex  sync.Mutex
	activeServers []*GameServer

	queueMutex     sync.Mutex
	waitingPlayers []*websocket.Conn

	// Upgrades standard HTTP connections to WebSockets
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
)

func main() {
	// Start the matchmaking loop in the background
	go matchmakerLoop()

	http.HandleFunc("/api/server/register", handleServerRegistration)

	http.HandleFunc("/api/matchmake", handleClientMatchmaking)

	fmt.Println("Go Matchmaking Backend started on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func handleServerRegistration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST allowed", http.StatusMethodNotAllowed)
		return
	}

	var newServer GameServer
	if err := json.NewDecoder(r.Body).Decode(&newServer); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	newServer.IsAvailable = true
	newServer.ActivePlayers = 0

	serversMutex.Lock()
	activeServers = append(activeServers, &newServer)
	serversMutex.Unlock()

	fmt.Printf("✅ Server Registered: %s:%s\n", newServer.IP, newServer.Port)
	w.WriteHeader(http.StatusOK)
}

func handleClientMatchmaking(w http.ResponseWriter, r *http.Request) {
	// Upgrade the HTTP request to a WebSocket connection
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket Upgrade Error:", err)
		return
	}

	fmt.Println("👤 Client joined the matchmaking queue.")

	// Add client to the queue
	queueMutex.Lock()
	waitingPlayers = append(waitingPlayers, conn)
	queueMutex.Unlock()

	// Optional: Notify client they are in queue
	conn.WriteMessage(websocket.TextMessage, []byte(`{"status": "queued"}`))

	// Keep connection alive until matchmaking loop handles them
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			fmt.Println("Client disconnected from queue.")
			removePlayerFromQueue(conn)
			break
		}
	}
}

func removePlayerFromQueue(conn *websocket.Conn) {
	queueMutex.Lock()
	defer queueMutex.Unlock()
	for i, player := range waitingPlayers {
		if player == conn {
			waitingPlayers = append(waitingPlayers[:i], waitingPlayers[i+1:]...)
			break
		}
	}
}

func matchmakerLoop() {
	ticker := time.NewTicker(1 * time.Second)
	for range ticker.C {
		queueMutex.Lock()

		// Do we have at least 2 players waiting?
		if len(waitingPlayers) >= 2 {

			// Try to find an empty server
			serversMutex.Lock()
			var matchedServer *GameServer
			for _, srv := range activeServers {
				if srv.IsAvailable {
					matchedServer = srv
					break
				}
			}

			// If we found a server, group the players!
			if matchedServer != nil {
				// Mark server as full
				matchedServer.IsAvailable = false
				matchedServer.ActivePlayers = 2

				// Pop 2 players from the queue
				p1 := waitingPlayers[0]
				p2 := waitingPlayers[1]
				waitingPlayers = waitingPlayers[2:]

				matchData := fmt.Sprintf(`{"status": "found", "ip": "%s:%s"}`, matchedServer.IP, matchedServer.Port)

				// Push the Server IP to both clients over WebSocket!
				p1.WriteMessage(websocket.TextMessage, []byte(matchData))
				p2.WriteMessage(websocket.TextMessage, []byte(matchData))

				// Close their WebSockets (they are moving to UDP now)
				p1.Close()
				p2.Close()

				fmt.Printf("Match Created! Sent players to %s:%s\n", matchedServer.IP, matchedServer.Port)
			}
			serversMutex.Unlock()
		}

		queueMutex.Unlock()
	}
}
