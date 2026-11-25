package registry_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/registry"
)

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
	r := registry.New()
	registry.RegisterConfig(r, "fake", func(c fakeConfig) (*fakeProvider, error) {
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
			got, err := r.Build("fake", tt.opts)
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

func TestUnknownProvider(t *testing.T) {
	if _, err := registry.New().Build("nope", nil); !errors.Is(err, registry.ErrUnknownProvider) {
		t.Errorf("err = %v, want ErrUnknownProvider", err)
	}
}

func TestRegistry(t *testing.T) {
	r := registry.New()
	r.Register("b", func(registry.Options) (transit.Provider, error) { return nil, nil })
	r.Register("a", func(registry.Options) (transit.Provider, error) { return nil, nil })
	if diff := cmp.Diff([]string{"a", "b"}, r.Names()); diff != "" {
		t.Errorf("Names mismatch (-want +got):\n%s", diff)
	}
	if _, err := r.Build("missing", nil); err == nil {
		t.Error("Build(missing) succeeded")
	}
	defer func() {
		if recover() == nil {
			t.Error("duplicate Register did not panic")
		}
	}()
	r.Register("a", nil)
}
