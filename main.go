package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	token := os.Getenv("DISCORD_TOKEN")
	appID := os.Getenv("DISCORD_APP_ID")
	guildID := os.Getenv("DISCORD_GUILD_ID") // optional, empty = global commands

	if token == "" || appID == "" {
		log.Fatal("DISCORD_TOKEN and DISCORD_APP_ID must be set in environment")
	}

	sess, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalf("error creating Discord session: %v", err)
	}

	// Need guilds to read voice states
	sess.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildVoiceStates

	sess.AddHandler(handleInteraction)

	if err := sess.Open(); err != nil {
		log.Fatalf("error opening Discord connection: %v", err)
	}
	defer sess.Close()

	log.Println("Registering slash commands...")
	if err := registerCommands(sess, appID, guildID); err != nil {
		log.Fatalf("error registering commands: %v", err)
	}

	log.Println("Bot is running. Press CTRL+C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
	log.Println("Shutting down...")
}
