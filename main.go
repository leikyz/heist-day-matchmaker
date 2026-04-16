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

// GameServer represents a dedicated Unreal Engine server
type GameServer struct {
	IP            string `json:"ip"`
	Port          string `json:"port"`
	ActivePlayers int    `json:"active_players"`
	IsAvailable   bool   `json:"is_available"`
}

// MatchTicket represents a group of players in the queue (solo or duo)
type MatchTicket struct {
	Conns     []*websocket.Conn // Support multiple connections per ticket
	PartySize int
}

// PartyMember represents a single player's connection in the pre-match menu
type PartyMember struct {
	Conn    *websocket.Conn
	IsReady bool
}

// Party represents a group of players sharing the same EOS Lobby ID
type Party struct {
	Members []*PartyMember
	Ticket  *MatchTicket // Tracks their queue ticket so we can cancel if someone leaves
}

var (
	serversMutex  sync.Mutex
	activeServers []*GameServer

	queueMutex     sync.Mutex
	waitingTickets []*MatchTicket

	partiesMutex sync.Mutex
	parties      = make(map[string]*Party)

	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
)

func main() {
	go matchmakerLoop()

	http.HandleFunc("/api/server/register", handleServerRegistration)

	http.HandleFunc("/api/matchmake", handleClientMatchmaking)

	http.HandleFunc("/api/party", handlePartyConnection)

	fmt.Println("Team Matchmaker & Party Manager started on port :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

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

	fmt.Printf("Server Registered: %s:%s\n", newServer.IP, newServer.Port)
	w.WriteHeader(http.StatusOK)
}

func handlePartyConnection(w http.ResponseWriter, r *http.Request) {
	lobbyId := r.URL.Query().Get("lobbyId")
	if lobbyId == "" {
		http.Error(w, "Missing lobbyId", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket upgrade error:", err)
		return
	}

	member := &PartyMember{Conn: conn, IsReady: false}

	partiesMutex.Lock()
	if parties[lobbyId] == nil {
		parties[lobbyId] = &Party{}
	}
	parties[lobbyId].Members = append(parties[lobbyId].Members, member)
	partiesMutex.Unlock()

	fmt.Printf("Player joined Go tracker for Lobby: %s\n", lobbyId)

	for {
		var msg struct {
			IsReady bool `json:"isReady"`
		}

		err := conn.ReadJSON(&msg)
		if err != nil {
			fmt.Printf("Player left lobby tracker: %s\n", lobbyId)
			removeMemberFromParty(lobbyId, member)
			break
		}

		member.IsReady = msg.IsReady
		checkPartyAndBroadcast(lobbyId)
	}
}

func checkPartyAndBroadcast(lobbyId string) {
	partiesMutex.Lock()
	defer partiesMutex.Unlock()

	party := parties[lobbyId]
	if party == nil {
		return
	}

	total := len(party.Members)
	readyCount := 0

	for _, m := range party.Members {
		if m.IsReady {
			readyCount++
		}
	}

	stateMsg := []byte(fmt.Sprintf(`{"event": "ready_update", "ready": %d, "total": %d}`, readyCount, total))
	for _, m := range party.Members {
		m.Conn.WriteMessage(websocket.TextMessage, stateMsg)
	}

	if total == 2 && readyCount == 2 && party.Ticket == nil {
		fmt.Printf("Lobby %s is fully ready! Sending to matchmaker...\n", lobbyId)

		var conns []*websocket.Conn
		for _, m := range party.Members {
			conns = append(conns, m.Conn)
		}

		ticket := &MatchTicket{
			Conns:     conns,
			PartySize: 2,
		}

		party.Ticket = ticket

		queueMutex.Lock()
		waitingTickets = append(waitingTickets, ticket)
		queueMutex.Unlock()

		queueMsg := []byte(`{"status": "queued"}`)
		for _, c := range conns {
			c.WriteMessage(websocket.TextMessage, queueMsg)
		}
	} else if readyCount < total && party.Ticket != nil {
		fmt.Printf("Lobby %s canceled matchmaking.\n", lobbyId)
		removeTicketFromQueue(party.Ticket)
		party.Ticket = nil
	}
}

func removeMemberFromParty(lobbyId string, member *PartyMember) {
	partiesMutex.Lock()
	defer partiesMutex.Unlock()

	party := parties[lobbyId]
	if party != nil {
		if party.Ticket != nil {
			removeTicketFromQueue(party.Ticket)
			party.Ticket = nil
		}

		for i, m := range party.Members {
			if m == member {
				party.Members = append(party.Members[:i], party.Members[i+1:]...)
				break
			}
		}

		if len(party.Members) == 0 {
			delete(parties, lobbyId)
		}
	}
}

func handleClientMatchmaking(w http.ResponseWriter, r *http.Request) {
	partySizeStr := r.URL.Query().Get("partySize")
	partySize, err := strconv.Atoi(partySizeStr)
	if err != nil || partySize < 1 {
		partySize = 1
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Error during WebSocket upgrade:", err)
		return
	}

	fmt.Printf("Solo/Direct Party of %d joined the queue.\n", partySize)

	ticket := &MatchTicket{Conns: []*websocket.Conn{conn}, PartySize: partySize}

	queueMutex.Lock()
	waitingTickets = append(waitingTickets, ticket)
	queueMutex.Unlock()

	conn.WriteMessage(websocket.TextMessage, []byte(`{"status": "queued"}`))

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
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
			waitingTickets = append(waitingTickets[:i], waitingTickets[i+1:]...)
			break
		}
	}
}

func matchmakerLoop() {
	ticker := time.NewTicker(1 * time.Second)
	for range ticker.C {
		queueMutex.Lock()

		var team1Index = -1
		var team2Index = -1

		for i, t := range waitingTickets {
			if t.PartySize == 2 {
				if team1Index == -1 {
					team1Index = i
				} else if team2Index == -1 {
					team2Index = i
					break
				}
			}
		}

		if team1Index != -1 && team2Index != -1 {
			serversMutex.Lock()
			var matchedServer *GameServer
			for _, srv := range activeServers {
				if srv.IsAvailable {
					matchedServer = srv
					break
				}
			}

			if matchedServer != nil {
				matchedServer.IsAvailable = false
				matchedServer.ActivePlayers = 4

				t2 := waitingTickets[team2Index]
				t1 := waitingTickets[team1Index]

				waitingTickets = append(waitingTickets[:team2Index], waitingTickets[team2Index+1:]...)
				waitingTickets = append(waitingTickets[:team1Index], waitingTickets[team1Index+1:]...)

				matchData := []byte(fmt.Sprintf(`{"status": "found", "ip": "%s:%s"}`, matchedServer.IP, matchedServer.Port))

				for _, c := range t1.Conns {
					c.WriteMessage(websocket.TextMessage, matchData)
					c.Close()
				}
				for _, c := range t2.Conns {
					c.WriteMessage(websocket.TextMessage, matchData)
					c.Close()
				}

				fmt.Printf("2v2 Match Created! Both teams sent to %s:%s\n", matchedServer.IP, matchedServer.Port)
			}
			serversMutex.Unlock()
		}
		queueMutex.Unlock()
	}
}
