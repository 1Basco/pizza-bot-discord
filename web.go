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
	http.HandleFunc("/api/resolve", handleAPIResolve)
	http.HandleFunc("/api/play-playlist", handleAPIPlayPlaylist)
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

func handleAPIResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Query) == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid request"}`)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if !isPlaylistURL(req.Query) {
		fmt.Fprint(w, `{"playlist":false}`)
		return
	}

	title, entries, err := fetchPlaylistEntries(req.Query, 50)
	if err != nil || len(entries) == 0 {
		fmt.Fprint(w, `{"playlist":false}`)
		return
	}

	type respEntry struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	resp := struct {
		Playlist bool        `json:"playlist"`
		Title    string      `json:"title"`
		Count    int         `json:"count"`
		Tracks   []respEntry `json:"tracks"`
	}{
		Playlist: true,
		Title:    title,
		Count:    len(entries),
	}
	for _, e := range entries {
		resp.Tracks = append(resp.Tracks, respEntry{Title: e.Title, URL: e.URL})
	}
	json.NewEncoder(w).Encode(resp)
}

func handleAPIPlayPlaylist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		GuildID string `json:"guildId"`
		Tracks  []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"tracks"`
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.GuildID == "" || len(req.Tracks) == 0 {
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

	playlist := make([]Track, 0, len(req.Tracks))
	for _, t := range req.Tracks {
		playlist = append(playlist, Track{Title: t.Title, Query: t.URL, URL: t.URL})
	}

	p.mu.Lock()
	p.queue = applyPlaylistMode(p.queue, playlist, req.Mode)
	shouldStart := !p.running && p.vc != nil
	if shouldStart {
		p.running = true
	}
	p.mu.Unlock()

	if shouldStart {
		go p.playLoop()
	}

	Log("INFO", "Playlist queued via web", map[string]string{
		"guild": req.GuildID, "count": fmt.Sprintf("%d", len(playlist)), "mode": req.Mode,
	})
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
  /* Playlist modal */
  .modal-backdrop { display: none; position: fixed; inset: 0; background: rgba(0,0,0,0.7); z-index: 100; align-items: center; justify-content: center; }
  .modal-backdrop.open { display: flex; }
  .modal { background: #1e1e1e; border: 1px solid #444; border-radius: 8px; padding: 1.5rem; max-width: 480px; width: 90%; max-height: 80vh; display: flex; flex-direction: column; gap: 1rem; }
  .modal h2 { color: #f97316; font-size: 1rem; }
  .modal-meta { color: #888; font-size: 1rem; }
  .modal-tracks { flex: 1; overflow-y: auto; border: 1px solid #333; border-radius: 4px; padding: 0.5rem; }
  .modal-tracks li { color: #aaa; font-size: 1rem; padding: 3px 0; list-style: decimal inside; }
  .modal-tracks li span { color: #555; font-size: 1rem; }
  .modal-actions { display: flex; gap: 0.5rem; flex-wrap: wrap; }
  .modal-actions button { flex: 1; padding: 0.6rem 0.5rem; border: none; border-radius: 4px; font-family: monospace; font-size: 1rem; font-weight: bold; cursor: pointer; }
  .btn-append   { background: #3b82f6; color: #fff; }
  .btn-alternate { background: #8b5cf6; color: #fff; }
  .btn-distribute { background: #10b981; color: #fff; }
  .btn-cancel   { background: #333; color: #aaa; flex: 0 0 auto; }
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

<!-- Playlist modal -->
<div class="modal-backdrop" id="playlist-modal">
  <div class="modal">
    <h2 id="modal-title">Playlist detectada</h2>
    <div class="modal-meta" id="modal-meta"></div>
    <ol class="modal-tracks" id="modal-tracks"></ol>
    <div class="modal-actions">
      <button class="btn-append"    onclick="submitPlaylist('append')">No fim</button>
      <button class="btn-alternate" onclick="submitPlaylist('alternate')">Intercalar</button>
      <button class="btn-distribute" onclick="submitPlaylist('distribute')">Distribuir</button>
      <button class="btn-cancel"    onclick="closeModal()">Cancelar</button>
    </div>
  </div>
</div>

<div class="refresh" id="last-refresh">refreshing every 3s...</div>
<div id="status"></div>

<script>
let lastStatus = [];
let dragSrcIdx = null;
let dragGuild = null;
let pendingPlaylist = null; // {tracks, guildId} waiting for mode choice

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

  // First check if this is a playlist URL.
  fetch('/api/resolve', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({query})
  })
  .then(r => r.json())
  .then(d => {
    if (d.playlist) {
      btn.disabled = false;
      showPlaylistModal(guildId, d);
      return;
    }
    // Not a playlist — queue normally.
    return fetch('/api/play', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({guildId, query})
    })
    .then(r => r.json())
    .then(d2 => {
      btn.disabled = false;
      if (d2.ok) {
        document.getElementById('search-input').value = '';
        msg.style.display = 'block';
        msg.style.color = '#4ade80';
        msg.textContent = 'Queued: ' + query;
        setTimeout(() => { msg.style.display = 'none'; }, 3000);
        refresh();
      } else {
        msg.style.display = 'block';
        msg.style.color = '#f87171';
        msg.textContent = 'Error: ' + (d2.error || 'unknown');
        setTimeout(() => { msg.style.display = 'none'; }, 4000);
      }
    });
  })
  .catch(() => { btn.disabled = false; });
}

function showPlaylistModal(guildId, data) {
  pendingPlaylist = {guildId, tracks: data.tracks};
  document.getElementById('modal-title').textContent = data.title || 'Playlist';
  document.getElementById('modal-meta').textContent =
    data.count + ' faixas' + (data.count === 50 ? ' (primeiras 50)' : '');
  const ol = document.getElementById('modal-tracks');
  const preview = data.tracks.slice(0, 10);
  ol.innerHTML = preview.map(t =>
    '<li>' + esc(t.title) + (data.tracks.length > 10 && t === preview[preview.length-1]
      ? ' <span>... e mais ' + (data.count - 10) + '</span>' : '') + '</li>'
  ).join('');
  document.getElementById('playlist-modal').classList.add('open');
}

function closeModal() {
  document.getElementById('playlist-modal').classList.remove('open');
  pendingPlaylist = null;
}

function submitPlaylist(mode) {
  if (!pendingPlaylist) return;
  const {guildId, tracks} = pendingPlaylist;
  closeModal();
  fetch('/api/play-playlist', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({guildId, tracks, mode})
  })
  .then(r => r.json())
  .then(d => {
    if (d.ok) {
      document.getElementById('search-input').value = '';
      const msg = document.getElementById('search-msg');
      msg.style.display = 'block';
      msg.style.color = '#4ade80';
      msg.textContent = 'Playlist adicionada (' + mode + ')';
      setTimeout(() => { msg.style.display = 'none'; }, 3000);
      refresh();
    }
  })
  .catch(() => {});
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
