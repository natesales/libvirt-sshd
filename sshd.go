package main

import (
	"encoding/xml"
	"flag"
	"fmt"
	gossh "golang.org/x/crypto/ssh"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"

	"github.com/creack/pty"
	"github.com/gliderlabs/ssh"
)

var release = "dev" // Set by build process

// domain stores a libvirt domain
type domain struct {
	XMLName  xml.Name `xml:"domain"`
	Name     string   `xml:"name"`
	Password string   `xml:"description"`
}

// Define flags
var (
	bindHost    = flag.String("l", ":2222", "Listen <host:port>")
	hostKeyFile = flag.String("k", "~/.ssh/id_ed25519", "SSH host key file")
	verbose     = flag.Bool("v", false, "Enable verbose logging")
)

func handleAuth(ctx ssh.Context, providedPassword string) bool {
	log.Printf("New connection from %s user %s password %s\n", ctx.RemoteAddr(), ctx.User(), providedPassword)

	files, err := filepath.Glob("/etc/libvirt/qemu/*.xml")
	if err != nil {
		log.Fatalf("Unable to parse qemu config file glob: %v\n", err)
	}

	for _, f := range files {
		// Read libvirt XML file
		xmlFile, err := os.Open(f)
		if err != nil {
			log.Printf("XML open error: %v\n", err)
		}

		// Parse libvirt XML file
		byteValue, _ := ioutil.ReadAll(xmlFile)
		var currentDomain domain
		err = xml.Unmarshal(byteValue, &currentDomain)
		if err != nil {
			log.Println(err)
			return false
		}
		_ = xmlFile.Close()

		if *verbose {
			fmt.Printf("Found VM %s password %s\n", currentDomain.Name, currentDomain.Password)
		}

		if currentDomain.Name == ctx.User() && currentDomain.Password == providedPassword {
			return true // Allow access
		}
	}

	return false // If there are no defined VMs, deny access
}

func handleSession(s ssh.Session) {
	cmd := exec.Command("virsh", "console", "--safe", s.User())
	ptyReq, winCh, isPty := s.Pty() // get SSH PTY information
	if isPty {
		cmd.Env = append(cmd.Env, fmt.Sprintf("TERM=%s", ptyReq.Term))
		f, _ := pty.Start(cmd)
		go func() {
			for win := range winCh {
				_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCSWINSZ), uintptr(unsafe.Pointer(&struct{ h, w, x, y uint16 }{uint16(win.Height), uint16(win.Width), 0, 0})))
			}
		}()
		go func() { // goroutine to handle
			_, err := io.Copy(f, s) // stdin
			if err != nil {
				log.Printf("virsh f->s copy error: %v\n", err)
			}
		}()
		_, err := io.Copy(s, f) // stdout
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
}

func main() {
	flag.Usage = func() {
		fmt.Printf("Usage for libvirt-sshd (%s) https://github.com/natesales/libvirt-sshd:\n", release)
		flag.PrintDefaults()
	}

	flag.Parse()

	pemBytes, err := ioutil.ReadFile(*hostKeyFile)
	if err != nil {
		log.Fatal(err)
	}

	signer, err := gossh.ParsePrivateKey(pemBytes)
	if err != nil {
		log.Fatal(err)
	}

	sshServer := &ssh.Server{
		Addr:            *bindHost,
		HostSigners:     []ssh.Signer{signer},
		Handler:         handleSession,
		PasswordHandler: handleAuth,
	}
	log.Printf("Starting libvirt-sshd on %s\n", *bindHost)
	log.Fatal(sshServer.ListenAndServe())
}
