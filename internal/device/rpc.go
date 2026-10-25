package device

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
)

const ControlSocket = "/run/pwnbridge/control.sock"

// validUnicodeEscapes prevents encoding/json's replacement of lone surrogate
// escapes with U+FFFD. JSON syntax is checked separately by the decoder.
func validUnicodeEscapes(b []byte) bool {
	inString := false
	for i := 0; i < len(b); i++ {
		if b[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || b[i] != '\\' {
			continue
		}
		i++
		if i >= len(b) {
			return false
		}
		if b[i] != 'u' {
			continue
		}
		if i+4 >= len(b) {
			return false
		}
		v, e := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if v >= 0xdc00 && v <= 0xdfff {
			return false
		}
		if v >= 0xd800 && v <= 0xdbff {
			if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
				return false
			}
			low, e := strconv.ParseUint(string(b[i+3:i+7]), 16, 16)
			if e != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

// strictObject rejects duplicate keys, null fields, non-UTF8 and trailing data.
func strictObject(b []byte) (map[string]json.RawMessage, error) {
	invalid := errors.New("invalid JSON object")
	if len(b) > maxFrame || !utf8.Valid(b) || !validUnicodeEscapes(b) {
		return nil, invalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	tok, e := d.Token()
	if e != nil || tok != json.Delim('{') {
		return nil, invalid
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		k, e := d.Token()
		if e != nil {
			return nil, invalid
		}
		key, ok := k.(string)
		if !ok {
			return nil, invalid
		}
		if _, exists := fields[key]; exists {
			return nil, invalid
		}
		var v json.RawMessage
		if d.Decode(&v) != nil || bytes.Equal(v, []byte("null")) {
			return nil, invalid
		}
		fields[key] = v
	}
	if _, e = d.Token(); e != nil {
		return nil, invalid
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, invalid
	}
	return fields, nil
}

func DecodeRequest(b []byte) (protocol.Request, error) {
	var r protocol.Request
	invalid := errors.New("invalid UI request")
	fields, e := strictObject(b)
	if e != nil {
		return r, invalid
	}
	for key := range fields {
		switch key {
		case "op", "face", "message", "ttl_seconds":
		default:
			return r, invalid
		}
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil {
		return r, invalid
	}
	switch r.Op {
	case "set_face":
		if _, ok := fields["message"]; ok {
			return r, invalid
		}
		if utf8.RuneCountInString(r.Face) > 64 {
			return r, invalid
		}
	case "set_message":
		if _, ok := fields["face"]; ok {
			return r, invalid
		}
		if utf8.RuneCountInString(r.Message) > 512 {
			return r, invalid
		}
	case "clear_override":
		if len(fields) != 1 {
			return r, invalid
		}
		return r, nil
	default:
		return r, invalid
	}
	if r.TTLSeconds < 1 || r.TTLSeconds > 300 {
		return r, invalid
	}
	return r, nil
}

func proxyRPC(ctx context.Context, path string, r protocol.Request, timeout time.Duration) (protocol.Response, error) {
	var response protocol.Response
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c, e := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if e != nil {
		return response, e
	}
	defer c.Close()
	deadline, _ := ctx.Deadline()
	c.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if e = json.NewEncoder(c).Encode(r); e != nil {
		return response, e
	}
	line, e := bufio.NewReader(io.LimitReader(c, maxFrame+1)).ReadBytes('\n')
	if e != nil {
		return response, e
	}
	fields, e := strictObject(line)
	if e != nil {
		return response, e
	}
	if _, ok := fields["ok"]; !ok {
		return response, errors.New("missing RPC result")
	}
	d := json.NewDecoder(bytes.NewReader(line))
	d.DisallowUnknownFields()
	if e = d.Decode(&response); e != nil {
		return response, e
	}
	if response.OK && response.Error != "" || !response.OK && response.Error == "" {
		return protocol.Response{}, errors.New("inconsistent RPC result")
	}
	return response, nil
}
