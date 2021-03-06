// Copyright 2016 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

// This file contains the implementation of the keygen command.

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"

	"upspin.io/errors"
	"upspin.io/key/proquint"
	"upspin.io/pack/ee"
	"upspin.io/subcmd"
)

func (s *State) keygen(args ...string) {
	const help = `
Keygen creates a new Upspin key pair and stores the pair in local files
secret.upspinkey and public.upspinkey in the specified directory.
Existing key pairs are appended to secret2.upspinkey.
Keygen does not update the information in the key server;
use the "user -put" command for that.

New users should instead use the "signup" command to create their first key.

See the description for rotate for information about updating keys.
`
	// Keep flags in sync with signup.go. New flags here should appear
	// there as well.
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	var (
		curve      = fs.String("curve", "p256", "cryptographic curve `name`: p256, p384, or p521")
		secretSeed = fs.String("secretseed", "", "the seed containing a 128-bit secret in proquint format or a file that contains it")
		rotate     = fs.Bool("rotate", false, "back up the existing keys and replace them with new ones")
	)
	s.ParseFlags(fs, args, help, "keygen [-curve=256] [-secretseed=seed] <directory>")
	if fs.NArg() != 1 {
		usageAndExit(fs)
	}
	s.keygenCommand(fs.Arg(0), *curve, *secretSeed, *rotate)
}

func (s *State) keygenCommand(where, curve, secretseed string, rotate bool) {
	switch curve {
	case "p256", "p384", "p521":
		// ok
	default:
		s.Exitf("no such curve %q", curve)
	}

	public, private, secretStr, err := s.createKeys(curve, secretseed)
	if err != nil {
		s.Exitf("creating keys: %v", err)
	}

	err = s.saveKeys(where, rotate, public, private)
	if err != nil {
		s.Exitf("saving previous keys failed, keys not generated: %s", err)
	}
	private = strings.TrimSpace(private) + " # " + secretStr + "\n"
	err = s.writeKeys(where, public, private)
	if err != nil {
		s.Exitf("writing keys: %v", err)
	}
	fmt.Fprintln(s.Stderr, "Upspin private/public key pair written to:")
	fmt.Fprintf(s.Stderr, "\t%s\n", filepath.Join(where, "public.upspinkey"))
	fmt.Fprintf(s.Stderr, "\t%s\n", filepath.Join(where, "secret.upspinkey"))
	fmt.Fprintln(s.Stderr, "This key pair provides access to your Upspin identity and data.")
	if secretseed == "" {
		fmt.Fprintln(s.Stderr, "If you lose the keys you can re-create them by running this command:")
		fmt.Fprintf(s.Stderr, "\tupspin keygen -curve %s -secretseed %s %s\n", curve, secretseed, where)
		fmt.Fprintln(s.Stderr, "Write this command down and store it in a secure, private place.")
		fmt.Fprintln(s.Stderr, "Do not share your private key or this command with anyone.")
	}
	if rotate {
		fmt.Fprintln(s.Stderr, "\nTo install new keys in the key server, see 'upspin rotate -help'.")
	}
	fmt.Fprintln(s.Stderr)
}

