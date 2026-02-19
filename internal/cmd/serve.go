package cmd

import (
	"os"

	"github.com/izdrail/chief/internal/server"
)

// ServeOptions contains configuration for the serve command.
type ServeOptions struct {
	Addr     string
	BaseDir  string
	GitToken string
}

// RunServe starts the Chief API server.
func RunServe(opts ServeOptions) error {
	if opts.Addr == "" {
		opts.Addr = ":1248"
	}
	if opts.BaseDir == "" {
		opts.BaseDir, _ = os.Getwd()
	}

	srv := server.NewServer(opts.Addr, opts.BaseDir, opts.GitToken)
	return srv.Start()
}
