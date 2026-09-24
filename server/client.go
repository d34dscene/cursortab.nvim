package main

import (
	"io"
	"os"
)

// Client is the stdio relay between Neovim and the daemon socket. Daemon
// lifecycle (spawn, config reload, restart) belongs to the Lua client, which
// starts the daemon itself before launching this relay.
type Client struct {
	stateDir string
}

func NewClient(stateDir string) *Client {
	return &Client{
		stateDir: stateDir,
	}
}

func (c *Client) Connect() error {
	conn, err := dialIPC(c.stateDir)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Relay between stdin/stdout and socket
	go func() {
		io.Copy(conn, os.Stdin)
		conn.Close()
	}()

	io.Copy(os.Stdout, conn)
	return nil
}
