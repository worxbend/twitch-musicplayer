package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// RegisterRoutes mounts all v1 resource routes onto the provided chi router.
func RegisterRoutes(r chi.Router, songs *SongsHandler, queue *QueueHandler, player *PlayerHandler) {
	r.Route("/v1", func(r chi.Router) {
		// Songs
		r.Get("/songs", songs.List)
		r.Get("/songs/{id}", songs.Get)
		r.Get("/songs/{id}/content", songs.StreamContent)
		r.Get("/songs/{id}:stream", songs.StreamContent)
		r.Get("/songs/{id}/cover", songs.Cover)
		r.Post("/songs/{id}:block", queue.Block)
		r.Post("/songs/{id}:unblock", queue.Unblock)
		r.Get("/blocked", queue.ListBlocked)

		// Queue
		r.Get("/queue", queue.List)
		r.Post("/queue", queue.Add)
		r.Delete("/queue/{id}", queue.Remove)
		r.Post("/queue/{id}:move", queue.Move)
		r.Post("/queue:skip", queue.Skip)
		r.Post("/queue:clear", queue.Clear)
		r.Post("/queue:play-next", queue.PlayNext)

		// Player
		r.Get("/player", player.State)
		r.Post("/player:skip", player.Skip)
		r.Get("/history", player.History)
		r.Get("/now-playing.txt", player.NowPlayingText)
	})
}

// RegisterRoutesHTTP is a convenience wrapper for use with a plain http.Handler.
func RegisterRoutesHTTP(songs *SongsHandler, queue *QueueHandler, player *PlayerHandler) http.Handler {
	r := chi.NewRouter()
	RegisterRoutes(r, songs, queue, player)
	return r
}
