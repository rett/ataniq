package seatnotice

import "encoding/base64"

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// point is a 65-byte uncompressed-point shape, varied by seed.
func point(seed byte) []byte {
	p := make([]byte, 65)
	p[0] = 0x04
	for i := 1; i < len(p); i++ {
		p[i] = seed + byte(i)
	}
	return p
}

// sub returns a valid subscription whose endpoint ends in name.
func sub(name string) Subscription {
	return Subscription{
		Endpoint: "https://push.example/send/" + name,
		P256DH:   b64(point(1)),
		Auth:     b64([]byte("0123456789abcdef")),
	}
}

func subJSON(s Subscription) string {
	return `{"endpoint":"` + s.Endpoint + `","p256dh":"` + s.P256DH + `","auth":"` + s.Auth + `"}`
}
