/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package serialization

import (
	"github.com/cockroachdb/errors"
	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/hyperledger/fabric-x-common/protoutil"
)

// UnwrapEnvelope deserialize an envelope.
func UnwrapEnvelope(message []byte) ([]byte, *common.ChannelHeader, error) {
	envelope, err := protoutil.GetEnvelopeFromBlock(message)
	if err != nil {
		return nil, nil, errors.Wrap(err, "error parsing envelope")
	}

	payload, channelHdr, err := ParseEnvelope(envelope)
	if err != nil {
		return nil, nil, err
	}

	return payload.Data, channelHdr, nil
}

// ParseEnvelope parse the envelope content.
func ParseEnvelope(envelope *common.Envelope) (*common.Payload, *common.ChannelHeader, error) {
	if envelope == nil {
		return nil, nil, errors.New("nil envelope")
	}

	payload, err := protoutil.UnmarshalPayload(envelope.Payload)
	if err != nil {
		return nil, nil, errors.Wrap(err, "error unmarshaling payload")
	}

	if payload.Header == nil { // Will panic if payload.Header is nil
		return nil, nil, errors.New("nil payload header")
	}

	channelHdr, err := protoutil.UnmarshalChannelHeader(payload.Header.ChannelHeader)
	if err != nil {
		return nil, nil, errors.Wrap(err, "error unmarshaling channel header")
	}
	return payload, channelHdr, nil
}

// EnvelopeHeader holds the minimal fields extracted from a protobuf Envelope
// without performing full deserialization.
type EnvelopeHeader struct {
	HeaderType int32
	TxId       string
	Data       []byte
}

