package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Track holds the user-supplied query, a display title, and the resolved YouTube URL.
type Track struct {
	Title string // Resolved YouTube title; falls back to Query until resolved
	Query string // Original user search query or URL
	URL   string // Resolved YouTube watch URL (empty until resolveTrackMeta completes)
}

// GuildPlayer manages the queue and playback state for a single guild.
type GuildPlayer struct {
	mu            sync.Mutex
	queue         []Track
	current       *Track
	loop          bool
	vc            *discordgo.VoiceConnection
	running       bool
	cancelTrack   context.CancelFunc
	guildID       string            // set at creation, read-only after
	currentSpanID string            // per-track span ID for PizzaLog latency
	frameCache    map[string][][]byte // key=Query; Opus frames cached for looped tracks
}

var (
	playersMu sync.Mutex
	players   = make(map[string]*GuildPlayer)
)

// resolveTrackMeta runs yt-dlp in the background to fetch the real title and watch URL
// for the track at queue index idx. If the track has moved (was dequeued) by the time
// resolution finishes, the update is a no-op.
func resolveTrackMeta(p *GuildPlayer, idx int, query string) {
	ytQuery := query
	if !strings.HasPrefix(ytQuery, "http://") && !strings.HasPrefix(ytQuery, "https://") {
		ytQuery = "ytsearch1:" + ytQuery
	}
	out, err := exec.Command("yt-dlp",
		"--no-playlist", "--skip-download",
		"--print", "title",
		"--print", "webpage_url",
		"--extractor-args", "youtube:player_client=tv_embedded,mweb",
		ytQuery,
	).Output()
	if err != nil {
		return
	}
	lines := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)
	if len(lines) < 2 {
		return
	}
	title := strings.TrimSpace(lines[0])
	url := strings.TrimSpace(lines[1])
	if title == "" || url == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < len(p.queue) && p.queue[idx].Query == query {
		p.queue[idx].Title = title
		p.queue[idx].URL = url
	} else if p.current != nil && p.current.Query == query {
		// Track was already dequeued and is now playing — update current directly.
		p.current.Title = title
		p.current.URL = url
	}
}

func getOrCreatePlayer(guildID string) *GuildPlayer {
	playersMu.Lock()
	defer playersMu.Unlock()
	if p, ok := players[guildID]; ok {
		return p
	}
	p := &GuildPlayer{guildID: guildID}
	players[guildID] = p
	return p
}

// playLoop dequeues and plays tracks until the queue is empty, then disconnects.
func (p *GuildPlayer) playLoop() {
	for {
		p.mu.Lock()
		if len(p.queue) == 0 {
			p.current = nil
			p.running = false
			p.frameCache = nil
			vc := p.vc
			p.vc = nil
			p.mu.Unlock()
			Log("INFO", "Queue empty, disconnecting", map[string]string{"guild": p.guildID})
			if vc != nil {
				vc.Disconnect(context.Background())
			}
			return
		}
		track := p.queue[0]
		p.queue = p.queue[1:]
		p.current = &track
		spanID := NewSpanID()
		p.currentSpanID = spanID
		p.mu.Unlock()

		LogTrace("INFO", "Track started", map[string]string{"title": track.Title, "guild": p.guildID}, "", spanID, "")

		skipped := p.playTrack(track)

		LogTrace("INFO", "Track finished", map[string]string{"title": track.Title, "guild": p.guildID, "skipped": fmt.Sprintf("%v", skipped)}, "", spanID, "")

		// If the track ended naturally and loop is on, push it back to the front.
		if !skipped {
			p.mu.Lock()
			if p.loop {
				p.queue = append([]Track{track}, p.queue...)
			}
			p.mu.Unlock()
		}
	}
}

// playCached sends pre-buffered Opus frames directly to Discord, bypassing yt-dlp/ffmpeg.
// Returns true if skipped, false if played to completion.
func (p *GuildPlayer) playCached(track Track, frames [][]byte) (skipped bool) {
	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.cancelTrack = cancel
	vc := p.vc
	p.mu.Unlock()
	defer cancel()

	if vc == nil {
		return false
	}

	Log("INFO", "Playing from cache", map[string]string{"title": track.Title, "guild": p.guildID})
	vc.Speaking(true)           //nolint:errcheck
	defer vc.Speaking(false)    //nolint:errcheck

	for _, frame := range frames {
		select {
		case <-ctx.Done():
			return true
		case vc.OpusSend <- frame:
		}
	}
	return false
}

