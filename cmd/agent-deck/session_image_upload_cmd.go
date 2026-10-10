package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

func runSessionImageUpload(profile string, args []string, input io.Reader, output io.Writer) error {
	fs := flag.NewFlagSet("session image-upload", flag.ContinueOnError)
	fs.SetOutput(helpOutput(args, output, os.Stderr))
	name := fs.String("name", "", "Attachment filename (png, jpg, gif, webp or pdf)")
	jsonOutput := fs.Bool("json", false, "Output path and byte count as JSON")
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	if fs.NArg() != 1 || *name == "" {
		return fmt.Errorf("usage: agent-deck session image-upload <id|title> --name <uuid>.png --json (bytes on stdin, max 20 MiB)")
	}
	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		return err
	}
	defer storage.Close()
	inst, message, _ := ResolveSession(fs.Arg(0), instances)
	if inst == nil {
		return fmt.Errorf("%s", message)
	}
	result, err := storage.UploadImage(inst.ID, *name, input)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(output).Encode(result)
	}
	_, err = fmt.Fprintln(output, result.Path)
	return err
}

func handleSessionImageUpload(profile string, args []string) {
	if err := runSessionImageUpload(profile, args, os.Stdin, os.Stdout); err != nil {
		// --help already printed the flag usage on stdout (the capability
		// probe reads "Usage of session image-upload:" there); it is not an
		// error.
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
