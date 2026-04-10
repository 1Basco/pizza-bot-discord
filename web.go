package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
)

type TrackInfo struct {
	Title string `json:"title"`
	Query string `json:"query"`
	URL   string `json:"url"`
}

type guildStatus struct {
	GuildID string      `json:"guildId"`
	Current *TrackInfo  `json:"current"`
	Queue   []TrackInfo `json:"queue"`
	Loop    bool        `json:"loop"`
	Running bool        `json:"running"`
}

func startWebServer() {
	http.HandleFunc("/api/status", handleAPIStatus)
	http.HandleFunc("/api/play", handleAPIPlay)
	http.HandleFunc("/api/loop", handleAPILoop)
	http.HandleFunc("/api/reorder", handleAPIReorder)
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
			Queue:   make([]TrackInfo, 0, len(p.queue)),
		}
		if p.current != nil {
			gs.Current = &TrackInfo{
				Title: p.current.Title,
				Query: p.current.Query,
				URL:   p.current.URL,
			}
		}
		for _, t := range p.queue {
			gs.Queue = append(gs.Queue, TrackInfo{Title: t.Title, Query: t.Query, URL: t.URL})
		}
		p.mu.Unlock()
		statuses = append(statuses, gs)
	}
	playersMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(statuses)
}

func handleAPIPlay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		GuildID string `json:"guildId"`
		Query   string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.GuildID == "" || strings.TrimSpace(req.Query) == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid request"}`)
		return
	}

	playersMu.Lock()
	p, ok := players[req.GuildID]
	playersMu.Unlock()

	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"guild not found"}`)
		return
	}

	track := Track{Title: req.Query, Query: req.Query}
	if strings.HasPrefix(req.Query, "http://") || strings.HasPrefix(req.Query, "https://") {
		track.URL = req.Query
	}

	p.mu.Lock()
	p.queue = append(p.queue, track)
	queueIdx := len(p.queue) - 1
	shouldStart := !p.running && p.vc != nil
	if shouldStart {
		p.running = true
	}
	p.mu.Unlock()

	go resolveTrackMeta(p, queueIdx, req.Query)
	if shouldStart {
		go p.playLoop()
	}

	Log("INFO", "Track queued via web", map[string]string{"title": req.Query, "guild": req.GuildID})
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"ok":true}`)
}

func handleAPILoop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		GuildID string `json:"guildId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.GuildID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid request"}`)
		return
	}

	playersMu.Lock()
	p, ok := players[req.GuildID]
	playersMu.Unlock()

	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"guild not found"}`)
		return
	}

	p.mu.Lock()
	p.loop = !p.loop
	loopOn := p.loop
	p.mu.Unlock()

	Log("INFO", "Loop toggled via web", map[string]string{"guild": req.GuildID, "loop": fmt.Sprintf("%v", loopOn)})
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"loop":%v}`, loopOn)
}

