package javaser

// Castor upload wire (STOMP payloads); only used by the test tooling to upload tuples.

var (
	descTupleType  = &desc{ent + "TupleType", 0, scSerializable | scEnum, nil, descEnumBase}
	descTupleChunk = &desc{ent + "TupleChunk", -5025431542939631318, scSerializable,
		[]field{{'L', "chunkId", "Ljava/util/UUID;"}, {'L', "tupleType", "Lio/carbynestack/castor/common/entities/TupleType;"}, {'[', "tuples", "[B"}}, nil}
)

// EncodeTupleChunk serialises a TupleChunk.
func EncodeTupleChunk(chunkID, tupleTypeName string, tuples []byte) []byte {
	w := newWriter()
	w.newObject(descTupleChunk)
	w.uuid(chunkID)
	w.enum(descTupleType, tupleTypeName)
	w.byteArray(tuples)
	return w.b.Bytes()
}

// UploadResponse is the decoded UploadTupleChunkResponse.
type UploadResponse struct {
	ChunkID  string // "" when null
	ErrorMsg *string
	Success  bool
}

// DecodeUploadResponse parses a Java-serialized UploadTupleChunkResponse.
func DecodeUploadResponse(b []byte) (*UploadResponse, error) {
	v, err := parse(b)
	if err != nil {
		return nil, err
	}
	o, ok := v.(*Object)
	if !ok || o.Class != "io.carbynestack.castor.common.websocket.UploadTupleChunkResponse" {
		return nil, ErrFormat
	}
	id, err := uuidFrom(o.Fields["chunkId"])
	if err != nil {
		return nil, err
	}
	resp := &UploadResponse{ChunkID: id}
	resp.Success, _ = o.Fields["isSuccess"].(bool)
	if s, ok := o.Fields["errorMsg"].(string); ok {
		resp.ErrorMsg = &s
	}
	return resp, nil
}
