//+build !windows

package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"github.com/creack/pty"
	"github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// Config ...
type Config struct {
	Port             uint
	Address          string
	IdleTimeout      time.Duration
	ExecutableBinary string
}

// Server ...
type Server struct {
	port             uint
	address          string
	idleTimeout      time.Duration
	executableBinary string
	sshServer        *ssh.Server
}

// NewServer ...
func NewServer(config *Config) *Server {
	return &Server{
		port:             config.Port,
		address:          config.Address,
		idleTimeout:      config.IdleTimeout,
		executableBinary: config.ExecutableBinary,
	}
}

// ListenAndServe ...
func (s *Server) ListenAndServe() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	s.sshServer = &ssh.Server{
		Addr:        fmt.Sprintf("%s:%v", s.address, s.port),
		IdleTimeout: s.idleTimeout,
		Handler: func(sshSession ssh.Session) {
			ptyReq, winCh, isPty := sshSession.Pty()
			if !isPty {
				io.WriteString(sshSession, "Error: Non-interactive terminals are not supported")
				sshSession.Exit(1)
				return
			}

			configPath, err := createTempConfig()
			if err != nil {
				fmt.Println(err)
				return
			}

			cmdCtx, cancelCmd := context.WithCancel(sshSession.Context())
			defer cancelCmd()

			cmd := exec.CommandContext(cmdCtx, s.executableBinary, "--config", configPath)
			cmd.Env = append(sshSession.Environ(), fmt.Sprintf("TERM=%s", ptyReq.Term))

			f, err := pty.Start(cmd)
			if err != nil {
				io.WriteString(sshSession, err.Error())
			}

			defer f.Close()

			go func() {
				for win := range winCh {
					setWinsize(f, win.Width, win.Height)
				}
			}()

			go func() {
				io.Copy(f, sshSession)
			}()

			io.Copy(sshSession, f)
			f.Close()
			cmd.Wait()
			os.Remove(configPath)
		},
		PtyCallback: func(ctx ssh.Context, pty ssh.Pty) bool {
			// TODO: check public key hash
			return true
		},
		PublicKeyHandler: func(ctx ssh.Context, key ssh.PublicKey) bool {
			return true
		},
		PasswordHandler: func(ctx ssh.Context, password string) bool {
			return true
		},
		KeyboardInteractiveHandler: func(ctx ssh.Context, challenger gossh.KeyboardInteractiveChallenge) bool {
			return true
		},
	}

	hostKeyFile := path.Join(homeDir, ".ssh", "id_rsa")
	if _, err := os.Stat(hostKeyFile); os.IsNotExist(err) {
		return errors.New("SSH key is required to start server")
	}

	err = s.sshServer.SetOption(ssh.HostKeyFile(hostKeyFile))
	if err != nil {
		return err
	}

	return s.sshServer.ListenAndServe()
}

// Shutdown ...
func (s *Server) Shutdown() {
	s.sshServer.Close()
}

// setWinsize ...
func setWinsize(f *os.File, w, h int) {
	syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCSWINSZ),
		uintptr(unsafe.Pointer(&struct{ h, w, x, y uint16 }{uint16(h), uint16(w), 0, 0})))
}

// createTempConfig ...
// TODO: load saved configuration based on ssh public key hash
func createTempConfig() (string, error) {
	f, err := ioutil.TempFile("", "config")
	if err != nil {
		return "", err
	}

	f.Close()
	return filepath.Clean(f.Name()), nil
}