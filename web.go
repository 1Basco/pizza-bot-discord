package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

type guildStatus struct {
	GuildID string   `json:"guildId"`
	Current string   `json:"current"`
	Queue   []string `json:"queue"`
	Loop    bool     `json:"loop"`
	Running bool     `json:"running"`
}

func startWebServer() {
	http.HandleFunc("/api/status", handleAPIStatus)
	http.HandleFunc("/", handleWebIndex)
	log.Println("Web status server listening on http://localhost:3000")
	if err := http.ListenAndServe(":3000", nil); err != nil {
		log.Println("Web server error:", err)
	}
}

func handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	playersMu.Lock()
	statuses := make([]guildStatus, 0, len(players))
	for gid, p := range players {
		p.mu.Lock()
		gs := guildStatus{
			GuildID: gid,
			Loop:    p.loop,
			Running: p.running,
			Queue:   make([]string, 0, len(p.queue)),
		}
		if p.current != nil {
			gs.Current = p.current.Title
		}
		for _, t := range p.queue {
			gs.Queue = append(gs.Queue, t.Title)
		}
		p.mu.Unlock()
		statuses = append(statuses, gs)
	}
	playersMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(statuses)
}

func handleWebIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>pizza-bot queue</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { background: #1a1a1a; color: #e0e0e0; font-family: monospace; padding: 2rem; }
  h1 { color: #f97316; margin-bottom: 1.5rem; font-size: 1.4rem; }
  .guild { background: #252525; border: 1px solid #333; border-radius: 6px; padding: 1.2rem; margin-bottom: 1rem; }
  .guild-id { color: #888; font-size: 0.75rem; margin-bottom: 0.8rem; }
  .now-playing { color: #4ade80; font-size: 1rem; margin-bottom: 0.6rem; }
  .now-playing span { color: #e0e0e0; }
  .badge { display: inline-block; font-size: 0.7rem; padding: 1px 6px; border-radius: 3px; margin-left: 6px; }
  .loop-on { background: #f97316; color: #1a1a1a; }
  .loop-off { background: #333; color: #888; }
  .queue-list { margin-top: 0.6rem; }
  .queue-list li { color: #aaa; font-size: 0.85rem; padding: 2px 0; list-style: none; counter-increment: queue-counter; }
  .queue-list li::before { content: counter(queue-counter) ". "; color: #555; }
  ol.queue-list { counter-reset: queue-counter; }
  .empty { color: #555; font-style: italic; }
  .status-dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 6px; }
  .dot-on { background: #4ade80; }
  .dot-off { background: #555; }
  .refresh { color: #555; font-size: 0.7rem; margin-bottom: 1.5rem; }
  #no-guilds { color: #555; font-style: italic; }
</style>
</head>
<body>
<h1>🍕 pizza-bot queue</h1>
<div class="refresh" id="last-refresh">refreshing every 3s...</div>
<div id="status"></div>
<script>
function render(guilds) {
  const el = document.getElementById('status');
  if (!guilds || guilds.length === 0) {
    el.innerHTML = '<p id="no-guilds">No active guilds.</p>';
    return;
  }
  el.innerHTML = guilds.map(g => {
    const dot = g.running
      ? '<span class="status-dot dot-on"></span>'
      : '<span class="status-dot dot-off"></span>';
    const loop = g.loop
      ? '<span class="badge loop-on">loop ON</span>'
      : '<span class="badge loop-off">loop off</span>';
    const current = g.current
      ? '<div class="now-playing">▶ <span>' + esc(g.current) + '</span>' + loop + '</div>'
      : '<div class="now-playing empty">nothing playing</div>';
    const queue = g.queue && g.queue.length > 0
      ? '<ol class="queue-list">' + g.queue.map(t => '<li>' + esc(t) + '</li>').join('') + '</ol>'
      : '<p class="empty" style="font-size:0.8rem;margin-top:0.4rem">queue empty</p>';
    return '<div class="guild">'
      + '<div class="guild-id">' + dot + 'guild ' + esc(g.guildId) + '</div>'
      + current
      + queue
      + '</div>';
  }).join('');
}

function esc(s) {
  return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}

function refresh() {
  fetch('/api/status')
    .then(r => r.json())
    .then(data => {
      render(data);
      document.getElementById('last-refresh').textContent =
        'last refresh: ' + new Date().toLocaleTimeString();
    })
    .catch(() => {});
}

refresh();
setInterval(refresh, 3000);
</script>
</body>
</html>`)
}
