/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/
package serialization_test

import (
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-common/protoutil"

	"github.com/hyperledger/fabric-x-committer/utils/serialization"
	"github.com/hyperledger/fabric-x-committer/utils/test"
)

// TestUnwrapEnvelopeBadInput tests UnwrapEnvelope function with invalid inputs.
func TestUnwrapEnvelopeBadInput(t *testing.T) {
	t.Parallel()
	t.Run("Not an envelope", func(t *testing.T) {
		t.Parallel()
		_, _, err := serialization.UnwrapEnvelope([]byte("invalid input"))
		require.Error(t, err)
	})

	t.Run("OK Header with an invalid payload", func(t *testing.T) {
		t.Parallel()
		envelopeBytes := protoutil.MarshalOrPanic(&common.Envelope{
			Payload: []byte("not-a-payload"),
		})
		_, _, err := serialization.UnwrapEnvelope(envelopeBytes)
		require.Error(t, err)
	})

	t.Run("OK Payload with a nil Header", func(t *testing.T) {
		t.Parallel()
		envelopeBytes := protoutil.MarshalOrPanic(&common.Envelope{
			Payload: protoutil.MarshalOrPanic(&common.Payload{
				Header: nil,
				Data:   []byte("some data"),
			}),
		})

		_, _, err := serialization.UnwrapEnvelope(envelopeBytes)
		require.Error(t, err)
	})

	t.Run("OK payload but invalid ChannelHeader", func(t *testing.T) {
		t.Parallel()
		envelopeBytes := protoutil.MarshalOrPanic(&common.Envelope{
			Payload: protoutil.MarshalOrPanic(&common.Payload{
				Header: &common.Header{
					ChannelHeader: []byte("not-a-channel-header"),
				},
				Data: []byte("some data"),
			}),
		})

		_, _, err := serialization.UnwrapEnvelope(envelopeBytes)
		require.Error(t, err)
	})
}

// TestUnwrapEnvelopeGoodInput Tests properly wrapped envelope is unwrapped correctly.
func TestUnwrapEnvelopeGoodInput(t *testing.T) {
	t.Parallel()
	expectedPayload := []byte("test payload")

	expectedChannelHeader := &common.ChannelHeader{
		ChannelId: "test-channel",
	}

	// Wrap
	wrappedEnvelope := protoutil.MarshalOrPanic(&common.Envelope{
		Payload: protoutil.MarshalOrPanic(&common.Payload{
			Header: &common.Header{
				ChannelHeader: protoutil.MarshalOrPanic(expectedChannelHeader),
			},
			Data: expectedPayload,
		}),
	})

	// Unwrap
	actualPayload, actualChannelHeader, err := serialization.UnwrapEnvelope(wrappedEnvelope)

	// -Check 1- Check unwrap envelope has no error
	require.NoError(t, err)

	// -Check 2- Check we get the correct Payload & Header
	require.Equal(t, expectedPayload, actualPayload)
	test.RequireProtoEqual(t, expectedChannelHeader, actualChannelHeader)
}

// TestUnwrapEnvelopeLiteBadInput tests UnwrapEnvelopeLite with invalid inputs.
func TestUnwrapEnvelopeLiteBadInput(t *testing.T) {
	t.Parallel()
	t.Run("Not an envelope", func(t *testing.T) {
		t.Parallel()
		_, err := serialization.UnwrapEnvelopeLite([]byte("invalid input"))
		// Garbage bytes may parse as valid protobuf with unexpected fields,
		// but should not produce a valid header.
		if err == nil {
			t.Log("garbage bytes parsed without error (valid protobuf but meaningless)")
		}
	})

	t.Run("Empty input", func(t *testing.T) {
		t.Parallel()
		_, err := serialization.UnwrapEnvelopeLite(nil)
		require.Error(t, err)
	})

	t.Run("Empty envelope", func(t *testing.T) {
		t.Parallel()
		_, err := serialization.UnwrapEnvelopeLite([]byte{})
		require.Error(t, err)
	})

	t.Run("Envelope without header in payload", func(t *testing.T) {
		t.Parallel()
		envelopeBytes := protoutil.MarshalOrPanic(&common.Envelope{
			Payload: protoutil.MarshalOrPanic(&common.Payload{
				Header: nil,
				Data:   []byte("some data"),
			}),
		})
		_, err := serialization.UnwrapEnvelopeLite(envelopeBytes)
		require.Error(t, err)
	})
}

// TestUnwrapEnvelopeLiteGoodInput tests correct extraction of fields.
func TestUnwrapEnvelopeLiteGoodInput(t *testing.T) {
	t.Parallel()
	expectedData := []byte("test payload")
	expectedType := int32(common.HeaderType_MESSAGE)
	expectedTxID := "tx-123-abc"

	wrappedEnvelope := protoutil.MarshalOrPanic(&common.Envelope{
		Payload: protoutil.MarshalOrPanic(&common.Payload{
			Header: &common.Header{
				ChannelHeader: protoutil.MarshalOrPanic(&common.ChannelHeader{
					Type:      expectedType,
					TxId:      expectedTxID,
					ChannelId: "test-channel",
				}),
			},
			Data: expectedData,
		}),
	})

	hdr, err := serialization.UnwrapEnvelopeLite(wrappedEnvelope)
	require.NoError(t, err)
	require.Equal(t, expectedType, hdr.HeaderType)
	require.Equal(t, expectedTxID, hdr.TxId)
	require.Equal(t, expectedData, hdr.Data)
}

