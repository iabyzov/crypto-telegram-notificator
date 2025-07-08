package assets

import (
	"log"
	"os"
)

func main() {
	cmcAPIKey := os.Getenv("CMC_API_KEY")
	if cmcAPIKey == "" {
		log.Fatal("CMC_API_KEY environment variable not set")
	}

	server, err := NewServer(cmcAPIKey)
	if err != nil {
		log.Fatal(err)
	}

	server.Start()
}