func (s *State) createKeys(curveName, secretFlag string) (public, private, secretStr string, err error) {
	// Pick secret 128 bits.
	// TODO(ehg)  Consider whether we are willing to ask users to write long seeds for P521.
	b := make([]byte, 16)

	// There are three cases:
	// 1) No secretFlag was given. Create a new secret seed.
	// 2) A secretFlag looks valid. Accept it.
	// 3) The secretFlag must be a file. Try to read it.
	switch {
	case secretFlag == "":
		ee.GenEntropy(b)
		proquints := make([]interface{}, 8)
		for i := 0; i < 8; i++ {
			proquints[i] = proquint.Encode(binary.BigEndian.Uint16(b[2*i : 2*i+2]))
		}
		secretStr = fmt.Sprintf("%s-%s-%s-%s.%s-%s-%s-%s", proquints...)
		// Ignore punctuation on input;  this format is just to help the user keep their place.

	case validSecretSeed(secretFlag):
		secretStr = secretFlag
	default:
		data, err := ioutil.ReadFile(subcmd.Tilde(secretFlag))
		if err != nil {
			return "", "", "", errors.E("keygen", errors.IO, err)
		}
		secretStr = strings.TrimSpace(string(data))
	}

	if !validSecretSeed(secretStr) {
		log.Printf("expected secret like\n lusab-babad-gutih-tugad.gutuk-bisog-mudof-sakat\n"+
			"not\n %s\nkey not generated", secretStr)
		return "", "", "", errors.E("keygen", errors.Invalid, errors.Str("bad format for secret"))
	}
	for i := 0; i < 8; i++ {
		binary.BigEndian.PutUint16(b[2*i:2*i+2], proquint.Decode([]byte((secretStr)[6*i:6*i+5])))
	}

	pub, priv, err := ee.CreateKeys(curveName, b)
	if err != nil {
		return "", "", "", err
	}
	return string(pub), priv, secretStr, nil
}

// validSecretSeed reports whether a seed conforms to the proquint format.
// TODO: this could be more strict.
func validSecretSeed(seed string) bool {
	return len(seed) == 47 && seed[5] == '-'
}

// writeKeyFile writes a single key to its file, removing the file
// beforehand if necessary due to permission errors.
// If the file's parent directory does not exist, writeKeyFile creates it.
func (s *State) writeKeyFile(name, key string) error {
	// Make the directory if it does not exist.
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	// Create the file.
	const create = os.O_RDWR | os.O_CREATE | os.O_TRUNC
	fd, err := os.OpenFile(name, create, 0400)
	if os.IsPermission(err) && os.Remove(name) == nil {
		// Create may fail if file already exists and is unwritable,
		// which is how it was created.
		fd, err = os.OpenFile(name, create, 0400)
	}
	if err != nil {
		return err
	}
	// Write the key.
	_, err = fd.WriteString(key)
	if err != nil {
		fd.Close()
		return err
	}
	return fd.Close()

}

// writeKeys save both the public and private keys to their respective files.
func (s *State) writeKeys(where, publicKey, privateKey string) error {
	err := s.writeKeyFile(filepath.Join(where, "secret.upspinkey"), privateKey)
	if err != nil {
		return err
	}
	err = s.writeKeyFile(filepath.Join(where, "public.upspinkey"), publicKey)
	if err != nil {
		return err
	}
	return nil
}

func (s *State) saveKeys(where string, rotate bool, newPublic, newPrivate string) error {
	var (
		publicFile  = filepath.Join(where, "public.upspinkey")
		privateFile = filepath.Join(where, "secret.upspinkey")
		archiveFile = filepath.Join(where, "secret2.upspinkey")
	)

	// Read existing key pair.
	private, err := ioutil.ReadFile(privateFile)
	if os.IsNotExist(err) {
		// There is nothing to save. Did we expect there to be?
		if rotate {
			s.Exitf("cannot rotate keys: no prior keys exist in %s", where)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !rotate {
		s.Exitf("prior keys exist in %s; rerun with rotate command to update keys", where)
	}
	public, err := ioutil.ReadFile(publicFile)
	if err != nil {
		return err // Halt. Existing files are corrupted and need manual attention.
	}
	if string(public) == newPublic && string(private) == newPrivate {
		return nil // No need to save duplicates.
	}

	// Write old key pair to archive file.
	archive, err := os.OpenFile(archiveFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		return err // We don't have permission to archive old keys?
	}

	var modtime string
	info, err := os.Stat(privateFile)
	if err != nil {
		modtime = ""
	} else {
		modtime = info.ModTime().UTC().Format(" 2006-01-02 15:04:05Z")
	}
	_, err = fmt.Fprintf(archive, "# EE%s\n%s%s", modtime, public, private)
	if err != nil {
		return err
	}
	err = archive.Close()
	if err != nil {
		return err
	}
	fmt.Fprintf(s.Stderr, "Saved previous key pair to:\n\t%s\n", archiveFile)
	return nil
}
