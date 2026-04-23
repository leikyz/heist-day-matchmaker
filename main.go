package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

type MatchRequest struct {
	LobbyID     string `json:"lobby_id"`
	PlayerCount int    `json:"player_count"`
}

type MatchResponse struct {
	Status string `json:"status"`
	IP     string `json:"ip"`
	Team   int    `json:"team"`
}

type QueuedLobby struct {
	ResponseChan chan MatchResponse
	PlayerCount  int
}

type Matchmaker struct {
	mu           sync.Mutex
	Queue        []QueuedLobby
	TotalInQueue int
	TargetSize   int
}

func (m *Matchmaker) HandleMatchmake(w http.ResponseWriter, r *http.Request) {
	var req MatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid Request", http.StatusBadRequest)
		return
	}

	respChan := make(chan MatchResponse, 1)

	m.mu.Lock()
	m.Queue = append(m.Queue, QueuedLobby{ResponseChan: respChan, PlayerCount: req.PlayerCount})
	m.TotalInQueue += req.PlayerCount

	fmt.Printf("[Queue] Lobby joined (%d players). Current: %d/%d\n", req.PlayerCount, m.TotalInQueue, m.TargetSize)

	if m.TotalInQueue >= m.TargetSize {
		fmt.Println("Match Found! Dispatched to all clients...")
		serverIP := "127.0.0.1:7777"

		for i, lobby := range m.Queue {
			// Even team IDs for first lobby, odd for second
			team := 0
			if i > 0 {
				team = 1
			}

			lobby.ResponseChan <- MatchResponse{
				Status: "found",
				IP:     serverIP,
				Team:   team,
			}
		}

		m.Queue = []QueuedLobby{}
		m.TotalInQueue = 0
	}
	m.mu.Unlock()

	// Wait for the result from the channel
	result := <-respChan

	// CRITICAL: Send as JSON only
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func main() {
	// TargetSize 2 means the match starts as soon as 2 players are found (total)
	mm := &Matchmaker{TargetSize: 2}

	http.HandleFunc("/matchmake", mm.HandleMatchmake)

	fmt.Println("JSON-Only Matchmaker running on :8080")
	http.ListenAndServe(":8080", nil)
}
