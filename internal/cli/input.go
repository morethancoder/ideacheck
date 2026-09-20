package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// input describes where the idea comes from. Interactive means: no idea was
// given and a human is at the terminal, so open the form.
type input struct {
	Data        []byte
	Interactive bool
}

type inputEnv struct {
	Stdin      io.Reader
	StdinPiped bool
	ReadFile   func(string) ([]byte, error)
	IsFile     func(string) bool
}

func osInputEnv() inputEnv {
	st, err := os.Stdin.Stat()
	return inputEnv{
		Stdin:      os.Stdin,
		StdinPiped: err == nil && st.Mode()&os.ModeCharDevice == 0,
		ReadFile:   os.ReadFile,
		IsFile: func(p string) bool {
			fi, err := os.Stat(p)
			return err == nil && fi.Mode().IsRegular()
		},
	}
}

// resolveInput applies the documented order: "-" → stdin; -f or a file → file;
// piped stdin with no arg → stdin; otherwise the args joined are the idea text.
func resolveInput(args []string, fileFlag string, env inputEnv) (input, error) {
	switch {
	case len(args) == 1 && args[0] == "-":
		return readAll(env.Stdin, "stdin")
	case fileFlag != "":
		return readFile(env, fileFlag)
	case len(args) == 1 && env.IsFile(args[0]):
		return readFile(env, args[0])
	case len(args) == 0 && env.StdinPiped:
		return readAll(env.Stdin, "stdin")
	case len(args) == 0:
		return input{Interactive: true}, nil
	}
	return input{Data: []byte(strings.Join(args, " "))}, nil
}

func readFile(env inputEnv, path string) (input, error) {
	b, err := env.ReadFile(path)
	if err != nil {
		return input{}, fmt.Errorf("read idea file: %w", err)
	}
	return input{Data: b}, nil
}

func readAll(r io.Reader, what string) (input, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return input{}, fmt.Errorf("read %s: %w", what, err)
	}
	return input{Data: b}, nil
}
