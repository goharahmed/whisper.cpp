package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"time"

	// Package imports
	whisper "github.com/ggerganov/whisper.cpp/bindings/go/pkg/whisper"
	wav "github.com/go-audio/wav"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{}

func PCMUToFloat32(pcmu []byte) ([]float32, error) {
	// Determine the number of samples in the PCM stream.
	numSamples := len(pcmu)

	// Allocate space for the decoded PCM samples.
	pcm := make([]int16, numSamples)

	// Decode the PCMU stream to PCM format.
	for i := 0; i < numSamples; i++ {
		// Convert the PCMU sample to a signed 8-bit integer.
		sample := int16(pcmu[i]) - 128

		// Convert the signed 8-bit integer to a signed 16-bit integer.
		pcm[i] = sample << 8
	}

	// Convert the PCM samples to float32 format.
	floats := make([]float32, numSamples/2)
	for i := 0; i < numSamples/2; i++ {
		// Convert the two bytes at index i*2 and i*2+1 to a signed 16-bit integer.
		sampleBytes := pcm[i*2 : (i+1)*2]
		sample := int16(binary.LittleEndian.Uint16([]byte{byte(sampleBytes[0]), byte(sampleBytes[1])}))

		// Convert the signed 16-bit integer to a float32 in the range [-1, 1].
		if sample > 0 {
			floats[i] = float32(sample) / float32(math.Pow(2, 15))
		} else {
			floats[i] = -float32(sample) / float32(math.Pow(2, 15))
		}
	}

	// Return the converted audio data.
	if len(floats) == 0 {
		return nil, errors.New("no audio data found")
	}
	return floats, nil
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgrade:", err)
		return
	}
	defer conn.Close()
	// Load model
	model, err := whisper.New(flags.GetModel())

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer model.Close()
	perr := ProcessStream(model, flags, conn)
	if perr != nil {
		WriteResponse(w, 400, perr.Error())
		return
	}
	WriteResponse(w, 200, "OK")
}

// WriteResponse - HTTP reply writer
func WriteResponse(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	w.Write([]byte(message))
}

func ProcessStream(model whisper.Model, flags *Flags, conn *websocket.Conn) error {
	// Create processing context
	context, err := model.NewContext()
	if err != nil {
		return err
	}

	// Set the parameters
	if err := flags.SetParams(context); err != nil {
		return err
	}

	fmt.Printf("\n%s\n", context.SystemInfo())

	// Segment callback when -tokens is specified
	var cb whisper.SegmentCallback
	if flags.IsTokens() {
		cb = func(segment whisper.Segment) {
			fmt.Fprintf(flags.Output(), "%02d [%6s->%6s] ", segment.Num, segment.Start.Truncate(time.Millisecond), segment.End.Truncate(time.Millisecond))
			for _, token := range segment.Tokens {
				if flags.IsColorize() && context.IsText(token) {
					fmt.Fprint(flags.Output(), Colorize(token.Text, int(token.P*24.0)), " ")
				} else {
					fmt.Fprint(flags.Output(), token.Text, " ")
				}
				if conn != nil {
					conn.WriteMessage(websocket.TextMessage, []byte(token.Text))
				}
			}
			fmt.Fprintln(flags.Output(), "")
			fmt.Fprintln(flags.Output(), "")
		}
	}
	// Process the data
	context.ResetTimings()
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Println("read:", err)
			break
		}
		//log.Printf("recv: %s", message)
		data, e := PCMUToFloat32(message)
		if e != nil {
			log.Println("error converting []byte into float32!", e)
		} else {
			if err := context.Process(data, cb); err != nil {
				return err
			}
		}
	}

	context.PrintTimings()

	// Print out the results
	switch {
	case flags.GetOut() == "srt":
		return OutputSRT(os.Stdout, context)
	case flags.GetOut() == "none":
		return nil
	default:
		return Output(os.Stdout, context, flags.IsColorize())
	}
}

func Process(model whisper.Model, path string, flags *Flags) error {
	var data []float32

	// Create processing context
	context, err := model.NewContext()
	if err != nil {
		return err
	}

	// Set the parameters
	if err := flags.SetParams(context); err != nil {
		return err
	}

	fmt.Printf("\n%s\n", context.SystemInfo())

	// Open the file
	fmt.Fprintf(flags.Output(), "Loading %q\n", path)
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()

	// Decode the WAV file - load the full buffer
	dec := wav.NewDecoder(fh)
	if buf, err := dec.FullPCMBuffer(); err != nil {
		return err
	} else if dec.SampleRate != whisper.SampleRate {
		return fmt.Errorf("unsupported sample rate: %d", dec.SampleRate)
	} else if dec.NumChans != 1 {
		return fmt.Errorf("unsupported number of channels: %d", dec.NumChans)
	} else {
		data = buf.AsFloat32Buffer().Data
	}

	// Segment callback when -tokens is specified
	var cb whisper.SegmentCallback
	if flags.IsTokens() {
		cb = func(segment whisper.Segment) {
			fmt.Fprintf(flags.Output(), "%02d [%6s->%6s] ", segment.Num, segment.Start.Truncate(time.Millisecond), segment.End.Truncate(time.Millisecond))
			for _, token := range segment.Tokens {
				if flags.IsColorize() && context.IsText(token) {
					fmt.Fprint(flags.Output(), Colorize(token.Text, int(token.P*24.0)), " ")
				} else {
					fmt.Fprint(flags.Output(), token.Text, " ")
				}
			}
			fmt.Fprintln(flags.Output(), "")
			fmt.Fprintln(flags.Output(), "")
		}
	}

	// Process the data
	fmt.Fprintf(flags.Output(), "  ...processing %q\n", path)
	context.ResetTimings()
	if err := context.Process(data, cb); err != nil {
		return err
	}

	context.PrintTimings()

	// Print out the results
	switch {
	case flags.GetOut() == "srt":
		return OutputSRT(os.Stdout, context)
	case flags.GetOut() == "none":
		return nil
	default:
		return Output(os.Stdout, context, flags.IsColorize())
	}
}

// Output text as SRT file
func OutputSRT(w io.Writer, context whisper.Context) error {
	n := 1
	for {
		segment, err := context.NextSegment()
		if err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		fmt.Fprintln(w, n)
		fmt.Fprintln(w, srtTimestamp(segment.Start), " --> ", srtTimestamp(segment.End))
		fmt.Fprintln(w, segment.Text)
		fmt.Fprintln(w, "")
		n++
	}
}

// Output text to terminal
func Output(w io.Writer, context whisper.Context, colorize bool) error {
	for {
		segment, err := context.NextSegment()
		if err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		fmt.Fprintf(w, "[%6s->%6s]", segment.Start.Truncate(time.Millisecond), segment.End.Truncate(time.Millisecond))
		if colorize {
			for _, token := range segment.Tokens {
				if !context.IsText(token) {
					continue
				}
				fmt.Fprint(w, " ", Colorize(token.Text, int(token.P*24.0)))
			}
			fmt.Fprint(w, "\n")
		} else {
			fmt.Fprintln(w, " ", segment.Text)
		}
	}
}

// Return srtTimestamp
func srtTimestamp(t time.Duration) string {
	return fmt.Sprintf("%02d:%02d:%02d,%03d", t/time.Hour, (t%time.Hour)/time.Minute, (t%time.Minute)/time.Second, (t%time.Second)/time.Millisecond)
}
