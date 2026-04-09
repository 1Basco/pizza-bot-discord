package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

var slashCommands = []*discordgo.ApplicationCommand{
	{
		Name:        "play",
		Description: "Play a song from YouTube (name or URL)",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "query",
				Description: "Song name or YouTube URL",
				Required:    true,
			},
		},
	},
	{
		Name:        "loop",
		Description: "Toggle loop for the current track",
	},
	{
		Name:        "next",
		Description: "Skip to the next track in queue",
	},
	{
		Name:        "playlist",
		Description: "Show the current queue",
	},
	{
		Name:        "stop",
		Description: "Stop playback, clear queue, and disconnect",
	},
}

func registerCommands(s *discordgo.Session, appID, guildID string) error {
	for _, cmd := range slashCommands {
		if _, err := s.ApplicationCommandCreate(appID, guildID, cmd); err != nil {
			return fmt.Errorf("registering /%s: %w", cmd.Name, err)
		}
	}
	return nil
}

func handleInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type != discordgo.InteractionApplicationCommand {
		return
	}
	switch i.ApplicationCommandData().Name {
	case "play":
		handlePlay(s, i)
	case "loop":
		handleLoop(s, i)
	case "next":
		handleNext(s, i)
	case "playlist":
		handlePlaylist(s, i)
	case "stop":
		handleStop(s, i)
	}
}

// respond sends an ephemeral reply to a slash command interaction.
func respond(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: msg,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

// deferReply sends Discord an immediate "thinking..." acknowledgement so the
// bot has up to 15 minutes to follow up, avoiding the 3-second timeout.
func deferReply(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})
}

// editReply updates the deferred (or existing) ephemeral response.
func editReply(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) {
	_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &msg,
	})
}

func handlePlay(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Acknowledge immediately — joining voice can exceed the 3-second limit.
	deferReply(s, i)

	query := i.ApplicationCommandData().Options[0].StringValue()

	// Resolve the invoker's current voice channel.
	vs, err := s.State.VoiceState(i.GuildID, i.Member.User.ID)
	if err != nil || vs.ChannelID == "" {
		editReply(s, i, "You must be in a voice channel to use this command.")
		return
	}

	p := getOrCreatePlayer(i.GuildID)

	// Join voice channel if not already connected.
	// Note: the lock is released before ChannelVoiceJoin to avoid blocking the
	// Discord event loop; a concurrent /play call is harmless here.
	p.mu.Lock()
	needJoin := p.vc == nil
	p.mu.Unlock()

	if needJoin {
		vc, err := s.ChannelVoiceJoin(context.Background(), i.GuildID, vs.ChannelID, false, true)
		if err != nil {
			editReply(s, i, "Failed to join your voice channel.")
			return
		}
		p.mu.Lock()
		p.vc = vc
		p.mu.Unlock()
		Log("INFO", "Joined voice channel", map[string]string{"guild": i.GuildID, "channel": vs.ChannelID})
	}

	track := Track{Title: query, Query: query}

	p.mu.Lock()
	p.queue = append(p.queue, track)
	if !p.running {
		p.running = true
		go p.playLoop()
	}
	p.mu.Unlock()

	editReply(s, i, fmt.Sprintf("Queued: **%s**", query))
	Log("INFO", "Track queued", map[string]string{"title": query, "guild": i.GuildID})
}

func handleLoop(s *discordgo.Session, i *discordgo.InteractionCreate) {
	p := getOrCreatePlayer(i.GuildID)
	p.mu.Lock()
	p.loop = !p.loop
	loopOn := p.loop
	p.mu.Unlock()

	if loopOn {
		respond(s, i, "Loop **enabled**.")
	} else {
		respond(s, i, "Loop **disabled**.")
	}
	Log("INFO", "Loop toggled", map[string]string{"guild": i.GuildID, "loop": fmt.Sprintf("%v", loopOn)})
}

func handleNext(s *discordgo.Session, i *discordgo.InteractionCreate) {
	p := getOrCreatePlayer(i.GuildID)
	p.mu.Lock()
	cancel := p.cancelTrack
	p.mu.Unlock()

	if cancel == nil {
		respond(s, i, "Nothing is playing.")
		return
	}
	cancel()
	Log("INFO", "Track skipped", map[string]string{"guild": i.GuildID})
	respond(s, i, "Skipped.")
}

func handlePlaylist(s *discordgo.Session, i *discordgo.InteractionCreate) {
	p := getOrCreatePlayer(i.GuildID)
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.current == nil && len(p.queue) == 0 {
		respond(s, i, "The queue is empty.")
		return
	}

	var sb strings.Builder
	if p.current != nil {
		sb.WriteString("▶ **" + p.current.Title + "**")
		if p.loop {
			sb.WriteString(" *(looping)*")
		}
		sb.WriteString("\n")
	}
	for idx, t := range p.queue {
		fmt.Fprintf(&sb, "%d. %s\n", idx+1, t.Title)
	}
	respond(s, i, sb.String())
}

func handleStop(s *discordgo.Session, i *discordgo.InteractionCreate) {
	p := getOrCreatePlayer(i.GuildID)
	p.mu.Lock()
	p.queue = nil
	p.loop = false
	cancel := p.cancelTrack
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	Log("INFO", "Stop command issued", map[string]string{"guild": i.GuildID})
	respond(s, i, "Stopped and cleared the queue.")
}
