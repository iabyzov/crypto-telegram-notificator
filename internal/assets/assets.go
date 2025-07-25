package assets

import (
	"log"
	"os"
)

func Handler() {
	cmcAPIKey := os.Getenv("CMC_API_KEY")
	if cmcAPIKey == "" {
		log.Fatal("CMC_API_KEY environment variable not set")
	}

	_, err := NewServer(cmcAPIKey)
	if err != nil {
		log.Fatal(err)
	}

	//server.Start()
}
