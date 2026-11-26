package directory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// DefaultTransitlandURL is Transitland's REST API.
const DefaultTransitlandURL = "https://transit.land/api/v2/rest"

const tlLimit = 8 << 20

type tlOperators struct {
	Operators []tlOperator `json:"operators"`
}

type tlOperator struct {
	OnestopID string  `json:"onestop_id"`
	Name      string  `json:"name"`
	ShortName *string `json:"short_name"`
	Agencies  []struct {
		AgencyName string `json:"agency_name"`
		Places     []struct {
			Adm0 *string `json:"adm0_name"`
			Adm1 *string `json:"adm1_name"`
			City *string `json:"city_name"`
		} `json:"places"`
	} `json:"agencies"`
	Feeds []tlFeedRef `json:"feeds"`
}

type tlFeedRef struct {
	OnestopID string  `json:"onestop_id"`
	Name      *string `json:"name"`
	Spec      string  `json:"spec"`
}

type tlFeeds struct {
	Feeds []tlFeed `json:"feeds"`
}

type tlFeed struct {
	OnestopID string `json:"onestop_id"`
	Spec      string `json:"spec"`
	URLs      struct {
		StaticCurrent       string `json:"static_current"`
		RealtimeTripUpdates string `json:"realtime_trip_updates"`
	} `json:"urls"`
	Authorization struct {
		Type    string `json:"type"`
		InfoURL string `json:"info_url"`
	} `json:"authorization"`
	License struct {
		SPDX string `json:"spdx_identifier"`
		URL  string `json:"url"`
	} `json:"license"`
}

const (
	specGTFS   = "GTFS"
	specGTFSRT = "GTFS_RT"
)

// searchTransitland asks Transitland for operators matching words, which
// it matches against names and places. Each of an operator's schedules is
// a system.
func (d *Directory) searchTransitland(ctx context.Context, words string) ([]System, error) {
	var ops tlOperators
	q := url.Values{"search": {words}, "limit": {"20"}}
	if err := d.transitland(ctx, "/operators?"+q.Encode(), &ops); err != nil {
		return nil, err
	}
	var out []System
	for _, op := range ops.Operators {
		data := DataSchedule
		for _, f := range op.Feeds {
			if strings.EqualFold(f.Spec, specGTFSRT) {
				data = DataRealtime
			}
		}
		for _, f := range op.Feeds {
			if !strings.EqualFold(f.Spec, specGTFS) {
				continue
			}
			s := System{ID: f.OnestopID, Agency: op.title(), Place: op.place(), Data: data, Source: SourceTransitland}
			if f.Name != nil {
				s.Name = *f.Name
			}
			out = append(out, s)
		}
	}
	return out, nil
}

func (op tlOperator) title() string {
	if op.ShortName != nil && *op.ShortName != "" && !strings.EqualFold(*op.ShortName, op.Name) {
		return op.Name + " (" + *op.ShortName + ")"
	}
	return op.Name
}

