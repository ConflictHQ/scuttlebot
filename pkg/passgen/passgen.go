// Package passgen provides configurable password generation using
// cryptographically secure randomness.
package passgen

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// Charset identifies a character pool for password generation.
type Charset string

const (
	Hex      Charset = "hex"      // 0-9a-f
	Alpha    Charset = "alpha"    // a-zA-Z
	Alphanum Charset = "alphanum" // a-zA-Z0-9
	ASCII    Charset = "ascii"    // printable ASCII 33-126, excluding ' " ` \
)

const (
	hexChars      = "0123456789abcdef"
	alphaChars    = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	alphanumChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

// asciiChars is printable ASCII 33-126 minus ' " ` \ which cause shell
// escaping problems.
var asciiChars string

func init() {
	var b []byte
	for c := byte(33); c <= 126; c++ {
		switch c {
		case '\'', '"', '`', '\\':
			continue
		}
		b = append(b, c)
	}
	asciiChars = string(b)
}

// Options controls password generation.
type Options struct {
	Length  int     // default 24
	Charset Charset // default Alphanum
}

// Generate creates a random password using the given options.
// If opts is nil, defaults are used (24-char alphanum).
func Generate(opts *Options) (string, error) {
	length := 24
	charset := Alphanum

	if opts != nil {
		if opts.Length != 0 {
			length = opts.Length
		}
		if opts.Charset != "" {
			charset = opts.Charset
		}
	}

	if length < 1 {
		return "", fmt.Errorf("passgen: length must be >= 1, got %d", length)
	}

	pool, err := charsetPool(charset)
	if err != nil {
		return "", err
	}

	max := big.NewInt(int64(len(pool)))
	buf := make([]byte, length)
	for i := range buf {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("passgen: crypto/rand: %w", err)
		}
		buf[i] = pool[idx.Int64()]
	}
	return string(buf), nil
}

// ParseCharset parses a charset name, returning an error for unknown values.
func ParseCharset(s string) (Charset, error) {
	switch Charset(s) {
	case Hex:
		return Hex, nil
	case Alpha:
		return Alpha, nil
	case Alphanum:
		return Alphanum, nil
	case ASCII:
		return ASCII, nil
	default:
		return "", fmt.Errorf("passgen: unknown charset %q", s)
	}
}

func charsetPool(c Charset) (string, error) {
	switch c {
	case Hex:
		return hexChars, nil
	case Alpha:
		return alphaChars, nil
	case Alphanum:
		return alphanumChars, nil
	case ASCII:
		return asciiChars, nil
	default:
		return "", fmt.Errorf("passgen: unknown charset %q", c)
	}
}
