package host

import (
	"errors"
	"github.com/kuroiko0429/pwnbridge/pkg/protocol"
	"unicode/utf8"
)

func ValidateRequest(r protocol.Request) error {
	switch r.Op {
	case "set_face":
		if !utf8.ValidString(r.Face) || utf8.RuneCountInString(r.Face) > 64 {
			return errors.New("face must be valid UTF-8 and <=64 runes")
		}
		if r.Message != "" {
			return errors.New("unexpected message")
		}
	case "set_message":
		if !utf8.ValidString(r.Message) || utf8.RuneCountInString(r.Message) > 512 {
			return errors.New("message must be valid UTF-8 and <=512 runes")
		}
		if r.Face != "" {
			return errors.New("unexpected face")
		}
	case "clear_override":
		if r.Face != "" || r.Message != "" || r.TTLSeconds != 0 {
			return errors.New("clear takes no parameters")
		}
		return nil
	default:
		return errors.New("unsupported RPC operation")
	}
	if r.TTLSeconds < 1 || r.TTLSeconds > 300 {
		return errors.New("ttl_seconds must be 1..300")
	}
	return nil
}