// UnwrapEnvelopeLite extracts HeaderType, TxId, and payload Data from a
// serialized Envelope by walking the protobuf wire format directly, instead
// of performing 3 chained proto.Unmarshal calls (Envelope → Payload → ChannelHeader)
// like UnwrapEnvelope does. This reduces allocations from 9 to 1 and is ~8x faster.
//
// The function navigates through the following nested proto messages,
// extracting only the fields it needs (marked with ←) and skipping the rest:
//
//	message Envelope {
//	    bytes payload = 1;   // serialized Payload
//	    bytes signature = 2;
//	}
//
//	message Payload {
//	    Header header = 1;   // serialized Header
//	    bytes data = 2;      // ← EnvelopeHeader.Data
//	}
//
//	message Header {
//	    bytes channel_header = 1;   // serialized ChannelHeader
//	    bytes signature_header = 2;
//	}
//
//	message ChannelHeader {
//	    int32 type = 1;                          // ← EnvelopeHeader.HeaderType
//	    int32 version = 2;
//	    google.protobuf.Timestamp timestamp = 3;
//	    string channel_id = 4;
//	    string tx_id = 5;                        // ← EnvelopeHeader.TxId
//	    uint64 epoch = 6;
//	    bytes extension = 7;
//	    bytes tls_cert_hash = 8;
//	}
//
// Each protobuf field is encoded on the wire as:
//
//	[tag] [value]
//
// The tag is a varint encoding (field_number << 3 | wire_type). For bytes/string
// fields, the value is a length prefix followed by the raw bytes:
//
//	[tag: 1 byte] [length: varint] [data: length bytes]
//
// For varint fields (like int32), the value is just the varint itself:
//
//	[tag: 1 byte] [value: varint]
//
// For example, an Envelope with Type=3 (ENDORSER_TRANSACTION), TxId="tx1",
// and Data="hello" encodes as:
//
//	Envelope (20 bytes):
//	  0a    = tag: field_number=1, wire_type=2 (bytes)   [1<<3|2 = 0x0a]
//	  12    = length: 18 bytes
//	  [...] = Payload (serialized, 18 bytes)             → Envelope.payload
//
//	  Payload (18 bytes, nested inside Envelope):
//	    0a    = tag: field_number=1, wire_type=2 (bytes) [1<<3|2 = 0x0a]
//	    09    = length: 9 bytes
//	    [...] = Header (serialized, 9 bytes)             → Payload.header
//
//	    12    = tag: field_number=2, wire_type=2 (bytes) [2<<3|2 = 0x12]
//	    05    = length: 5 bytes
//	    68656c6c6f = "hello"                             → Payload.data ←
//
//	    Header (9 bytes, nested inside Payload):
//	      0a    = tag: field_number=1, wire_type=2 (bytes) [1<<3|2 = 0x0a]
//	      07    = length: 7 bytes
//	      [...] = ChannelHeader (serialized, 7 bytes)      → Header.channel_header
//
//	      ChannelHeader (7 bytes, nested inside Header):
//	        08  = tag: field_number=1, wire_type=0 (varint) [1<<3|0 = 0x08]
//	        03  = value: 3 (ENDORSER_TRANSACTION)           → ChannelHeader.type ←
//
//	        2a  = tag: field_number=5, wire_type=2 (bytes)  [5<<3|2 = 0x2a]
//	        03  = length: 3 bytes
//	        747831 = "tx1"                                  → ChannelHeader.tx_id ←
//
// The function reads tags sequentially at each nesting level using protowire,
// matches the field numbers from the proto definitions above, and skips
// everything else.
func UnwrapEnvelopeLite(message []byte) (*EnvelopeHeader, error) {
	// Step 1: Read the Envelope. The input bytes are a serialized Envelope message.
	// We need field 1 (payload). Using the example from the doc comment:
	//
	//   input: [0a 12 ...]
	//           ^^         tag: field=1, wire_type=bytes (0a = 1<<3 | 2)
	//              ^^      length: 18 bytes
	//                 ...  payload bytes (the serialized Payload message)
	//
	// extractBytesField scans for field 1 with bytes wire type and returns its value.
	payloadBytes, err := extractBytesField(message, 1)
	if err != nil || payloadBytes == nil {
		return nil, errors.New("failed to extract payload from envelope")
	}

	// Step 2: Read the Payload. We need two fields from it:
	//   field 1 (header) and field 2 (data).
	//
	//   payloadBytes: [0a 09 ... 12 05 68656c6c6f]
	//                  ^^          tag: field=1, wire_type=bytes → Header
	//                     ^^       length: 9 bytes
	//                        ...   header bytes (serialized Header)
	//                            ^^          tag: field=2, wire_type=bytes → Data
	//                               ^^       length: 5 bytes
	//                                  ^^^^^ "hello"
	//
	// Note: This scans payloadBytes twice (once per field). A single-pass loop
	// that extracts both fields simultaneously is ~5 ns faster, but the payload
	// is small enough that the difference is negligible. We chose to reuse
	// extractBytesField for simplicity.
	headerBytes, err := extractBytesField(payloadBytes, 1)
	if err != nil || headerBytes == nil {
		return nil, errors.New("missing header in payload")
	}
	// Data may be absent (nil) if the Payload has no data — that's valid.
	dataBytes, err := extractBytesField(payloadBytes, 2)
	if err != nil {
		return nil, errors.New("invalid data in payload")
	}

	// Step 3: Read the Header. We need field 1 (channel_header).
	//
	//   headerBytes: [0a 07 ...]
	//                 ^^         tag: field=1, wire_type=bytes → ChannelHeader
	//                    ^^      length: 7 bytes
	//                       ...  channel header bytes
	channelHeaderBytes, err := extractBytesField(headerBytes, 1)
	if err != nil {
		return nil, errors.New("failed to extract channel header")
	}
	// When all ChannelHeader fields are zero/default, proto3 omits the field entirely,
	// so channelHeaderBytes is nil. This is valid and means Type=0 and TxId="".
	if channelHeaderBytes == nil {
		return &EnvelopeHeader{Data: dataBytes}, nil
	}

	// Step 4: Read the ChannelHeader. We need field 1 (type) and field 5 (tx_id).
	//
	//   channelHeaderBytes: [08 03 2a 03 747831]
	//                        ^^                   tag: field=1, wire_type=varint → Type
	//                           ^^                value: 3 (ENDORSER_TRANSACTION)
	//                              ^^             tag: field=5, wire_type=bytes → TxId
	//                                 ^^          length: 3
	//                                    ^^^^^^   "tx1"
	headerType, txID, err := extractTypeAndTxID(channelHeaderBytes)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse channel header fields")
	}

	return &EnvelopeHeader{
		HeaderType: headerType,
		TxId:       txID,
		Data:       dataBytes,
	}, nil
}