func handleAPIReorder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		GuildID string `json:"guildId"`
		From    int    `json:"from"`
		To      int    `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.GuildID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid request"}`)
		return
	}

	playersMu.Lock()
	p, ok := players[req.GuildID]
	playersMu.Unlock()

	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"guild not found"}`)
		return
	}

	p.mu.Lock()
	q := p.queue
	if req.From < 0 || req.To < 0 || req.From >= len(q) || req.To >= len(q) || req.From == req.To {
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid indices"}`)
		return
	}
	track := q[req.From]
	newQ := make([]Track, 0, len(q))
	for i, t := range q {
		if i == req.From {
			continue
		}
		newQ = append(newQ, t)
	}
	finalQ := make([]Track, 0, len(q))
	for i, t := range newQ {
		if i == req.To {
			finalQ = append(finalQ, track)
		}
		finalQ = append(finalQ, t)
	}
	if req.To >= len(newQ) {
		finalQ = append(finalQ, track)
	}
	p.queue = finalQ
	p.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"ok":true}`)
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
  body { background: #1a1a1a; color: #e0e0e0; font-family: monospace; padding: 2rem; font-size: 1rem; }
  h1 { color: #f97316; margin-bottom: 1.5rem; font-size: 1.4rem; }
  .search-bar { display: flex; gap: 0.5rem; margin-bottom: 1.5rem; flex-wrap: wrap; }
  .search-bar select { background: #252525; color: #e0e0e0; border: 1px solid #444; border-radius: 4px; padding: 0.4rem 0.6rem; font-family: monospace; font-size: 1rem; }
  .search-bar input { flex: 1; min-width: 200px; background: #252525; color: #e0e0e0; border: 1px solid #444; border-radius: 4px; padding: 0.4rem 0.7rem; font-family: monospace; font-size: 1rem; outline: none; }
  .search-bar input:focus { border-color: #f97316; }
  .search-bar button { background: #f97316; color: #1a1a1a; border: none; border-radius: 4px; padding: 0.4rem 1rem; font-family: monospace; font-size: 1rem; cursor: pointer; font-weight: bold; }
  .search-bar button:hover { background: #ea6a00; }
  .search-bar button:disabled { background: #555; color: #888; cursor: default; }
  .guild { background: #252525; border: 1px solid #333; border-radius: 6px; padding: 1.2rem; margin-bottom: 1rem; }
  .guild-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 0.8rem; }
  .guild-id { color: #888; font-size: 1rem; }
  .now-playing { color: #4ade80; font-size: 1rem; margin-bottom: 0.6rem; }
  .now-playing span { color: #e0e0e0; }
  .track-link { color: #60a5fa; text-decoration: none; font-size: 1rem; margin-left: 6px; }
  .track-link:hover { text-decoration: underline; }
  .track-query { color: #666; font-size: 1rem; margin-left: 4px; }
  .badge { display: inline-block; font-size: 1rem; padding: 1px 6px; border-radius: 3px; margin-left: 6px; }
  .loop-on { background: #f97316; color: #1a1a1a; }
  .loop-off { background: #333; color: #888; }
  .loop-btn { background: none; border: 1px solid #444; border-radius: 4px; color: #aaa; font-family: monospace; font-size: 1rem; padding: 4px 10px; cursor: pointer; }
  .loop-btn:hover { border-color: #f97316; color: #f97316; }
  .queue-list { margin-top: 0.6rem; list-style: none; counter-reset: queue-counter; }
  .queue-item { display: flex; align-items: flex-start; gap: 0.5rem; padding: 4px 2px; border-radius: 3px; cursor: grab; counter-increment: queue-counter; border: 1px solid transparent; }
  .queue-item::before { content: counter(queue-counter) "."; color: #555; min-width: 1.4rem; text-align: right; padding-top: 1px; flex-shrink: 0; font-size: 1rem; }
  .queue-item.drag-over { border-color: #f97316; background: #2e2009; }
  .queue-item.dragging { opacity: 0.4; }
  .drag-handle { color: #444; cursor: grab; user-select: none; padding-top: 1px; font-size: 1rem; }
  .track-info { flex: 1; }
  .track-title { color: #aaa; font-size: 1rem; }
  .track-meta { display: flex; align-items: center; gap: 0.3rem; flex-wrap: wrap; margin-top: 1px; }
  .empty { color: #555; font-style: italic; }
  .status-dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 6px; }
  .dot-on { background: #4ade80; }
  .dot-off { background: #555; }
  .refresh { color: #555; font-size: 1rem; margin-bottom: 1.5rem; }
  #no-guilds { color: #555; font-style: italic; }
  #search-msg { font-size: 1rem; color: #4ade80; margin-top: 0.3rem; display: none; }
</style>
</head>
<body>
<h1>🍕 pizza-bot queue</h1>

<div class="search-bar">
  <select id="guild-select"><option value="">— select guild —</option></select>
  <input id="search-input" type="text" placeholder="Search YouTube or paste URL..." autocomplete="off">
  <button id="search-btn" onclick="addToQueue()">Add to queue</button>
</div>
<div id="search-msg"></div>

<div class="refresh" id="last-refresh">refreshing every 3s...</div>
<div id="status"></div>

<script>
let lastStatus = [];
let dragSrcIdx = null;
let dragGuild = null;

function esc(s) {
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}

function ytLink(t) {
  if (t.url) return t.url;
  return 'https://www.youtube.com/results?search_query=' + encodeURIComponent(t.query || t.title);
}

function render(guilds) {
  lastStatus = guilds || [];
  const sel = document.getElementById('guild-select');
  const prev = sel.value;
  sel.innerHTML = '<option value="">— select guild —</option>'
    + lastStatus.map(g => '<option value="' + esc(g.guildId) + '">' + esc(g.guildId) + '</option>').join('');
  if (prev) sel.value = prev;

  const el = document.getElementById('status');
  if (!guilds || guilds.length === 0) {
    el.innerHTML = '<p id="no-guilds">No active guilds.</p>';
    return;
  }

  el.innerHTML = guilds.map(g => {
    const dot = g.running
      ? '<span class="status-dot dot-on"></span>'
      : '<span class="status-dot dot-off"></span>';
    const loopLabel = g.loop ? 'loop ON' : 'loop off';
    const loopClass = g.loop ? 'loop-on' : 'loop-off';
    const loopBtn = '<button class="loop-btn" onclick="toggleLoop(\'' + esc(g.guildId) + '\')">'
      + (g.loop ? 'disable loop' : 'enable loop') + '</button>';

    let current = '';
    if (g.current) {
      const link = '<a class="track-link" href="' + esc(ytLink(g.current)) + '" target="_blank" rel="noopener">[link]</a>';
      const queryTag = g.current.title !== g.current.query
        ? '<span class="track-query">' + esc(g.current.query) + '</span>' : '';
      current = '<div class="now-playing">▶ <span>' + esc(g.current.title) + '</span>'
        + link + queryTag
        + '<span class="badge ' + loopClass + '">' + loopLabel + '</span>'
        + '</div>';
    } else {
      current = '<div class="now-playing empty">nothing playing</div>';
    }

    let queue = '';
    if (g.queue && g.queue.length > 0) {
      const items = g.queue.map((t, i) => {
        const link = '<a class="track-link" href="' + esc(ytLink(t)) + '" target="_blank" rel="noopener">[link]</a>';
        const queryTag = t.title !== t.query
          ? '<span class="track-query">' + esc(t.query) + '</span>' : '';
        return '<li class="queue-item" draggable="true" data-index="' + i + '" data-guild="' + esc(g.guildId) + '">'
          + '<span class="drag-handle">⠿</span>'
          + '<div class="track-info">'
          + '<div class="track-title">' + esc(t.title) + '</div>'
          + '<div class="track-meta">' + link + queryTag + '</div>'
          + '</div>'
          + '</li>';
      }).join('');
      queue = '<ol class="queue-list">' + items + '</ol>';
    } else {
      queue = '<p class="empty" style="font-size:0.8rem;margin-top:0.4rem">queue empty</p>';
    }

    return '<div class="guild">'
      + '<div class="guild-header">'
      + '<div class="guild-id">' + dot + 'guild ' + esc(g.guildId) + '</div>'
      + loopBtn
      + '</div>'
      + current
      + queue
      + '</div>';
  }).join('');

  attachDragHandlers();
}

function attachDragHandlers() {
  document.querySelectorAll('.queue-item').forEach(el => {
    el.addEventListener('dragstart', e => {
      dragSrcIdx = parseInt(el.dataset.index);
      dragGuild = el.dataset.guild;
      el.classList.add('dragging');
      e.dataTransfer.effectAllowed = 'move';
    });
    el.addEventListener('dragend', () => {
      el.classList.remove('dragging');
      document.querySelectorAll('.queue-item').forEach(i => i.classList.remove('drag-over'));
    });
    el.addEventListener('dragover', e => {
      e.preventDefault();
      e.dataTransfer.dropEffect = 'move';
      document.querySelectorAll('.queue-item').forEach(i => i.classList.remove('drag-over'));
      el.classList.add('drag-over');
    });
    el.addEventListener('drop', e => {
      e.preventDefault();
      const toIdx = parseInt(el.dataset.index);
      if (dragGuild === el.dataset.guild && dragSrcIdx !== null && dragSrcIdx !== toIdx) {
        reorder(dragGuild, dragSrcIdx, toIdx);
      }
      dragSrcIdx = null;
      dragGuild = null;
    });
  });
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

function addToQueue() {
  const guildId = document.getElementById('guild-select').value;
  const query = document.getElementById('search-input').value.trim();
  const msg = document.getElementById('search-msg');
  if (!guildId || !query) return;

  const btn = document.getElementById('search-btn');
  btn.disabled = true;
  fetch('/api/play', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({guildId, query})
  })
  .then(r => r.json())
  .then(d => {
    btn.disabled = false;
    if (d.ok) {
      document.getElementById('search-input').value = '';
      msg.style.display = 'block';
      msg.style.color = '#4ade80';
      msg.textContent = 'Queued: ' + query;
      setTimeout(() => { msg.style.display = 'none'; }, 3000);
      refresh();
    } else {
      msg.style.display = 'block';
      msg.style.color = '#f87171';
      msg.textContent = 'Error: ' + (d.error || 'unknown');
      setTimeout(() => { msg.style.display = 'none'; }, 4000);
    }
  })
  .catch(() => { btn.disabled = false; });
}

function toggleLoop(guildId) {
  fetch('/api/loop', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({guildId})
  })
  .then(r => r.json())
  .then(() => refresh())
  .catch(() => {});
}

function reorder(guildId, from, to) {
  fetch('/api/reorder', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({guildId, from, to})
  })
  .then(() => refresh())
  .catch(() => {});
}

document.getElementById('search-input').addEventListener('keydown', e => {
  if (e.key === 'Enter') addToQueue();
});

refresh();
setInterval(refresh, 3000);
</script>
</body>
</html>`)
}
