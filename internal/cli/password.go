package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// A password is the one thing these commands must not get wrong twice over: written
// into a database as anything but a hash it is an account anybody can read, and asked
// for in a way that echoes it it is a password in the scrollback of whoever typed it.

// readPassword returns the password for a new account. It comes from -password when
// that was given; otherwise it comes from stdin, which is a prompt with the echo off in
// a terminal and one line in a pipe. A password is asked for twice in a terminal
// because the alternative is an account the operator just created and cannot open.
func readPassword(flagValue string, inv Invocation, label string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if isTerminal(inv.stdin) {
		first, err := readHidden(inv, label+": ")
		if err != nil {
			return "", err
		}
		second, err := readHidden(inv, "repeat it: ")
		if err != nil {
			return "", err
		}
		if first != second {
			return "", errors.New("the two passwords are not the same, so no account was created")
		}
		return first, nil
	}
	line, err := readLine(inv)
	if err != nil {
		return "", err
	}
	if line == "" {
		return "", errors.New("no password: pass -password, write one to stdin, or run this in a terminal to type it")
	}
	return line, nil
}

// readHidden asks for one line with the echo off, and puts the newline the terminal
// swallowed back where the next prompt starts.
func readHidden(inv Invocation, prompt string) (string, error) {
	file, ok := inv.stdin.(*os.File)
	if !ok {
		return "", errors.New("cannot ask for a password: standard input is not a terminal")
	}
	sayText(inv.out, prompt)
	// term.ReadPassword puts the descriptor into a mode without echo and without
	// canonical processing, reads until a newline and restores the mode on every exit
	// path, including a signal-interrupted read.
	value, err := term.ReadPassword(int(file.Fd()))
	sayLine(inv.out)
	if err != nil {
		return "", fmt.Errorf("reading the password: %w", err)
	}
	return strings.TrimSpace(string(value)), nil
}

// readLine reads one line of input. A stdin that holds nothing is an empty answer rather
// than a read error: the caller decides what nothing means, which is a refused password in
// one command and an unconfirmed removal in another.
func readLine(inv Invocation) (string, error) {
	reader := bufio.NewReader(inv.stdin)
	line, err := reader.ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("reading input: %w", err)
	}
	return line, nil
}

// termIsTerminal is a variable rather than a call so the terminal-only paths can be
// named in a test without pretending to have a terminal.
var termIsTerminal = func(fd uintptr) bool {
	return term.IsTerminal(int(fd))
}
