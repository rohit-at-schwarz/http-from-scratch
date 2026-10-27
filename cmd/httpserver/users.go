package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
)

type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type UserStore struct {
	mu     sync.RWMutex
	users  []User
	nextID int
}

func NewUserStore() *UserStore {
	return &UserStore{
		users:  make([]User, 0),
		nextID: 1,
	}
}

func (s *UserStore) GetAll() []User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]User, len(s.users))
	copy(result, s.users)

	return result
}

func (s *UserStore) Add(name string) (User, error) {
	if name == "" {
		return User{}, errors.New("name cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	user := User{ID: s.nextID, Name: name}
	s.nextID++
	s.users = append(s.users, user)

	return user, nil
}

type CreateUserRequest struct {
	Name string `json:"name"`
}

func usersHandler(store *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			users := store.GetAll()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(users)

		case http.MethodPost:
			var req CreateUserRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "malformed JSON request body"})
				return
			}

			user, err := store.Add(req.Name)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(user)

		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	}
}
