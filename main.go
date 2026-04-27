package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// ----- Wire types -----

type MatchRequest struct {
	LobbyID     string   `json:"lobby_id"`
	PlayerCount int      `json:"player_count"`
	Players     []string `json:"players"` // EOS UniqueNetId strings
}

type MatchResponse struct {
	Status string         `json:"status"`
	IP     string         `json:"ip"`
	Teams  map[string]int `json:"teams"` // PlayerId -> team enum value
}

// ----- Team enum (mirrors Unreal ETeam) -----

const (
	TeamNone     = 0
	TeamThief    = 1
	TeamEmployee = 2
)

// ----- Matchmaker state -----

type QueuedLobby struct {
	LobbyID      string
	Players      []string
	ResponseChan chan MatchResponse
}

type Matchmaker struct {
	mu           sync.Mutex
	Queue        []QueuedLobby
	TotalInQueue int
	TargetSize   int
}

// ----- HTTP handler -----

func (m *Matchmaker) HandleMatchmake(w http.ResponseWriter, r *http.Request) {
	var req MatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid Request", http.StatusBadRequest)
		return
	}
	if len(req.Players) == 0 {
		http.Error(w, "No players provided", http.StatusBadRequest)
		return
	}

	respChan := make(chan MatchResponse, 1)

	m.mu.Lock()

	m.Queue = append(m.Queue, QueuedLobby{
		LobbyID:      req.LobbyID,
		Players:      req.Players,
		ResponseChan: respChan,
	})
	m.TotalInQueue += len(req.Players)

	fmt.Printf("[Queue] Lobby %s joined with %d players %v. Current: %d/%d\n",
		req.LobbyID, len(req.Players), req.Players, m.TotalInQueue, m.TargetSize)

	if m.TotalInQueue >= m.TargetSize {
		fmt.Println("[Match] Threshold reached, dispatching match...")

		serverIP := "127.0.0.1:7777"

		// Pool every player across every queued lobby into one ordered list.
		allPlayers := make([]string, 0, m.TotalInQueue)
		for _, lobby := range m.Queue {
			allPlayers = append(allPlayers, lobby.Players...)
		}

		// Assign teams: alternate Thief / Employee.
		// Adjust this rule to taste (e.g. 1 thief vs many employees).
		teams := make(map[string]int, len(allPlayers))
		for i, pid := range allPlayers {
			if i%2 == 0 {
				teams[pid] = TeamThief
			} else {
				teams[pid] = TeamEmployee
			}
		}

		fmt.Printf("[Match] Server: %s\n", serverIP)
		for pid, t := range teams {
			fmt.Printf("[Match]   %s -> team %d\n", pid, t)
		}

		response := MatchResponse{
			Status: "found",
			IP:     serverIP,
			Teams:  teams,
		}

		// Send the SAME response to every lobby leader so each client can
		// look up its own player id in the teams map.
		for _, lobby := range m.Queue {
			lobby.ResponseChan <- response
		}

		m.Queue = m.Queue[:0]
		m.TotalInQueue = 0
	}

	m.mu.Unlock()

	// Block until our channel is fed by the dispatch above.
	result := <-respChan

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func main() {
	// TargetSize = total players needed across all queued lobbies before a match dispatches.
	mm := &Matchmaker{TargetSize: 2}

	http.HandleFunc("/matchmake", mm.HandleMatchmake)

	fmt.Println("Matchmaker listening on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		fmt.Println("server error:", err)
	}
}