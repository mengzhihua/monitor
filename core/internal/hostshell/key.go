package hostshell

import (
	"bytes"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"golang.org/x/crypto/ssh"
)

// OpenSSH's encrypted key header contains a caller-controlled bcrypt work
// factor. Bound it before decryption so a pasted key cannot tie up a worker.
func checkKeyCost(key string) error {
	block, _ := pem.Decode([]byte(key))
	if block == nil || block.Type != "OPENSSH PRIVATE KEY" {
		return nil
	}
	magic := []byte("openssh-key-v1\x00")
	if !bytes.HasPrefix(block.Bytes, magic) {
		return errors.New("invalid SSH private key")
	}
	rest := block.Bytes[len(magic):]
	next := func() ([]byte, bool) {
		if len(rest) < 4 {
			return nil, false
		}
		n := binary.BigEndian.Uint32(rest[:4])
		rest = rest[4:]
		if uint64(n) > uint64(len(rest)) {
			return nil, false
		}
		v := rest[:n]
		rest = rest[n:]
		return v, true
	}
	cipher, ok := next()
	if !ok {
		return errors.New("invalid SSH private key")
	}
	kdf, ok := next()
	if !ok {
		return errors.New("invalid SSH private key")
	}
	opts, ok := next()
	if !ok {
		return errors.New("invalid SSH private key")
	}
	if string(cipher) == "none" {
		return nil
	}
	var cost struct {
		Salt   string
		Rounds uint32
	}
	if string(kdf) != "bcrypt" || ssh.Unmarshal(opts, &cost) != nil || len(cost.Salt) > 1024 || cost.Rounds == 0 || cost.Rounds > 64 {
		return errors.New("invalid SSH private key: bcrypt rounds must be 1..64")
	}
	return nil
}
