package main

import (
	"net/url"
	"path"
	"strings"
)

// Player segments are not self-contained downloads. Keep manifests and whole
// media files, including byte-range requests for a complete MP4.
func mediaFragment(raw, mt string) bool {
	mt = strings.ToLower(strings.TrimSpace(strings.Split(mt, ";")[0]))
	if mt == "video/iso.segment" || mt == "audio/iso.segment" {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	name := strings.ToLower(path.Base(u.Path))
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".m4s", ".cmfv", ".cmfa":
		return true
	case ".ts":
		return strings.HasPrefix(name, "segment") || strings.HasPrefix(name, "seg-") || strings.HasPrefix(name, "chunk") || strings.HasPrefix(name, "frag")
	}
	return false
}

func captureNoise(raw, mt string, size int64) bool {
	if mediaFragment(raw, mt) {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	// Endless FLV responses belong to a live player, rather than a finite file.
	// Finite FLV files and explicitly added live URLs remain available.
	mt = strings.ToLower(strings.TrimSpace(strings.Split(mt, ";")[0]))
	return size <= 0 && (strings.EqualFold(path.Ext(u.Path), ".flv") || mt == "video/x-flv")
}
