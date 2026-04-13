package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// --- DATA STRUCTURES ---

// GameServer represents a dedicated Unreal Engine server
type GameServer struct {
	IP            string `json:"ip"`
	Port          string `json:"port"`
	ActivePlayers int    `json:"active_players"`
	IsAvailable   bool   `json:"is_available"`
}

// MatchTicket represents a group of players (a solo player or a group of 2)
type MatchTicket struct {
	Conn      *websocket.Conn
	PartySize int
}

// Global variables protected by Mutexes (to avoid race conditions between threads)
var (
	serversMutex  sync.Mutex
	activeServers []*GameServer

	queueMutex     sync.Mutex
	waitingTickets []*MatchTicket

	// Upgrader to transform HTTP requests into WebSockets
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
)

func main() {
	go matchmakerLoop()

	http.HandleFunc("/api/server/register", handleServerRegistration)

	http.HandleFunc("/api/matchmake", handleClientMatchmaking)

	fmt.Println("🚀 Go 2v2 Team Matchmaker started on port :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// --- SERVER LOGIC (HTTP) ---

func handleServerRegistration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST is allowed", http.StatusMethodNotAllowed)
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

// --- CLIENT LOGIC (WEBSOCKET) ---

func handleClientMatchmaking(w http.ResponseWriter, r *http.Request) {
	// Reads party size from URL (e.g.: ws://127.0.0.1:8080/api/matchmake?partySize=2)
	partySizeStr := r.URL.Query().Get("partySize")
	partySize, err := strconv.Atoi(partySizeStr)
	if err != nil || partySize < 1 {
		partySize = 1 // Default: assume a solo player
	}

	// Upgrade the HTTP request to a WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Error during WebSocket upgrade:", err)
		return
	}

	fmt.Printf("👤 Party of %d joined the queue.\n", partySize)

	// Create a ticket for this party
	ticket := &MatchTicket{Conn: conn, PartySize: partySize}

	// Add the ticket to the queue
	queueMutex.Lock()
	waitingTickets = append(waitingTickets, ticket)
	queueMutex.Unlock()

	// Send confirmation message
	conn.WriteMessage(websocket.TextMessage, []byte(`{"status": "queued"}`))

	// Keep the connection open and listen for client disconnect
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			fmt.Println("❌ Party disconnected from queue.")
			removeTicketFromQueue(ticket)
			break
		}
	}
}

func removeTicketFromQueue(ticket *MatchTicket) {
	queueMutex.Lock()
	defer queueMutex.Unlock()
	for i, t := range waitingTickets {
		if t == ticket {
			// Remove the ticket from the list
			waitingTickets = append(waitingTickets[:i], waitingTickets[i+1:]...)
			break
		}
	}
}

// --- MATCHMAKING CORE ---

// Loop that runs continuously every second
func matchmakerLoop() {
	ticker := time.NewTicker(1 * time.Second)
	for range ticker.C {
		queueMutex.Lock()

		// Look for two groups of 2 players to create a 2v2 match
		var team1Index = -1
		var team2Index = -1

		for i, t := range waitingTickets {
			if t.PartySize == 2 {
				if team1Index == -1 {
					team1Index = i
				} else if team2Index == -1 {
					team2Index = i
					break // We found both teams!
				}
			}
		}

		// If we found two teams of 2 players
		if team1Index != -1 && team2Index != -1 {

			// Look for an available server
			serversMutex.Lock()
			var matchedServer *GameServer
			for _, srv := range activeServers {
				if srv.IsAvailable {
					matchedServer = srv
					break
				}
			}

			// If an available server is found
			if matchedServer != nil {
				// Mark the server as occupied
				matchedServer.IsAvailable = false
				matchedServer.ActivePlayers = 4 // 2 teams of 2 players

				// Retrieve tickets (remove highest index first to avoid shifting issues)
				t2 := waitingTickets[team2Index]
				t1 := waitingTickets[team1Index]

				// Remove them from the queue
				waitingTickets = append(waitingTickets[:team2Index], waitingTickets[team2Index+1:]...)
				waitingTickets = append(waitingTickets[:team1Index], waitingTickets[team1Index+1:]...)

				// Prepare JSON message with server IP
				matchData := fmt.Sprintf(`{"status": "found", "ip": "%s:%s"}`, matchedServer.IP, matchedServer.Port)

				// Send server IP to both party leaders
				t1.Conn.WriteMessage(websocket.TextMessage, []byte(matchData))
				t2.Conn.WriteMessage(websocket.TextMessage, []byte(matchData))

				// Close WebSockets (clients will now connect to the dedicated server via UDP)
				t1.Conn.Close()
				t2.Conn.Close()

				fmt.Printf("⚔️ 2v2 Match Created! Both teams sent to %s:%s\n", matchedServer.IP, matchedServer.Port)
			}
			serversMutex.Unlock()
		}
		queueMutex.Unlock()
	}
}
