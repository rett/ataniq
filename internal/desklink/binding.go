package desklink

import (
	"crypto/subtle"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"math"
)

// ChannelBinding is the TLS exporter for one desk connection and home.
type ChannelBinding [32]byte

// Matches reports whether a supplied binding belongs to this connection.
func (b ChannelBinding) Matches(candidate []byte) bool {
	return subtle.ConstantTimeCompare(b[:], candidate) == 1
}

func channelBinding(state tls.ConnectionState, homeID string) (ChannelBinding, error) {
	if homeID == "" || uint64(len(homeID)) > math.MaxUint32 {
		return ChannelBinding{}, errors.New("desklink: invalid home ID length")
	}
	// Frame each string with its four-byte big-endian byte length.
	context := binary.BigEndian.AppendUint32(nil, uint32(len(homeID)))
	context = append(context, homeID...)
	context = binary.BigEndian.AppendUint32(context, uint32(len(state.NegotiatedProtocol)))
	context = append(context, state.NegotiatedProtocol...)
	key, err := state.ExportKeyingMaterial("EXPORTER-ataniq-desk-v1", context, len(ChannelBinding{}))
	if err != nil {
		return ChannelBinding{}, err
	}
	return ChannelBinding(key), nil
}
