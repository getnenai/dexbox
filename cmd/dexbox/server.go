package main

import (
	"github.com/getnenai/dexbox/internal/server"
	"github.com/spf13/cobra"
)

func cmdServer() *cobra.Command {
	var listenAddr string
	var openaiBaseURL string

	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the dexbox API server",
		Long:  `Starts the Go-based server that handles API requests for the dexbox CLI.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return server.Run(server.Options{
				ListenAddr:    listenAddr,
				OpenAIBaseURL: openaiBaseURL,
			})
		},
	}

	cmd.Flags().StringVar(&listenAddr, "listen", ":8600", "Address to listen on")

	return cmd
}