// extractBytesField scans a protobuf wire-format message for the first
// occurrence of the given field number with bytes wire type, returning its value.
// Returns (nil, nil) if the field is not found.
//
// Protobuf wire format encodes each field as [tag] [value], where the tag is
// a varint encoding (field_number << 3 | wire_type). For example, to find
// field 1 with bytes wire type, we look for tag 0x0a (1<<3 | 2 = 10).
// This function reads tags sequentially, skipping non-matching fields,
// until it finds the target or reaches the end.
func extractBytesField(b []byte, targetField protowire.Number) ([]byte, error) {
	for len(b) > 0 {
		// Read the tag to get the field number and wire type.
		num, wtype, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, errors.New("invalid protobuf tag")
		}
		b = b[n:]

		// If this is the field we're looking for, read and return its value.
		if num == targetField && wtype == protowire.BytesType {
			val, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return nil, errors.New("invalid protobuf bytes field")
			}
			return val, nil
		}

		// Not our field — skip past its value to reach the next tag.
		vn := protowire.ConsumeFieldValue(num, wtype, b)
		if vn < 0 {
			return nil, errors.New("invalid protobuf field value")
		}
		b = b[vn:]
	}
	return nil, nil
}

// extractTypeAndTxID scans a ChannelHeader wire-format message for
// field 1 (Type, varint) and field 5 (TxId, string/bytes).
// It returns early once both fields are found. If a field is absent
// (proto3 omits default values), its zero value is returned.
//
// Using the example: [08 03 2a 03 747831]
//
//	Iteration 1: tag=08 → field=1, varint → read 03 → Type=3
//	Iteration 2: tag=2a → field=5, bytes  → read len=3, "tx1" → TxId="tx1"
//	Both found → return early.
func extractTypeAndTxID(b []byte) (int32, string, error) {
	var headerType int32
	var txID string
	foundType := false
	foundTxID := false

	for len(b) > 0 {
		// Read the next tag.
		num, wtype, n := protowire.ConsumeTag(b)
		if n < 0 {
			return 0, "", errors.New("invalid tag in channel header")
		}
		b = b[n:]

		switch {
		case num == 1 && wtype == protowire.VarintType:
			// ChannelHeader.type (int32, field 1).
			// Example: [08 03] → tag=08 (field 1, varint), value=03 → Type=3.
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return 0, "", errors.New("invalid varint for type field")
			}
			headerType = int32(v)
			foundType = true
			b = b[vn:]
			if foundTxID {
				return headerType, txID, nil
			}

		case num == 5 && wtype == protowire.BytesType:
			// ChannelHeader.tx_id (string, field 5).
			// Example: [2a 03 747831] → tag=2a (field 5, bytes), len=3, value="tx1".
			val, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return 0, "", errors.New("invalid bytes for tx_id field")
			}
			txID = string(val)
			foundTxID = true
			b = b[vn:]
			if foundType {
				return headerType, txID, nil
			}

		default:
			// Some other field (version, timestamp, channel_id, etc.) — skip it.
			vn := protowire.ConsumeFieldValue(num, wtype, b)
			if vn < 0 {
				return 0, "", errors.New("invalid field value in channel header")
			}
			b = b[vn:]
		}
	}
	return headerType, txID, nil
}
