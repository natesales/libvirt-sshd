package main

import (
	"encoding/xml"
	"fmt"
	"github.com/gliderlabs/ssh"
	"github.com/kr/pty"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"unsafe"
)

const bindHost = ":2222"

type Domain struct {
	XMLName xml.Name `xml:"domain"`
	Name    string   `xml:"name"`
	Key     string   `xml:"description"`
	UUID    string   `xml:"uuid"`
}

func setWinsize(f *os.File, w, h int) {
	syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCSWINSZ), uintptr(unsafe.Pointer(&struct{ h, w, x, y uint16 }{uint16(h), uint16(w), 0, 0})))
}

func main() {
	ssh.Handle(func(s ssh.Session) {
		// Find VM by UUID
		files, err := filepath.Glob("/etc/libvirt/qemu/*.xml")
		if err != nil {
			panic(err)
		}

		for _, f := range files {
			xmlFile, err := os.Open(f)
			if err != nil {
				fmt.Println(err)
			}
			defer xmlFile.Close()

			byteValue, _ := ioutil.ReadAll(xmlFile)

			var domain Domain
			xml.Unmarshal(byteValue, &domain)

			if domain.UUID == s.User() {
				realKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(domain.Key))
				if err != nil || !reflect.DeepEqual(realKey.Marshal(), s.PublicKey().Marshal()) {
					io.WriteString(s, "Permission denied\n")
					s.Exit(1)
				} else {
					// io.WriteString(s, "Key matches!")
					break
				}
			}

			_, _ = io.WriteString(s, "Permission denied\n")
			_ = s.Exit(1)
		}

		cmd := exec.Command("virsh", "console", "--safe", s.User())
		ptyReq, winCh, isPty := s.Pty()
		if isPty {
			cmd.Env = append(cmd.Env, fmt.Sprintf("TERM=%s", ptyReq.Term))
			f, err := pty.Start(cmd)
			if err != nil {
				_, _ = io.WriteString(s, "unable to start PTY\n")
				_ = s.Exit(1)
				log.Fatalf("unable to start pty: %v\n", err)
			}
			go func() {
				for win := range winCh {
					setWinsize(f, win.Width, win.Height)
				}
			}()
			go func() {
				io.Copy(f, s) // stdin
			}()
			io.Copy(s, f) // stdout
			cmd.Wait()
		} else {
			_, _ = io.WriteString(s, "No PTY requested.\n")
			_ = s.Exit(1)
		}
	})

	log.Printf("Starting sshpty server on %s\n", bindHost)
	log.Fatal(ssh.ListenAndServe(bindHost, nil, ssh.PublicKeyAuth(func(ctx ssh.Context, key ssh.PublicKey) bool {
		return true // require pubkey auth
	})))
}
