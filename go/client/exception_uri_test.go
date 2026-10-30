package client

// Port of AmphoraClientExceptionTest (the uriStatusMap accessor has no Go counterpart: the
// per-endpoint failures are aggregated into the message, see TestTagCallsAndErrorAggregation).

import (
	"errors"
	"strings"
	"testing"
)

func TestClientErrorMessageAndCause(t *testing.T) {
	cause := errors.New("Root of all evil")
	err := &ClientError{Msg: "Description", Cause: cause}
	if err.Msg != "Description" || !errors.Is(err, cause) || errors.Unwrap(err) != cause {
		t.Fatal(err)
	}
	if !strings.HasPrefix(err.Error(), "Description") {
		t.Fatal(err.Error())
	}
	if got := (&ClientError{Msg: "no cause"}).Error(); got != "no cause" {
		t.Fatal(got)
	}
}