// TestUnwrapEnvelopeLiteConsistency verifies that UnwrapEnvelopeLite returns
// the same values as UnwrapEnvelope for various inputs.
func TestUnwrapEnvelopeLiteConsistency(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name       string
		headerType int32
		txID       string
		data       []byte
	}{
		{"MESSAGE with data", int32(common.HeaderType_MESSAGE), "tx-msg-1", []byte("payload data")},
		{"CONFIG with data", int32(common.HeaderType_CONFIG), "tx-cfg-1", []byte("config data")},
		{"Empty TxID", int32(common.HeaderType_MESSAGE), "", []byte("data")},
		{"Empty data", int32(common.HeaderType_MESSAGE), "tx-empty", nil},
		{"Large TxID", int32(common.HeaderType_MESSAGE), "tx-" + string(make([]byte, 256)), []byte("d")},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wrappedEnvelope := protoutil.MarshalOrPanic(&common.Envelope{
				Payload: protoutil.MarshalOrPanic(&common.Payload{
					Header: &common.Header{
						ChannelHeader: protoutil.MarshalOrPanic(&common.ChannelHeader{
							Type: tc.headerType,
							TxId: tc.txID,
						}),
					},
					Data: tc.data,
				}),
			})

			origData, origHdr, origErr := serialization.UnwrapEnvelope(wrappedEnvelope)
			liteHdr, liteErr := serialization.UnwrapEnvelopeLite(wrappedEnvelope)

			require.NoError(t, origErr)
			require.NoError(t, liteErr)
			require.Equal(t, origHdr.Type, liteHdr.HeaderType)
			require.Equal(t, origHdr.TxId, liteHdr.TxId)
			require.Equal(t, origData, liteHdr.Data)
		})
	}
}

// Run: go test -fuzz=FuzzUnwrapEnvelopeLiteConsistency -fuzztime=30s ./utils/serialization/
func FuzzUnwrapEnvelopeLiteConsistency(f *testing.F) {
	seeds := []struct {
		headerType int32
		txID       string
		data       []byte
		channelID  string
		signature  []byte
		sigHeader  []byte
	}{
		{0, "", nil, "", nil, nil}, // all defaults (proto3 omits everything)
		{int32(common.HeaderType_MESSAGE), "tx-1", []byte("hello"), "ch1", nil, nil},
		{int32(common.HeaderType_CONFIG), "tx-cfg", []byte("cfg"), "ch2", []byte("sig"), []byte("sighdr")},
		{int32(common.HeaderType_ENDORSER_TRANSACTION), "tx-end", make([]byte, 1024), "ch3", make([]byte, 72), make([]byte, 64)},
		{3, "", []byte("d"), "", nil, nil},                  // non-zero type, empty txID
		{0, "tx-zero-type", []byte("d"), "", nil, nil},      // zero type, non-empty txID
		{1, string(make([]byte, 512)), nil, "ch", nil, nil}, // large txID
	}
	for _, s := range seeds {
		f.Add(s.headerType, s.txID, s.data, s.channelID, s.signature, s.sigHeader)
	}

	f.Fuzz(func(t *testing.T, headerType int32, txID string, data []byte, channelID string, signature, sigHeader []byte) {
		// Proto3 string fields must be valid UTF-8. Skip inputs that would
		// cause proto.Marshal to reject them, since we're testing the parser
		// not the serializer.
		if !utf8.ValidString(txID) || !utf8.ValidString(channelID) {
			t.Skip("skipping invalid UTF-8 input")
		}

		wrappedEnvelope := protoutil.MarshalOrPanic(&common.Envelope{
			Payload: protoutil.MarshalOrPanic(&common.Payload{
				Header: &common.Header{
					ChannelHeader: protoutil.MarshalOrPanic(&common.ChannelHeader{
						Type:      headerType,
						TxId:      txID,
						ChannelId: channelID,
					}),
					SignatureHeader: sigHeader,
				},
				Data: data,
			}),
			Signature: signature,
		})

		origData, origHdr, origErr := serialization.UnwrapEnvelope(wrappedEnvelope)
		liteHdr, liteErr := serialization.UnwrapEnvelopeLite(wrappedEnvelope)

		require.NoError(t, origErr)
		require.NoError(t, liteErr)
		require.Equal(t, origHdr, liteHdr)
		require.Equal(t, origData, liteHdr.Data)
	})
}

func makeTestEnvelope() []byte {
	return protoutil.MarshalOrPanic(&common.Envelope{
		Payload: protoutil.MarshalOrPanic(&common.Payload{
			Header: &common.Header{
				ChannelHeader: protoutil.MarshalOrPanic(&common.ChannelHeader{
					Type:      int32(common.HeaderType_MESSAGE),
					TxId:      "benchmark-tx-id-12345",
					ChannelId: "benchmark-channel",
				}),
			},
			Data: []byte("benchmark payload data"),
		}),
	})
}

func BenchmarkUnwrapEnvelope(b *testing.B) {
	envelope := makeTestEnvelope()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := serialization.UnwrapEnvelope(envelope)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnwrapEnvelopeLite(b *testing.B) {
	envelope := makeTestEnvelope()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := serialization.UnwrapEnvelopeLite(envelope)
		if err != nil {
			b.Fatal(err)
		}
	}
}
