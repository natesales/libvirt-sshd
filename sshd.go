package main

import (
	"encoding/xml"
	"flag"
	"fmt"
	"github.com/creack/pty"
	"github.com/gliderlabs/ssh"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"unsafe"
)

var release = "dev" // set by the build process

// Define flags
var (
	hostKeyFile = flag.String("k", "~/.ssh/id_ed25519", "SSH host key file")
	bindHost    = flag.String("l", ":2222", "Listen <host:port>")
	verbose     = flag.Bool("v", false, "Enable verbose logging")
	virshPath   = flag.String("p", "virsh", "Path to virsh binary")
)

// libvirt domain
type Domain struct {
	XMLName xml.Name `xml:"domain"`
	Name    string   `xml:"name"`
	Keys    string   `xml:"description"`
	UUID    string   `xml:"uuid"`
}

// Check if providedKey is an authorized key
func userVerified(keys string, providedKey ssh.PublicKey) bool {
	for _, key := range strings.Split(keys, ",") {
		if *verbose {
			fmt.Printf("loading allowed key %s\n", key)
		}
		realKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key))
		// Verify key equality
		if err == nil && reflect.DeepEqual(realKey.Marshal(), providedKey.Marshal()) { // If keys match
			return true
		}
	}

	return false
}

func main() {
	flag.Usage = func() {
		fmt.Printf("Usage for libvirt-sshd (%s) https://github.com/natesales/libvirt-sshd:\n", release)
		flag.PrintDefaults()
	}

	flag.Parse()

	if *verbose {
		log.Println("Verbose logging enabled")
	}

	ssh.Handle(func(s ssh.Session) {
		if *verbose {
			log.Printf("SSH connection from %s\n", s.RemoteAddr())
		}

		// Find VM by UUID
		files, err := filepath.Glob("/etc/libvirt/qemu/*.xml")
		if err != nil {
			log.Fatalf("unable to parse qemu config file glob: %v\n", err)
		}

		if len(files) == 0 {
			log.Println("No qemu domain files found")
		}

		for _, f := range files {
			if *verbose {
				log.Printf("Connection %s trying %s\n", s.RemoteAddr(), f)
			}

			// Read libvirt XML file
			xmlFile, err := os.Open(f)
			if err != nil {
				log.Printf("xml open error: %v\n", err)
			}
			defer xmlFile.Close()

			// Parse libvirt XML file
			byteValue, _ := ioutil.ReadAll(xmlFile)
			var domain Domain
			xml.Unmarshal(byteValue, &domain)

			if domain.UUID == s.User() {
				// Convert ssh key strings to key objects
				if userVerified(domain.Keys, s.PublicKey()) {
					if *verbose {
						log.Printf("Allowing connection from %s for %s\n", s.RemoteAddr(), domain.UUID)
					}
					break
				} else { // If user not allowed
					if *verbose {
						log.Printf("Denying %s for %s\n", s.RemoteAddr(), domain.UUID)
					}
					io.WriteString(s, "Permission denied\n")
					s.Exit(1)
					break
				}
			}

			if *verbose {
				log.Printf("Connection %s UUID not found %s\n", s.RemoteAddr(), domain.UUID)
			}
			_, _ = io.WriteString(s, "Permission denied\n")
			_ = s.Exit(1)
		}

		cmd := exec.Command(*virshPath, "console", "--safe", s.User())
		ptyReq, winCh, isPty := s.Pty() // get SSH PTY information
		if isPty {
			if *verbose {
				log.Printf("Starting PTY for %s\n", s.RemoteAddr())
			}
			cmd.Env = append(cmd.Env, fmt.Sprintf("TERM=%s", ptyReq.Term))
			f, _ := pty.Start(cmd)
			go func() {
				for win := range winCh {
					syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCSWINSZ), uintptr(unsafe.Pointer(&struct{ h, w, x, y uint16 }{uint16(win.Height), uint16(win.Width), 0, 0})))
				}
			}()
			go func() { // goroutine to handle
				_, err = io.Copy(f, s) // stdin
				if err != nil {
					log.Printf("virsh f->s copy error: %v\n", err)
				}
			}()
			_, err = io.Copy(s, f) // stdout
			if err != nil {
				log.Printf("virsh s->f copy error: %v\n", err)
			}

			err = cmd.Wait()
			if err != nil {
				log.Printf("virsh wait error: %v\n", err)
			}
		} else {
			_, _ = io.WriteString(s, "No PTY requested.\n")
			_ = s.Exit(1)
		}
	})

	log.Printf("Starting sshpty server on %s\n", *bindHost)
	log.Fatal(ssh.ListenAndServe(*bindHost, nil, ssh.HostKeyFile(*hostKeyFile), ssh.PublicKeyAuth(func(ctx ssh.Context, key ssh.PublicKey) bool {
		return true // require pubkey auth
	})))
}
