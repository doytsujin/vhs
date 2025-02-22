package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/caarlos0/env/v6"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/logging"
	"github.com/gliderlabs/ssh"
	"github.com/spf13/cobra"
)

const (
	maxNumber = 1000000000
	timeout   = 30 * time.Second
)

type config struct {
	Port               int    `env:"PORT" envDefault:"1976"`
	Host               string `env:"HOST" envDefault:"localhost"`
	GID                int    `env:"GID" envDefault:"0"`
	UID                int    `env:"UID" envDefault:"0"`
	KeyPath            string `env:"KEY_PATH" envDefault:""`
	AuthorizedKeysPath string `env:"AUTHORIZED_KEYS_PATH"`
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the VHS SSH server",
	RunE: func(cmd *cobra.Command, args []string) error {
		var cfg config
		if err := env.Parse(&cfg, env.Options{
			Prefix: "VHS_",
		}); err != nil {
			return err
		}
		key := cfg.KeyPath
		if key == "" {
			key = filepath.Join(".ssh", "vhs_ed25519")
		}
		addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
		s, err := wish.NewServer(
			wish.WithAddress(addr),
			wish.WithHostKeyPath(key),
			func(s *ssh.Server) error {
				if cfg.AuthorizedKeysPath == "" {
					return nil
				}
				return wish.WithAuthorizedKeys(cfg.AuthorizedKeysPath)(s)
			},
			wish.WithMiddleware(
				func(h ssh.Handler) ssh.Handler {
					return func(s ssh.Session) {
						// Request for vhs must be passed in through stdin, which
						// implies that there is no PTY.
						//
						// In the future, we should support PTY by providing a
						// Bubble Tea interface for VHS.
						//
						// Ideally, users can SSH into the server and get a
						// walk through of how to write a .tape file.
						_, _, isPty := s.Pty()
						if isPty {
							wish.Println(s, "PTY is not supported")
							_ = s.Exit(1)
							return
						}

						// Read stdin passed from the client.
						// This is the .tape file which contains the VHS commands.
						//
						// ssh vhs.charm.sh < demo.tape
						var b bytes.Buffer
						_, err := io.Copy(&b, s)
						if err != nil {
							wish.Errorln(s, err)
							_ = s.Exit(1)
							return
						}

						//nolint:gosec
						rand := rand.Int63n(maxNumber)
						tempFile := filepath.Join(os.TempDir(), fmt.Sprintf("vhs-%d.gif", rand))

						err = Evaluate(cmd.Context(), b.String(), s.Stderr(), func(v *VHS) {
							v.Options.Video.Output.GIF = tempFile
							// Disable generating MP4 & WebM.
							v.Options.Video.Output.MP4 = ""
							v.Options.Video.Output.WebM = ""
						})
						if err != nil {
							_ = s.Exit(1)
						}

						gif, _ := os.ReadFile(tempFile)
						wish.Print(s, string(gif))
						_ = os.Remove(tempFile)

						h(s)
					}
				},
				logging.Middleware(),
			),
		)
		if err != nil {
			log.Fatalln(err)
		}

		log.Printf("Starting SSH server on %s", addr)
		go func() {
			ls, err := net.Listen("tcp", addr)
			if err != nil {
				log.Fatalf("Failed to listen on %s: %v", addr, err)
			}
			gid, uid := cfg.GID, cfg.UID
			if gid != 0 && uid != 0 {
				log.Printf("Starting server with GID: %d, UID: %d", gid, uid)
				if err := dropUserPrivileges(gid, uid); err != nil {
					log.Fatalln(err)
				}
			}
			if err = s.Serve(ls); err != nil {
				log.Fatalln(err)
			}
		}()

		<-cmd.Context().Done()
		log.Println("Stopping SSH server")
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer func() { cancel() }()
		return s.Shutdown(ctx)
	},
}
