package main

import (
	"errors"
	"strings"
	"testing"
)

// The version picks the broker image that vault set and the start-and-retry
// step run. A release binary has only its stamped tag, so ignoring it breaks
// every downloaded nobs; NOBS_VERSION must still win so a person testing a
// local image is not switched to a release image without noticing.
func TestStampedVersionUsedUnlessOverridden(t *testing.T) {
	tests := []struct {
		name     string
		override string
		stamped  string
		want     string
	}{
		{name: "stamped-tag", stamped: "v0.3.1", want: "v0.3.1"},
		{name: "override-wins", override: "dev", stamped: "v0.3.1", want: "dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NOBS_VERSION", tt.override)
			stamped := version
			version = tt.stamped
			t.Cleanup(func() { version = stamped })

			got, err := brokerVersion()

			if err != nil || got != tt.want {
				t.Fatalf("brokerVersion() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

// A test binary has no release version, like a go build in a checkout. The
// error is all a person sees, so it must name both ways out: a release binary
// and NOBS_VERSION.
func TestNoVersionErrorNamesReleaseBinary(t *testing.T) {
	t.Setenv("NOBS_VERSION", "")
	stamped := version
	version = ""
	t.Cleanup(func() { version = stamped })

	_, err := brokerVersion()

	var nobsErr *NOBSError
	if !errors.As(err, &nobsErr) || nobsErr.Code != NOBSErrBrokerUnavailable {
		t.Fatalf("err = %v; want broker_unavailable", err)
	}
	for _, want := range []string{"github.com/henrico813/nobs/releases", "NOBS_VERSION"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}