func (op tlOperator) place() string {
	for _, a := range op.Agencies {
		for _, p := range a.Places {
			if p.City != nil && *p.City != "" {
				return joinNonEmpty(", ", *p.City, deref(p.Adm1), deref(p.Adm0))
			}
		}
	}
	for _, a := range op.Agencies {
		for _, p := range a.Places {
			if s := joinNonEmpty(", ", deref(p.Adm1), deref(p.Adm0)); s != "" {
				return s
			}
		}
	}
	return ""
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// resolveTransitland builds the block for a Transitland schedule feed. A
// gtfsrt block, which spends no Transitland quota, is written when the
// schedule needs no key and its realtime feeds are unambiguous and need
// none: the feed's operator has this one schedule. Otherwise the block
// reads departures through Transitland.
func (d *Directory) resolveTransitland(ctx context.Context, id string) (System, Block, error) {
	feed, err := d.transitlandFeed(ctx, id)
	if err != nil {
		return System{}, Block{}, err
	}
	if !strings.EqualFold(feed.Spec, specGTFS) {
		return System{}, Block{}, fmt.Errorf("%s is a %s feed; give its GTFS schedule's ID", id, feed.Spec)
	}
	var ops tlOperators
	if err := d.transitland(ctx, "/operators?"+url.Values{"feed_onestop_id": {id}}.Encode(), &ops); err != nil {
		return System{}, Block{}, err
	}
	sys := System{ID: id, Agency: id, Data: DataSchedule, Source: SourceTransitland, License: firstNonEmpty(feed.License.SPDX, feed.License.URL), StaticURL: feed.URLs.StaticCurrent}
	var rtRefs []string
	schedules := 0
	if len(ops.Operators) > 0 {
		op := ops.Operators[0]
		sys.Agency, sys.Place = op.title(), op.place()
		for _, f := range op.Feeds {
			switch {
			case strings.EqualFold(f.Spec, specGTFS):
				schedules++
			case strings.EqualFold(f.Spec, specGTFSRT):
				rtRefs = append(rtRefs, f.OnestopID)
			}
		}
	}
	if len(rtRefs) > 0 {
		sys.Data = DataRealtime
	}

	direct := feed.Authorization.Type == "" && feed.URLs.StaticCurrent != "" && len(ops.Operators) == 1 && (len(rtRefs) == 0 || schedules == 1)
	var realtime []any
	for _, ref := range rtRefs {
		if !direct {
			break
		}
		rt, err := d.transitlandFeed(ctx, ref)
		if err != nil {
			return System{}, Block{}, err
		}
		switch {
		case rt.Authorization.Type != "":
			direct = false
		case rt.URLs.RealtimeTripUpdates != "":
			realtime = append(realtime, rt.URLs.RealtimeTripUpdates)
		}
	}
	if direct {
		opts := map[string]any{"static_url": feed.URLs.StaticCurrent}
		if len(realtime) > 0 {
			opts["realtime_urls"] = realtime
		}
		return sys, Block{Type: "gtfsrt", Options: opts, Comment: fmt.Sprintf("Feed URLs from Transitland (%s).", id)}, nil
	}
	sys.Needs = []string{TransitlandKeyVar}
	return sys, Block{
		Type:    "transitland",
		Options: map[string]any{"feed": id, "api_key": "${" + TransitlandKeyVar + "}"},
		Comment: fmt.Sprintf("Through Transitland (%s); requests count against its monthly quota.", id),
	}, nil
}

func (d *Directory) transitlandFeed(ctx context.Context, id string) (tlFeed, error) {
	var feeds tlFeeds
	if err := d.transitland(ctx, "/feeds/"+url.PathEscape(id), &feeds); err != nil {
		return tlFeed{}, err
	}
	if len(feeds.Feeds) == 0 {
		return tlFeed{}, fmt.Errorf("%w %q on Transitland", ErrUnknownID, id)
	}
	return feeds.Feeds[0], nil
}

func (d *Directory) transitland(ctx context.Context, path string, v any) error {
	base := d.TransitlandURL
	if base == "" {
		base = DefaultTransitlandURL
	}
	u := strings.TrimRight(base, "/") + path
	shown := u
	if i := strings.IndexByte(shown, '?'); i >= 0 {
		shown = shown[:i]
	}
	status, b, err := d.get(ctx, u, http.Header{"Accept": {"application/json"}, "Apikey": {d.TransitlandKey}}, tlLimit)
	if err != nil {
		return fmt.Errorf("GET %s: %w", shown, err)
	}
	text := fmt.Sprintf("%d %s", status, http.StatusText(status))
	switch {
	case status == http.StatusNotFound:
		return fmt.Errorf("%w: GET %s: %s", ErrUnknownID, shown, text)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("GET %s: %s: check %s", shown, text, TransitlandKeyVar)
	case status != http.StatusOK:
		return fmt.Errorf("GET %s: %s", shown, text)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("GET %s: %w", shown, err)
	}
	return nil
}
