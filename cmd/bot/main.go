// Command bot starts the default MagiBox host.
package main

import (
	"log"

	"github.com/moyoez/magibox/pkg/magibox"
)

func main() {
	if err := magibox.Run(); err != nil {
		log.Fatal(err)
	}
}
