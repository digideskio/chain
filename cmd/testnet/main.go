package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

var (
	srcdir       = os.Getenv("CHAIN")
	instanceAddr = os.Getenv("TESTNET_IP")
	sshConfig    = &ssh.ClientConfig{
		User: "ubuntu",
		Auth: sshAuthMethods(
			os.Getenv("SSH_AUTH_SOCK"),
			os.Getenv("SSH_PRIVATE_KEY"),
		),
	}
)

const (
	stopsh  = `sudo stop chain`
	startsh = `sudo start chain`
)

func sshAuthMethods(agentSock, privKeyPEM string) (m []ssh.AuthMethod) {
	conn, sockErr := net.Dial("unix", agentSock)
	key, keyErr := ssh.ParsePrivateKey([]byte(privKeyPEM))
	if sockErr != nil && keyErr != nil {
		log.Println(sockErr)
		log.Println(keyErr)
		log.Fatal("no auth methods found (tried agent and environ)")
	}
	if sockErr == nil {
		m = append(m, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
	}
	if keyErr == nil {
		m = append(m, ssh.PublicKeys(key))
	}
	return m
}

func main() {
	envFile, err := ioutil.ReadFile(srcdir + "/cmd/testnet/chain.env")
	must(err)
	coredBin := mustBuildCored()
	mustRunOn(instanceAddr, stopsh)
	log.Println("uploading binaries")
	must(scpPut(instanceAddr, coredBin, "cored", 0755))
	must(scpPut(instanceAddr, envFile, "chain.env", 0755))
	mustRunOn(instanceAddr, startsh)
	log.Println("SUCCESS")
}

func mustBuildCored() []byte {
	log.Println("building cored")

	env := []string{
		"GOOS=linux",
		"GOARCH=amd64",
		"CGO_ENABLED=0",
	}

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Stderr = os.Stderr
	commit, err := cmd.Output()
	must(err)
	commit = bytes.TrimSpace(commit)
	date := time.Now().UTC().Format(time.RFC3339)
	cmd = exec.Command("go", "build",
		"-tags", "insecure_disable_https_redirect",
		"-ldflags", "-X main.buildTag=dev -X main.buildDate="+date+" -X main.buildCommit="+string(commit),
		"-o", "/dev/stdout",
		"chain/cmd/cored",
	)
	cmd.Env = mergeEnvLists(env, os.Environ())
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	must(err)
	log.Printf("cored executable: %d bytes", len(out))
	return out
}

func scpPut(host string, data []byte, dest string, mode int) error {
	log.Printf("scp %d bytes to %s", len(data), dest)
	var client *ssh.Client
	retry(func() (err error) {
		client, err = ssh.Dial("tcp", host+":22", sshConfig)
		return
	})
	defer client.Close()
	s, err := client.NewSession()
	if err != nil {
		return err
	}
	s.Stderr = os.Stderr
	s.Stdout = os.Stderr
	w, err := s.StdinPipe()
	if err != nil {
		return err
	}
	go func() {
		defer w.Close()
		fmt.Fprintf(w, "C%04o %d %s\n", mode, len(data), dest)
		w.Write(data)
		w.Write([]byte{0})
	}()

	return s.Run("/usr/bin/scp -tr .")
}

func mustRunOn(host, sh string, keyval ...string) {
	if len(keyval)%2 != 0 {
		log.Fatal("odd params", keyval)
	}
	log.Println("run on", host)
	client, err := ssh.Dial("tcp", host+":22", sshConfig)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()
	s, err := client.NewSession()
	if err != nil {
		log.Fatal(err)
	}
	s.Stdout = os.Stderr
	s.Stderr = os.Stderr
	for i := 0; i < len(keyval); i += 2 {
		sh = strings.Replace(sh, "{{"+keyval[i]+"}}", keyval[i+1], -1)
	}
	err = s.Run(sh)
	if err != nil {
		log.Fatal(err)
	}
}

var errRetry = errors.New("retry")

// retry f until it returns nil.
// wait 500ms in between attempts.
// log err unless it is errRetry.
// after 5 failures, it will call log.Fatal.
// returning errRetry doesn't count as a failure.
func retry(f func() error) {
	for n := 0; n < 5; {
		err := f()
		if err != nil && err != errRetry {
			log.Println("retrying:", err)
			n++
		}
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		return
	}
	log.Fatal("too many retries")
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// mergeEnvLists merges the two environment lists such that
// variables with the same name in "in" replace those in "out".
// This always returns a newly allocated slice.
func mergeEnvLists(in, out []string) []string {
	out = append([]string(nil), out...)
NextVar:
	for _, inkv := range in {
		k := strings.SplitAfterN(inkv, "=", 2)[0]
		for i, outkv := range out {
			if strings.HasPrefix(outkv, k) {
				out[i] = inkv
				continue NextVar
			}
		}
		out = append(out, inkv)
	}
	return out
}