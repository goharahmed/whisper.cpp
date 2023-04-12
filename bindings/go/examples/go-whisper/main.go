package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	// Packages
	whisper "github.com/ggerganov/whisper.cpp/bindings/go/pkg/whisper"
)

var flags *Flags
var err error

func main() {
	flags, err = NewFlags(filepath.Base(os.Args[0]), os.Args[1:])

	var filesProcessing bool
	filesProcessing = true
	if err == flag.ErrHelp {
		os.Exit(0)
	} else if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	} else if flags.GetModel() == "" {
		fmt.Fprintln(os.Stderr, "Use -model flag to specify which model file to use")
		os.Exit(1)
	} else if flags.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "No input files specified")
		filesProcessing = false
	}

	if filesProcessing {
		// Load model
		model, err := whisper.New(flags.GetModel())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer model.Close()
		// Process files
		for _, filename := range flags.Args() {
			if err := Process(model, filename, flags); err != nil {
				fmt.Fprintln(os.Stderr, err)
				continue
			}
		}
	} else {
		if flags.GetWSSSocket() == "" {
			fmt.Fprintln(os.Stderr, "Use -listen_wss flag to specify Listening interface")
			os.Exit(1)
		}
		http.HandleFunc("/", handleWebSocket)
		log.Fatal(http.ListenAndServe(flags.GetWSSSocket(), nil))
	}
}
