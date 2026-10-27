package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
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

type UpdateUserRequest struct {
	Name string `json:"name"`
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

func (s *UserStore) Update(identifier string, newName string) (User, error) {
	if newName == "" {
		return User{}, errors.New("name cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	targetID, isNumeric := strconv.Atoi(identifier)

	for i, u := range s.users {
		if (isNumeric == nil && u.ID == targetID) || strings.EqualFold(u.Name, identifier) {
			s.users[i].Name = newName
			return s.users[i], nil
		}
	}

	return User{}, errors.New("user not found")
}

type UserPayload struct {
	Name string `json:"name"`
}
type CreateUserRequest struct {
	Name string `json:"name"`
}

func usersCollectionHandler(store *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users" {
			http.NotFound(w, r)
			return
		}

		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(store.GetAll())

		case http.MethodPost:
			var req UserPayload
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "malformed JSON body"})
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

func userDetailHandler(store *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identifier := strings.TrimPrefix(r.URL.Path, "/users/")
		if identifier == "" || strings.Contains(identifier, "/") {
			http.NotFound(w, r)
			return
		}

		switch r.Method {
		case http.MethodPut:
			var req UserPayload
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "malformed JSON body"})
				return
			}

			user, err := store.Update(identifier, req.Name)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				if err.Error() == "user not found" {
					w.WriteHeader(http.StatusNotFound)
				} else {
					w.WriteHeader(http.StatusBadRequest)
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(user)

		default:
			w.Header().Set("Allow", "PUT")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	}
}
