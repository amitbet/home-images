package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web/*
var webFiles embed.FS
var webFS, _ = fs.Sub(webFiles, "web")
var webHandler = http.FileServer(http.FS(webFS))

func serveUI(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-cache")
	webHandler.ServeHTTP(w, r)
	return nil
}
