package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/wyw14/solo-039/internal/service"
	"net/http"
)

type Server struct {
	runtime *service.Runtime
	plant   *service.Plant
	router  chi.Router
}

func NewServer() *Server { return NewServerAt("data") }

// NewServerAt builds a server backed by the given data directory.
func NewServerAt(dataDir string) *Server {
	plant := service.NewPlant(dataDir)
	_ = plant.Restore()
	plant.LoadEnergy()
	s := &Server{runtime: service.NewRuntime("baseline"), plant: plant, router: chi.NewRouter()}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.router }
