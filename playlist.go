package main

import (
	"fmt"
	"net/url"
	"os/exec"
	"strings"
)

// PlaylistEntry holds the resolved title and watch URL for a single playlist item.
type PlaylistEntry struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// isPlaylistURL returns true when query is a YouTube URL containing a playlist ID (list= param).
func isPlaylistURL(query string) bool {
	if !strings.HasPrefix(query, "http://") && !strings.HasPrefix(query, "https://") {
		return false
	}
	u, err := url.Parse(query)
	if err != nil {
		return false
	}
	return u.Query().Get("list") != ""
}

// fetchPlaylistEntries uses yt-dlp to fetch up to max entries from a playlist URL.
// Returns the playlist title and a slice of entries, each with a resolved watch URL.
func fetchPlaylistEntries(rawURL string, max int) (playlistTitle string, entries []PlaylistEntry, err error) {
	out, err := exec.Command("yt-dlp",
		"--flat-playlist",
		"--no-warnings",
		"--playlist-end", fmt.Sprintf("%d", max),
		"--print", "%(playlist_title)s\t%(id)s\t%(title)s",
		"--extractor-args", "youtube:player_client=tv_embedded,mweb",
		rawURL,
	).Output()
	if err != nil {
		return "", nil, fmt.Errorf("yt-dlp: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		pTitle, id, vTitle := parts[0], parts[1], parts[2]
		if playlistTitle == "" && pTitle != "" && pTitle != "NA" {
			playlistTitle = pTitle
		}
		if id == "" || id == "NA" {
			continue
		}
		entries = append(entries, PlaylistEntry{
			Title: vTitle,
			URL:   "https://www.youtube.com/watch?v=" + id,
		})
	}

	if playlistTitle == "" {
		playlistTitle = "Playlist"
	}
	return playlistTitle, entries, nil
}

// applyPlaylistMode merges playlist tracks into the existing queue according to mode:
//
//	"append"     — playlist tracks go after all existing tracks
//	"alternate"  — interleave: existing[0], playlist[0], existing[1], playlist[1], ...
//	"distribute" — insert playlist tracks at evenly-spaced positions in the queue
func applyPlaylistMode(existing []Track, playlist []Track, mode string) []Track {
	switch mode {
	case "alternate":
		result := make([]Track, 0, len(existing)+len(playlist))
		i, j := 0, 0
		for i < len(existing) || j < len(playlist) {
			if i < len(existing) {
				result = append(result, existing[i])
				i++
			}
			if j < len(playlist) {
				result = append(result, playlist[j])
				j++
			}
		}
		return result

	case "distribute":
		if len(existing) == 0 {
			return playlist
		}
		// Insert playlist tracks at evenly-spaced intervals inside existing.
		total := len(existing) + len(playlist)
		result := make([]Track, 0, total)
		// step = how many existing tracks between each playlist insertion
		step := float64(len(existing)+1) / float64(len(playlist)+1)
		ei, pi := 0, 0
		nextInsert := step
		for ei < len(existing) || pi < len(playlist) {
			// Insert playlist track if we've reached the next insertion point.
			for pi < len(playlist) && float64(ei) >= nextInsert {
				result = append(result, playlist[pi])
				pi++
				nextInsert += step
			}
			if ei < len(existing) {
				result = append(result, existing[ei])
				ei++
			}
		}
		// Append any remaining playlist tracks.
		result = append(result, playlist[pi:]...)
		return result

	default: // "append"
		return append(existing, playlist...)
	}
}