// playTrack streams one track through the yt-dlp → ffmpeg → Ogg → Discord pipeline.
// If the track is cached (from a previous loop iteration), playCached is used instead.
// Returns true if the track was skipped (context cancelled), false if it ended naturally.
func (p *GuildPlayer) playTrack(track Track) (skipped bool) {
	// Use cached frames if available (populated on previous loop iteration).
	p.mu.Lock()
	cacheKey := track.Query
	cachedFrames, hasCached := p.frameCache[cacheKey]
	shouldCache := p.loop && !hasCached
	p.mu.Unlock()

	if hasCached {
		return p.playCached(track, cachedFrames)
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.cancelTrack = cancel
	p.mu.Unlock()
	defer cancel()

	// Treat plain-text queries as YouTube searches.
	query := track.Query
	if !strings.HasPrefix(query, "http://") && !strings.HasPrefix(query, "https://") {
		query = "ytsearch1:" + query
	}

	// yt-dlp extracts the best audio and writes raw bytes to stdout.
	// tv_embedded+mweb avoids both the SABR 403s from the default web client
	// and the GVS PO Token requirement that ios/android now enforce.
	dlp := exec.CommandContext(ctx, "yt-dlp",
		"--no-playlist", "-x",
		"-f", "bestaudio/best",
		"--extractor-args", "youtube:player_client=tv_embedded,mweb",
		"-o", "-",
		query,
	)
	var dlpStderr bytes.Buffer
	dlp.Stderr = &dlpStderr

	// ffmpeg re-encodes to Ogg-wrapped Opus at 48kHz stereo with 20ms frames.
	ffm := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-i", "pipe:0",
		"-vn",
		"-c:a", "libopus",
		"-ar", "48000",
		"-ac", "2",
		"-b:a", "96k",
		"-frame_duration", "20",
		"-f", "ogg",
		"pipe:1",
	)
	var ffmStderr bytes.Buffer
	ffm.Stderr = &ffmStderr

	dlpOut, err := dlp.StdoutPipe()
	if err != nil {
		log.Println("yt-dlp pipe error:", err)
		Log("ERROR", "yt-dlp pipe error", map[string]string{"error": err.Error(), "title": track.Title})
		return false
	}

	ffm.Stdin = dlpOut

	ffmOut, err := ffm.StdoutPipe()
	if err != nil {
		log.Println("ffmpeg pipe error:", err)
		Log("ERROR", "ffmpeg pipe error", map[string]string{"error": err.Error(), "title": track.Title})
		return false
	}

	if err := dlp.Start(); err != nil {
		log.Println("yt-dlp start error:", err)
		Log("ERROR", "yt-dlp start error", map[string]string{"error": err.Error(), "title": track.Title})
		return false
	}
	if err := ffm.Start(); err != nil {
		log.Println("ffmpeg start error:", err)
		Log("ERROR", "ffmpeg start error", map[string]string{"error": err.Error(), "title": track.Title})
		dlp.Process.Kill()
		return false
	}

	ogg := newOggReader(ffmOut)

	// Skip the two Ogg/Opus header packets (ID header + comment header).
	if _, err := ogg.nextPacket(); err != nil {
		dlpErr := strings.TrimSpace(dlpStderr.String())
		ffmErr := strings.TrimSpace(ffmStderr.String())
		log.Printf("ogg header read error: %v | yt-dlp: %s | ffmpeg: %s", err, dlpErr, ffmErr)
		Log("ERROR", "ogg header read error", map[string]string{"error": err.Error(), "title": track.Title, "yt-dlp": dlpErr, "ffmpeg": ffmErr})
		return false
	}
	if _, err := ogg.nextPacket(); err != nil {
		log.Println("ogg comment read error:", err)
		Log("ERROR", "ogg comment read error", map[string]string{"error": err.Error(), "title": track.Title})
		return false
	}

	// 250 frames × 20ms = 5 seconds of pre-buffered audio to absorb network jitter.
	const bufferFrames = 250
	bufferCh := make(chan []byte, bufferFrames)
	readerDone := make(chan struct{})

	// Reader goroutine: decode Ogg packets and push into the buffer channel.
	go func() {
		defer close(readerDone)
		defer close(bufferCh)
		for {
			packet, err := ogg.nextPacket()
			if err != nil {
				return
			}
			select {
			case bufferCh <- packet:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Block until the buffer holds 5 seconds of audio or the track is shorter.
bufferWait:
	for len(bufferCh) < bufferFrames {
		select {
		case <-ctx.Done():
			<-readerDone
			dlp.Wait() //nolint:errcheck
			ffm.Wait() //nolint:errcheck
			return true
		case <-readerDone:
			break bufferWait
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	p.mu.Lock()
	vc := p.vc
	p.mu.Unlock()

	if vc == nil {
		<-readerDone
		return false
	}

	vc.Speaking(true)           //nolint:errcheck
	defer vc.Speaking(false)    //nolint:errcheck

	// Drain the buffer channel into Discord's Opus sender.
	// If shouldCache is set, accumulate frames so the next loop iteration skips yt-dlp.
	var savedFrames [][]byte
	for {
		select {
		case <-ctx.Done():
			<-readerDone
			dlp.Wait() //nolint:errcheck
			ffm.Wait() //nolint:errcheck
			return true
		case frame, ok := <-bufferCh:
			if !ok {
				// Reader finished and buffer is exhausted — track ended naturally.
				dlp.Wait() //nolint:errcheck
				ffm.Wait() //nolint:errcheck
				if shouldCache {
					p.mu.Lock()
					if p.frameCache == nil {
						p.frameCache = make(map[string][][]byte)
					}
					p.frameCache[cacheKey] = savedFrames
					p.mu.Unlock()
					Log("INFO", "Track cached for loop", map[string]string{"title": track.Title, "guild": p.guildID})
				}
				return false
			}
			if shouldCache {
				saved := make([]byte, len(frame))
				copy(saved, frame)
				savedFrames = append(savedFrames, saved)
			}
			select {
			case vc.OpusSend <- frame:
			case <-ctx.Done():
				<-readerDone
				dlp.Wait() //nolint:errcheck
				ffm.Wait() //nolint:errcheck
				return true
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Minimal Ogg bitstream reader (RFC 3533) — pure Go, no CGO.
// Supports a single logical bitstream (the common case for ffmpeg output).
// ---------------------------------------------------------------------------

type oggReader struct {
	r       io.Reader
	pending [][]byte // complete packets waiting to be returned
	partial []byte   // bytes of the in-progress packet (lacing continuation)
}

func newOggReader(r io.Reader) *oggReader {
	return &oggReader{r: r}
}

// nextPacket returns the next complete Opus packet from the Ogg stream.
func (o *oggReader) nextPacket() ([]byte, error) {
	for len(o.pending) == 0 {
		if err := o.readPage(); err != nil {
			return nil, err
		}
	}
	pkt := o.pending[0]
	o.pending = o.pending[1:]
	return pkt, nil
}

// readPage reads one Ogg page and appends any completed packets to o.pending.
func (o *oggReader) readPage() error {
	// Capture pattern "OggS"
	var magic [4]byte
	if _, err := io.ReadFull(o.r, magic[:]); err != nil {
		return err
	}
	if magic != [4]byte{'O', 'g', 'g', 'S'} {
		return fmt.Errorf("ogg: invalid capture pattern %q", magic)
	}

	// version(1) + header_type(1) + granule_pos(8) + serial(4) + seqno(4) + checksum(4) = 22 bytes
	var hdr [22]byte
	if _, err := io.ReadFull(o.r, hdr[:]); err != nil {
		return err
	}

	// Number of segments
	var nsegBuf [1]byte
	if _, err := io.ReadFull(o.r, nsegBuf[:]); err != nil {
		return err
	}
	nseg := int(nsegBuf[0])

	// Segment table
	segTable := make([]byte, nseg)
	if _, err := io.ReadFull(o.r, segTable); err != nil {
		return err
	}

	// Total page data size
	total := 0
	for _, s := range segTable {
		total += int(s)
	}
	data := make([]byte, total)
	if _, err := io.ReadFull(o.r, data); err != nil {
		return err
	}

	// Reconstruct packets using lace values.
	// A segment of 255 means the packet continues into the next segment (or page).
	// A segment < 255 terminates the current packet.
	offset := 0
	for _, seg := range segTable {
		o.partial = append(o.partial, data[offset:offset+int(seg)]...)
		offset += int(seg)
		if seg < 255 {
			pkt := make([]byte, len(o.partial))
			copy(pkt, o.partial)
			o.pending = append(o.pending, pkt)
			o.partial = nil
		}
	}
	// If partial is non-nil after all segments, the packet continues in the next page.

	return nil
}

