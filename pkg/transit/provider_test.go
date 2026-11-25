package transit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

func TestSortByTime(t *testing.T) {
	a, b, c := dep("1", "A", 5, false), dep("2", "B", 2, false), dep("3", "C", 5, false)
	got := []transit.Departure{a, b, c}
	transit.SortByTime(got)
	// Ties keep their order.
	if diff := cmp.Diff([]transit.Departure{b, a, c}, got); diff != "" {
		t.Errorf("SortByTime (-want +got):\n%s", diff)
	}
}

type fakeOptions struct {
	name string
	err  error
}

func (o fakeOptions) Decode(v any) error {
	if o.err != nil {
		return o.err
	}
	v.(*fakeConfig).Name = o.name
	return nil
}

type fakeConfig struct{ Name string }

type fakeProvider struct{ Name string }

func (*fakeProvider) Departures(context.Context, transit.Query) ([]transit.Departure, error) {
	return nil, nil
}

func TestRegisterConfig(t *testing.T) {
	r := transit.NewRegistry()
	transit.RegisterConfig(r, "fake", func(c fakeConfig) (*fakeProvider, error) {
		if c.Name == "" {
			return nil, errors.New("name is required")
		}
		return &fakeProvider{Name: c.Name}, nil
	})
	tests := []struct {
		name    string
		opts    fakeOptions
		want    transit.Provider
		wantErr string
	}{
		{"builds from the decoded block", fakeOptions{name: "x"}, &fakeProvider{Name: "x"}, ""},
		{"decode error", fakeOptions{err: errors.New("bad key")}, nil, `provider "fake": bad key`},
		// A nil *fakeProvider must not come back as a non-nil Provider.
		{"build error", fakeOptions{}, nil, `provider "fake": name is required`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.New("fake", tt.opts)
			var gotErr string
			if err != nil {
				gotErr = err.Error()
			}
			if gotErr != tt.wantErr {
				t.Errorf("err = %q, want %q", gotErr, tt.wantErr)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("provider mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
