module pizza-bot

go 1.22.0

toolchain go1.22.4

require (
	github.com/bwmarrin/discordgo v0.29.1-0.20260214123928-f43dd94faaac
	github.com/joho/godotenv v1.5.1
)

require (
	github.com/cloudflare/circl v1.6.3 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	golang.org/x/crypto v0.32.0 // indirect
	golang.org/x/sys v0.29.0 // indirect
)

replace github.com/bwmarrin/discordgo => github.com/yeongaori/discordgo-fork v0.0.0-20260326072433-16ef34198ced
